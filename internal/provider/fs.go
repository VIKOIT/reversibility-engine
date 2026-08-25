// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Include decides whether a path belongs in the changeset.
//
// The provider is given this rather than deciding for itself, because knowing which extensions
// matter is the analyzers' business. Passing nil includes every file.
type Include func(path string) bool

// FS resolves a changeset from the local filesystem.
//
// Two shapes are supported. With only After paths, every file is reported as ADDED — the shape
// of a migration pull request, and enough for the PostgreSQL rules. With Before paths as well,
// the two trees are diffed by relative path, which is what the Kubernetes rules need in order to
// compare old against new.
type FS struct {
	before  []string
	after   []string
	include Include
}

// NewFS returns a filesystem provider.
//
// before may be empty, in which case every file in after is treated as newly added.
func NewFS(before, after []string, include Include) *FS {
	return &FS{
		before:  append([]string(nil), before...),
		after:   append([]string(nil), after...),
		include: include,
	}
}

// ChangedFiles implements FileProvider.
//
// The ref is ignored: a local directory has no revision to resolve, so the paths given at
// construction are the reference. The parameter stays because the interface is shared with the
// GitHub provider, where a ref is the only thing that identifies the change.
func (f *FS) ChangedFiles(ctx context.Context, _ domain.ChangeRef) ([]domain.ChangedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fs provider: %w", err)
	}
	if len(f.after) == 0 && len(f.before) == 0 {
		return nil, fmt.Errorf("fs provider: no paths given: %w", domain.ErrProviderFailed)
	}

	after, err := f.readTrees(f.after)
	if err != nil {
		return nil, err
	}

	if len(f.before) == 0 {
		out := make([]domain.ChangedFile, 0, len(after))
		for path, content := range after {
			out = append(out, domain.ChangedFile{
				Path:    path,
				Status:  domain.StatusAdded,
				Current: content,
			})
		}
		sortByPath(out)
		return out, nil
	}

	before, err := f.readTrees(f.before)
	if err != nil {
		return nil, err
	}

	return diffTrees(before, after), nil
}

// readTrees reads every included file under the given roots, keyed by a path relative to its
// root so that two trees rooted differently still compare.
func (f *FS) readTrees(roots []string) (map[string][]byte, error) {
	out := map[string][]byte{}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("fs provider: %s: %w", root, err)
		}

		// A single file is addressed by its own name, so that "revctl check migrations/x.sql"
		// reports the path the user typed.
		if !info.IsDir() {
			if f.included(filepath.ToSlash(filepath.Base(root))) {
				content, err := os.ReadFile(root)
				if err != nil {
					return nil, fmt.Errorf("fs provider: reading %s: %w", root, err)
				}
				out[filepath.ToSlash(filepath.Base(root))] = content
			}
			continue
		}

		if err := f.walk(root, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (f *FS) walk(root string, out map[string][]byte) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			// Version-control and dependency directories contain nothing the engine analyzes
			// and can be enormous.
			if skipDir(d.Name()) && p != root {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relativising %s: %w", p, err)
		}
		rel = filepath.ToSlash(rel)

		// The predicate is given the path as it will appear in the changeset, not the path on
		// this machine. A caller matching "legacy/**" means the directory in the repository,
		// and testing that against an absolute temp-directory path would silently match
		// nothing — which for an ignore rule means analyzing exactly what was excluded.
		if !f.included(rel) {
			return nil
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}

		out[rel] = content
		return nil
	})
	if err != nil {
		return fmt.Errorf("fs provider: walking %s: %w", root, err)
	}
	return nil
}

func (f *FS) included(path string) bool {
	if f.include == nil {
		return true
	}
	return f.include(filepath.ToSlash(path))
}

// diffTrees classifies each path by which side it appears on.
//
// Files present and identical on both sides are still returned, as MODIFIED. That is deliberate:
// rules such as K8S003 and K8S009 ask about objects the change did not touch — the StorageClass
// behind a deleted claim, the workload still mounting a deleted ConfigMap — and those questions
// cannot be answered from the changed files alone.
func diffTrees(before, after map[string][]byte) []domain.ChangedFile {
	paths := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}

	for p := range before {
		paths = append(paths, p)
		seen[p] = true
	}
	for p := range after {
		if !seen[p] {
			paths = append(paths, p)
		}
	}

	out := make([]domain.ChangedFile, 0, len(paths))
	for _, p := range paths {
		previous, had := before[p]
		current, has := after[p]

		switch {
		case had && has:
			out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusModified, Previous: previous, Current: current})
		case has:
			out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusAdded, Current: current})
		default:
			out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusRemoved, Previous: previous})
		}
	}

	sortByPath(out)
	return out
}

// sortByPath enforces the ordering the interface promises. Callers hash the result into the
// certificate's InputDigest, and an unstable order would produce a different digest for
// identical input.
func sortByPath(files []domain.ChangedFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", ".terraform":
		return true
	default:
		return strings.HasPrefix(name, ".") && name != "."
	}
}
