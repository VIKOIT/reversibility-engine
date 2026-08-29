// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// RootPrefix is what makes candidate detection independent of how the analysis root was named.
// The CLI property test holds the invariant end to end; these hold the branches that invariant
// cannot reach from a command line, and the one trade-off the design makes on purpose.

// repoWith builds a checkout containing the given directories and returns its root.
func repoWith(t *testing.T, dirs ...string) string {
	t.Helper()

	root := t.TempDir()

	// A marker file rather than a real repository: RootPrefix looks for the marker, and shelling
	// out to git would make this test depend on git being installed to assert something that has
	// nothing to do with git.
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	return root
}

func TestRootPrefixIsTheRepositoryRelativePath(t *testing.T) {
	t.Parallel()

	repo := repoWith(t, "django/contrib/auth/migrations", "db/migrate")

	for _, tc := range []struct {
		name string
		root string
		want string
	}{
		{"a nested migrations directory", "django/contrib/auth/migrations", "django/contrib/auth/migrations"},
		{"the app above it", "django/contrib/auth", "django/contrib/auth"},
		{"the repository itself", ".", ""},
		{"a Rails migrate directory", "db/migrate", "db/migrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := provider.RootPrefix([]string{filepath.Join(repo, filepath.FromSlash(tc.root))})
			if got != tc.want {
				t.Errorf("RootPrefix(%q) = %q, want %q", tc.root, got, tc.want)
			}
		})
	}
}

// TestRootPrefixIsTheSameHoweverTheRootIsSpelled is the invariant itself, at this level.
//
// Absolute, relative, `./name`, `..`, and `.` from inside all name one directory, and a
// classifier that answers differently for each is the defect this function exists to remove.
func TestRootPrefixIsTheSameHoweverTheRootIsSpelled(t *testing.T) {
	// Not parallel: several of these spellings are relative to the working directory.

	repo := repoWith(t, "django/contrib/auth/migrations")
	target := filepath.Join(repo, "django", "contrib", "auth", "migrations")

	const want = "django/contrib/auth/migrations"

	for _, tc := range []struct {
		name    string
		workdir string
		root    string
	}{
		{"absolute", repo, target},
		{"relative from the repository root", repo, filepath.Join("django", "contrib", "auth", "migrations")},
		{"as ./migrations from its parent", filepath.Dir(target), "." + string(filepath.Separator) + "migrations"},
		{"as migrations from its parent", filepath.Dir(target), "migrations"},
		{"as dot from inside it", target, "."},
		{"as .. from a sibling", filepath.Join(repo, "django", "contrib", "auth"), filepath.Join("migrations", "..", "migrations")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirTo(t, tc.workdir)

			if got := provider.RootPrefix([]string{tc.root}); got != want {
				t.Errorf("RootPrefix(%q) from %s = %q, want %q", tc.root, tc.workdir, got, want)
			}
		})
	}
}

// TestRootPrefixStopsAtTheRepository is the trade-off, asserted rather than assumed.
//
// The alternative anchor is the filesystem root, which is equally consistent and was rejected:
// a checkout that happens to live under a directory named `migrations/` would have every .py
// file beneath it classified as a migration, and the engine would fail changesets over where
// somebody keeps their source. Anchoring at the repository is what keeps the ancestry out.
func TestRootPrefixStopsAtTheRepository(t *testing.T) {
	t.Parallel()

	// A parent directory named exactly like a migration directory, with the checkout inside it.
	outer := filepath.Join(t.TempDir(), "migrations")
	if err := os.MkdirAll(outer, 0o755); err != nil {
		t.Fatalf("creating %s: %v", outer, err)
	}

	repo := filepath.Join(outer, "project")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("creating the checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}

	got := provider.RootPrefix([]string{filepath.Join(repo, "src")})

	if got != "src" {
		t.Errorf("RootPrefix = %q, want %q", got, "src")
	}
	for _, segment := range strings.Split(got, "/") {
		if segment == "migrations" {
			t.Errorf("RootPrefix = %q; a directory above the checkout must not make its .py files migrations", got)
		}
	}
}

// TestRootPrefixOfAFileNamesItsDirectory covers the shape `revctl check migrations/0001.sql`.
//
// The provider keys a single-file root by its base name, so the directory is exactly what
// relativising dropped and exactly what has to come back.
func TestRootPrefixOfAFileNamesItsDirectory(t *testing.T) {
	t.Parallel()

	repo := repoWith(t, "db/migrate")

	file := filepath.Join(repo, "db", "migrate", "0001_add.sql")
	if err := os.WriteFile(file, []byte("CREATE TABLE t (id int);\n"), 0o644); err != nil {
		t.Fatalf("writing the migration: %v", err)
	}

	if got := provider.RootPrefix([]string{file}); got != "db/migrate" {
		t.Errorf("RootPrefix of a file = %q, want %q", got, "db/migrate")
	}
}

// TestRootPrefixOfSeveralRootsIsWhatTheyShare.
//
// A segment true of one root and not of another is not true of the changeset. Claiming it would
// classify half the files by a directory they are not in, which is the same error as losing the
// segment, pointed the other way.
func TestRootPrefixOfSeveralRootsIsWhatTheyShare(t *testing.T) {
	t.Parallel()

	repo := repoWith(t, "db/migrate/first", "db/migrate/second", "src/app")

	for _, tc := range []struct {
		name  string
		roots []string
		want  string
	}{
		{"two siblings under one migration directory",
			[]string{"db/migrate/first", "db/migrate/second"}, "db/migrate"},
		{"one root twice", []string{"db/migrate", "db/migrate"}, "db/migrate"},
		{"roots sharing nothing", []string{"db/migrate", "src/app"}, ""},
		{"a root and its own parent", []string{"db/migrate", "db"}, "db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			roots := make([]string, 0, len(tc.roots))
			for _, r := range tc.roots {
				roots = append(roots, filepath.Join(repo, filepath.FromSlash(r)))
			}

			if got := provider.RootPrefix(roots); got != tc.want {
				t.Errorf("RootPrefix(%v) = %q, want %q", tc.roots, got, tc.want)
			}
		})
	}
}

// TestRootPrefixOutsideARepositoryIsStillConsistent.
//
// Without a checkout there is no boundary to anchor at, so the absolute path is used. The value
// is not the assertion — it varies by machine — the consistency is, because consistency is the
// entire property being bought.
func TestRootPrefixOutsideARepositoryIsStillConsistent(t *testing.T) {
	// Not parallel: it changes the working directory.

	root := t.TempDir()
	target := filepath.Join(root, "app", "migrations")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("creating %s: %v", target, err)
	}

	absolute := provider.RootPrefix([]string{target})

	chdirTo(t, target)
	dotted := provider.RootPrefix([]string{"."})

	if absolute != dotted {
		t.Errorf("outside a repository the prefix still depends on the spelling: %q vs %q", absolute, dotted)
	}
	if !strings.HasSuffix(absolute, "app/migrations") {
		t.Errorf("prefix %q does not end at the directory that was named", absolute)
	}
}

// TestRootPrefixOfNothingIsNothing. No roots means no prefix, which is what every provider whose
// paths are already repository-relative passes.
func TestRootPrefixOfNothingIsNothing(t *testing.T) {
	t.Parallel()

	if got := provider.RootPrefix(nil); got != "" {
		t.Errorf("RootPrefix(nil) = %q, want the empty prefix", got)
	}
}

// chdirTo moves into dir for the duration of the test.
func chdirTo(t *testing.T, dir string) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("entering %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("returning to %s: %v", previous, err)
		}
	})
}
