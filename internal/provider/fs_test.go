// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func sqlOnly(listed []provider.Path) []provider.Path {
	out := make([]provider.Path, 0, len(listed))
	for _, p := range listed {
		if strings.HasSuffix(p.Path, ".sql") {
			out = append(out, p)
		}
	}
	return out
}

// resolveFS runs both phases against a filesystem provider.
func resolveFS(ctx context.Context, before, after []string, choose provider.Select) ([]domain.ChangedFile, error) {
	_, files, err := provider.Resolve(ctx, provider.NewFS(before, after), "", choose)
	return files, err
}

// listFS enumerates without reading — the half that must not touch content.
func listFS(ctx context.Context, before, after []string) ([]provider.Path, error) {
	return provider.NewFS(before, after).List(ctx, "")
}

func TestFSTreatsASingleTreeAsAllAdded(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"0001.up.sql":        "SELECT 1;",
		"0001.down.sql":      "SELECT 2;",
		"nested/0002.up.sql": "SELECT 3;",
	})

	got, err := resolveFS(context.Background(), nil, []string{root}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d files, want 3: %v", len(got), paths(got))
	}
	for _, f := range got {
		if f.Status != domain.StatusAdded {
			t.Errorf("%s: status %q, want ADDED", f.Path, f.Status)
		}
		if f.Previous != nil {
			t.Errorf("%s: an added file must have no previous content", f.Path)
		}
		if len(f.Current) == 0 {
			t.Errorf("%s: no content read", f.Path)
		}
		// Paths must use forward slashes on every platform, or a certificate produced on
		// Windows would differ from one produced in CI for the same change.
		if strings.Contains(f.Path, `\`) {
			t.Errorf("%s: path contains a backslash", f.Path)
		}
	}
}

func TestFSIncludePredicateIsHonoured(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"0001.up.sql": "SELECT 1;",
		"README.md":   "# hello",
		"main.go":     "package main",
	})

	got, err := resolveFS(context.Background(), nil, []string{root}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if len(got) != 1 || got[0].Path != "0001.up.sql" {
		t.Errorf("got %v, want only the .sql file", paths(got))
	}
}

// Walking .git or node_modules would read an enormous amount of data the engine never looks at.
func TestFSSkipsNoiseDirectories(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"0001.up.sql":          "SELECT 1;",
		".git/objects/x.sql":   "SELECT 2;",
		"node_modules/p/y.sql": "SELECT 3;",
		"vendor/z.sql":         "SELECT 4;",
		".hidden/secret.sql":   "SELECT 5;",
	})

	got, err := resolveFS(context.Background(), nil, []string{root}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if len(got) != 1 || got[0].Path != "0001.up.sql" {
		t.Errorf("got %v, want only the top-level migration", paths(got))
	}
}

func TestFSDiffsTwoTrees(t *testing.T) {
	t.Parallel()

	before := writeFiles(t, map[string]string{
		"kept.sql":    "SELECT 1;",
		"changed.sql": "SELECT 2;",
		"removed.sql": "SELECT 3;",
	})
	after := writeFiles(t, map[string]string{
		"kept.sql":    "SELECT 1;",
		"changed.sql": "SELECT 99;",
		"added.sql":   "SELECT 4;",
	})

	got, err := resolveFS(context.Background(), []string{before}, []string{after}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	byPath := map[string]domain.ChangedFile{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	// An unchanged file is still returned, as MODIFIED with identical sides: rules such as
	// K8S003 and K8S009 need context the change did not touch.
	if f := byPath["kept.sql"]; f.Status != domain.StatusModified || string(f.Previous) != string(f.Current) {
		t.Errorf("kept.sql: status %q, previous==current: %v", f.Status, string(f.Previous) == string(f.Current))
	}
	if f := byPath["changed.sql"]; f.Status != domain.StatusModified || string(f.Current) != "SELECT 99;" {
		t.Errorf("changed.sql: status %q content %q", f.Status, f.Current)
	}
	if f := byPath["removed.sql"]; f.Status != domain.StatusRemoved || f.Current != nil {
		t.Errorf("removed.sql: status %q, current %q", f.Status, f.Current)
	}
	if f := byPath["added.sql"]; f.Status != domain.StatusAdded || f.Previous != nil {
		t.Errorf("added.sql: status %q, previous %q", f.Status, f.Previous)
	}
}

// A single file argument is addressed by its own name, so the report matches what the user typed.
func TestFSAcceptsASingleFile(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{"0001.up.sql": "SELECT 1;"})

	got, err := resolveFS(context.Background(), nil, []string{filepath.Join(root, "0001.up.sql")}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if len(got) != 1 || got[0].Path != "0001.up.sql" {
		t.Errorf("got %v, want [0001.up.sql]", paths(got))
	}
}

// The interface promises sorted output because InputDigest is computed from it.
func TestFSOutputIsSorted(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"zzz.sql":     "SELECT 1;",
		"aaa.sql":     "SELECT 2;",
		"mmm/bbb.sql": "SELECT 3;",
	})

	got, err := resolveFS(context.Background(), nil, []string{root}, sqlOnly)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if p := paths(got); !sort.StringsAreSorted(p) {
		t.Errorf("output is not sorted: %v", p)
	}
}

func TestFSErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "nope")
		if _, err := resolveFS(context.Background(), nil, []string{missing}, nil); err == nil {
			t.Error("expected an error for a missing path")
		}
	})

	t.Run("no paths at all", func(t *testing.T) {
		t.Parallel()

		if _, err := resolveFS(context.Background(), nil, nil, nil); err == nil {
			t.Error("expected an error when no paths are given")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		root := writeFiles(t, map[string]string{"a.sql": "SELECT 1;"})
		if _, err := resolveFS(ctx, nil, []string{root}, nil); err == nil {
			t.Error("expected an error for a cancelled context")
		}
	})
}

// The two phases must disagree about their contents, and that is the whole contract.
//
// docs/SPECIFICATION.md §2: enumeration and retrieval are separate concerns. List reports what
// exists; Read fetches what was chosen. A provider whose List returned only what its Select
// would have kept would satisfy every other test here and reintroduce the defect the split was
// made to remove — coverage would once again be measured against the files that were read.
func TestListEnumeratesMoreThanReadFetches(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"db/migrate/0001.up.sql": "SELECT 1;",
		"db/migrate/README.md":   "# notes",
		"db/migrate/.gitkeep":    "",
		"docs/guide.md":          "words",
	})

	listed, err := listFS(context.Background(), nil, []string{root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Everything, including the files no analyzer would ever want. This is the denominator.
	if len(listed) != 4 {
		t.Fatalf("List returned %d paths, want all 4: %v", len(listed), listed)
	}

	var sawReadme bool
	for _, p := range listed {
		if p.Path == "db/migrate/README.md" {
			sawReadme = true
		}
	}
	if !sawReadme {
		t.Error("List omitted a file no analyzer claims; coverage cannot report what was never enumerated")
	}

	// And Read fetches only what was chosen.
	files, err := resolveFS(context.Background(), nil, []string{root}, sqlOnly)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(files) != 1 || files[0].Path != "db/migrate/0001.up.sql" {
		t.Fatalf("Read returned %v, want only the .sql", paths(files))
	}

	if len(listed) <= len(files) {
		t.Errorf("List returned %d and Read returned %d; the phases are not separable",
			len(listed), len(files))
	}
}

// Read must fetch exactly what it was given, including files a Select would normally reject.
// Anything else means Read is applying a filter of its own, which is the conflation again.
func TestReadFetchesExactlyWhatItIsGiven(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"db/migrate/0001.up.sql": "SELECT 1;",
		"db/migrate/README.md":   "# notes",
	})

	files, err := resolveFS(context.Background(), nil, []string{root},
		func(listed []provider.Path) []provider.Path {
			out := make([]provider.Path, 0, len(listed))
			for _, p := range listed {
				if strings.HasSuffix(p.Path, ".md") {
					out = append(out, p)
				}
			}
			return out
		})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(files) != 1 || files[0].Path != "db/migrate/README.md" {
		t.Fatalf("Read returned %v, want only the README it was asked for", paths(files))
	}
	if string(files[0].Current) != "# notes" {
		t.Errorf("Read returned %q, want the file's content", files[0].Current)
	}
}
