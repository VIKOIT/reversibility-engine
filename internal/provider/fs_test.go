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

func sqlOnly(path string) bool { return strings.HasSuffix(path, ".sql") }

func TestFSTreatsASingleTreeAsAllAdded(t *testing.T) {
	t.Parallel()

	root := writeFiles(t, map[string]string{
		"0001.up.sql":        "SELECT 1;",
		"0001.down.sql":      "SELECT 2;",
		"nested/0002.up.sql": "SELECT 3;",
	})

	got, err := provider.NewFS(nil, []string{root}, sqlOnly).ChangedFiles(context.Background(), "")
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

	got, err := provider.NewFS(nil, []string{root}, sqlOnly).ChangedFiles(context.Background(), "")
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

	got, err := provider.NewFS(nil, []string{root}, sqlOnly).ChangedFiles(context.Background(), "")
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

	got, err := provider.NewFS([]string{before}, []string{after}, sqlOnly).ChangedFiles(context.Background(), "")
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

	got, err := provider.NewFS(nil, []string{filepath.Join(root, "0001.up.sql")}, sqlOnly).
		ChangedFiles(context.Background(), "")
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

	got, err := provider.NewFS(nil, []string{root}, sqlOnly).ChangedFiles(context.Background(), "")
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
		if _, err := provider.NewFS(nil, []string{missing}, nil).ChangedFiles(context.Background(), ""); err == nil {
			t.Error("expected an error for a missing path")
		}
	})

	t.Run("no paths at all", func(t *testing.T) {
		t.Parallel()

		if _, err := provider.NewFS(nil, nil, nil).ChangedFiles(context.Background(), ""); err == nil {
			t.Error("expected an error when no paths are given")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		root := writeFiles(t, map[string]string{"a.sql": "SELECT 1;"})
		if _, err := provider.NewFS(nil, []string{root}, nil).ChangedFiles(ctx, ""); err == nil {
			t.Error("expected an error for a cancelled context")
		}
	})
}
