// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Fake resolves a ChangeRef to a fixture directory on disk.
//
// It exists so that no test anywhere in this repository has to fabricate a changeset inline or
// stand in a placeholder for a real fetch. Fixtures are files, so they can be reviewed, diffed,
// and argued about like any other artifact.
//
// Two directory shapes are recognised:
//
//	<ref>/migrations/...      every file is treated as ADDED, the shape of a migration PR
//	<ref>/old/... + new/...   the two trees are diffed by path
type Fake struct {
	root string
}

// NewFake returns a Fake rooted at the given fixture directory.
func NewFake(root string) *Fake { return &Fake{root: root} }

// The subdirectory names that give a fixture its shape.
const (
	migrationsDir = "migrations"
	planDir       = "plan"
	oldDir        = "old"
	newDir        = "new"
)

// ChangedFiles implements FileProvider by reading the fixture directory named by ref.
func (f *Fake) ChangedFiles(ctx context.Context, ref domain.ChangeRef) ([]domain.ChangedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fake provider: %w", err)
	}

	dir := filepath.Join(f.root, filepath.FromSlash(string(ref)))
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("fake provider: fixture %q: %w", ref, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fake provider: fixture %q is not a directory: %w", ref, domain.ErrProviderFailed)
	}

	var files []domain.ChangedFile

	if _, err := os.Stat(filepath.Join(dir, migrationsDir)); err == nil {
		files, err = f.readMigrations(dir)
		if err != nil {
			return nil, err
		}
	} else if _, err := os.Stat(filepath.Join(dir, planDir)); err == nil {
		// A Terraform plan fixture: one generated document, added. Same shape as migrations/
		// but named for what it holds, because calling a plan a migration would mislead every
		// contributor who opened the directory.
		files, err = f.readAllAdded(dir, planDir)
		if err != nil {
			return nil, err
		}
	} else {
		files, err = f.readTreePair(dir)
		if err != nil {
			return nil, err
		}
	}

	// Sorting here rather than at the call site: the interface promises a stable order, and a
	// provider that forgets would surface as a flapping InputDigest, which is far harder to
	// diagnose than a failing provider test.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// readMigrations treats every file under migrations/ as newly added, which is what a migration
// pull request actually looks like.
func (f *Fake) readMigrations(dir string) ([]domain.ChangedFile, error) {
	return f.readAllAdded(dir, migrationsDir)
}

// readAllAdded treats every file under one subdirectory as newly added.
//
// Two fixture shapes use it: migrations/ for SQL and plan/ for a Terraform plan document. They
// are the same operation and differ only in what the directory is called, which matters because
// a contributor opening the directory should see the word for what is in it.
func (f *Fake) readAllAdded(dir, sub string) ([]domain.ChangedFile, error) {
	var out []domain.ChangedFile

	root := filepath.Join(dir, sub)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || skipFile(d.Name()) {
			return nil
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("relativise %s: %w", p, err)
		}

		out = append(out, domain.ChangedFile{
			Path:    filepath.ToSlash(rel),
			Status:  domain.StatusAdded,
			Current: content,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fake provider: reading %s: %w", sub, err)
	}
	return out, nil
}

// readTreePair diffs old/ against new/ by path.
//
// Files present and byte-identical on both sides are still returned, as MODIFIED. That is
// deliberate: rules such as K8S009 ask whether a workload still references a ConfigMap the
// changeset deletes, and that question cannot be answered from the deleted file alone. See the
// open question in docs/SPECIFICATION.md about whether the real providers must supply this
// context too.
func (f *Fake) readTreePair(dir string) ([]domain.ChangedFile, error) {
	oldFiles, err := readTree(filepath.Join(dir, oldDir))
	if err != nil {
		return nil, err
	}
	newFiles, err := readTree(filepath.Join(dir, newDir))
	if err != nil {
		return nil, err
	}

	if oldFiles == nil && newFiles == nil {
		return nil, fmt.Errorf("fake provider: %s has no migrations/, old/ or new/ directory: %w", dir, domain.ErrProviderFailed)
	}

	seen := make(map[string]struct{}, len(oldFiles)+len(newFiles))
	paths := make([]string, 0, len(oldFiles)+len(newFiles))
	for p := range oldFiles {
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for p := range newFiles {
		if _, ok := seen[p]; !ok {
			paths = append(paths, p)
		}
	}

	out := make([]domain.ChangedFile, 0, len(paths))
	for _, p := range paths {
		before, hadBefore := oldFiles[p]
		after, hadAfter := newFiles[p]

		switch {
		case hadBefore && hadAfter:
			out = append(out, domain.ChangedFile{
				Path:     p,
				Status:   domain.StatusModified,
				Previous: before,
				Current:  after,
			})
		case hadAfter:
			out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusAdded, Current: after})
		default:
			out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusRemoved, Previous: before})
		}
	}
	return out, nil
}

// readTree reads one side of a fixture, keyed by path relative to that side. A missing
// directory is not an error: an added-only or removed-only changeset is a legitimate fixture.
func readTree(root string) (map[string][]byte, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}

	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || skipFile(d.Name()) {
			return nil
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relativise %s: %w", p, err)
		}

		out[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fake provider: reading %s: %w", root, err)
	}
	return out, nil
}

// skipFile filters out repository bookkeeping that is not part of any changeset.
func skipFile(name string) bool {
	return name == ".gitkeep" || name == "expected.json" || strings.HasPrefix(name, ".")
}

// FixtureRef builds a ChangeRef for a fixture from its group and directory name.
func FixtureRef(group, name string) domain.ChangeRef {
	return domain.ChangeRef(path.Join(group, name))
}
