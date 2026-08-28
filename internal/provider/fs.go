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

// FS resolves a changeset from the local filesystem.
//
// Two shapes are supported. With only After paths, every file is reported as ADDED — the shape
// of a migration pull request, and enough for the PostgreSQL rules. With Before paths as well,
// the two trees are diffed by relative path, which is what the Kubernetes rules need in order to
// compare old against new.
type FS struct {
	before []string
	after  []string
}

// NewFS returns a filesystem provider.
//
// before may be empty, in which case every file in after is treated as newly added.
//
// It no longer takes a predicate. Choosing what to read is the caller's decision and is made
// against the complete listing — see Select — because the provider cannot answer "is this file
// worth reading" without knowing what else is in the changeset.
func NewFS(before, after []string) *FS {
	return &FS{
		before: append([]string(nil), before...),
		after:  append([]string(nil), after...),
	}
}

// List implements FileProvider.
//
// The ref is ignored: a local directory has no revision to resolve, so the paths given at
// construction are the reference. The parameter stays because the interface is shared with the
// GitHub provider, where a ref is the only thing that identifies the change.
//
// The walk visits every entry and reads none of them. That was already true of the old walk —
// it decided inclusion before calling os.ReadFile — so enumerating separately costs one extra
// traversal of directory metadata and no file content at all.
func (f *FS) List(ctx context.Context, _ domain.ChangeRef) ([]Path, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fs provider: %w", err)
	}
	if len(f.after) == 0 && len(f.before) == 0 {
		return nil, fmt.Errorf("fs provider: no paths given: %w", domain.ErrProviderFailed)
	}

	after, err := f.listTrees(f.after)
	if err != nil {
		return nil, err
	}

	if len(f.before) == 0 {
		out := make([]Path, 0, len(after))
		for p := range after {
			out = append(out, Path{Path: p, Status: domain.StatusAdded})
		}
		SortPaths(out)
		return out, nil
	}

	before, err := f.listTrees(f.before)
	if err != nil {
		return nil, err
	}

	return diffListings(before, after), nil
}

// Read implements FileProvider.
//
// It reads exactly the paths given. A path that vanished between List and Read — a concurrent
// checkout, a temporary file — is reported rather than skipped: silently returning fewer files
// than were asked for is how a coverage denominator loses entries.
func (f *FS) Read(ctx context.Context, _ domain.ChangeRef, paths []Path) ([]domain.ChangedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fs provider: %w", err)
	}

	wanted := map[string]bool{}
	for _, p := range paths {
		wanted[p.Path] = true
	}

	after, err := f.readTrees(f.after, wanted)
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

	before, err := f.readTrees(f.before, wanted)
	if err != nil {
		return nil, err
	}

	return diffTrees(before, after), nil
}

// listTrees enumerates every file under the given roots, keyed by a path relative to its root so
// that two trees rooted differently still compare. No content is read.
func (f *FS) listTrees(roots []string) (map[string]bool, error) {
	out := map[string]bool{}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("fs provider: %s: %w", root, err)
		}

		// A single file is addressed by its own name, so that "revctl check migrations/x.sql"
		// reports the path the user typed.
		if !info.IsDir() {
			out[filepath.ToSlash(filepath.Base(root))] = true
			continue
		}

		if err := f.walkNames(root, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (f *FS) walkNames(root string, out map[string]bool) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			// Version-control and dependency directories contain nothing the engine analyzes
			// and can be enormous. Skipping them is an enumeration decision, not a filter on
			// what is interesting: nothing in .git is a file of the changeset at all.
			if skipDir(d.Name()) && p != root {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relativising %s: %w", p, err)
		}

		out[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("fs provider: walking %s: %w", root, err)
	}
	return nil
}

// readTrees reads the content of the wanted paths under the given roots.
func (f *FS) readTrees(roots []string, wanted map[string]bool) (map[string][]byte, error) {
	out := map[string][]byte{}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("fs provider: %s: %w", root, err)
		}

		if !info.IsDir() {
			rel := filepath.ToSlash(filepath.Base(root))
			if wanted[rel] {
				content, err := os.ReadFile(root)
				if err != nil {
					return nil, fmt.Errorf("fs provider: reading %s: %w", root, err)
				}
				out[rel] = content
			}
			continue
		}

		if err := f.walkContents(root, wanted, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (f *FS) walkContents(root string, wanted map[string]bool, out map[string][]byte) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
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

		if !wanted[rel] {
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

// diffListings classifies each enumerated path by which side it appears on.
//
// It mirrors diffTrees exactly, on names alone. A file present on both sides is MODIFIED even
// when its bytes turn out to be identical — the listing cannot know that, and the rules that
// need unchanged context files (K8S003, K8S009) want them either way.
func diffListings(before, after map[string]bool) []Path {
	seen := map[string]bool{}
	out := make([]Path, 0, len(before)+len(after))

	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true

		status := domain.StatusModified
		switch {
		case !before[p]:
			status = domain.StatusAdded
		case !after[p]:
			status = domain.StatusRemoved
		}
		out = append(out, Path{Path: p, Status: status})
	}

	for p := range before {
		add(p)
	}
	for p := range after {
		add(p)
	}

	SortPaths(out)
	return out
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
