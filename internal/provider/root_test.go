// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider_test

import (
	"os"
	"path"
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

			got := provider.ResolveRoot([]string{filepath.Join(repo, filepath.FromSlash(tc.root))}).Prefix
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

			if got := provider.ResolveRoot([]string{tc.root}).Prefix; got != want {
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

	got := provider.ResolveRoot([]string{filepath.Join(repo, "src")}).Prefix

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

	if got := provider.ResolveRoot([]string{file}).Prefix; got != "db/migrate" {
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

			if got := provider.ResolveRoot(roots).Prefix; got != tc.want {
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

	absolute := provider.ResolveRoot([]string{target}).Prefix

	chdirTo(t, target)
	dotted := provider.ResolveRoot([]string{"."}).Prefix

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

	if got := provider.ResolveRoot(nil).Prefix; got != "" {
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

// TestNearestProjectMarkerWins is the monorepo ruling.
//
// One `.git` at the top and a `.reversibility.yml` per package disagree about where the project
// starts, so the rule has to be stated rather than left to the order of a list. The nearest wins,
// because a package's policy is written about that package: its globs say `db/migrate/**`, and a
// run about that package has to resolve them against paths of that shape.
func TestNearestProjectMarkerWins(t *testing.T) {
	t.Parallel()

	// A monorepo: one checkout at the top, a policy per package.
	repo := repoWith(t, "packages/api/db/migrate", "packages/web/src")
	writeMarker(t, filepath.Join(repo, "packages", "api"), ".reversibility.yml", "version: 1\n")

	for _, tc := range []struct {
		name       string
		root       string
		wantPrefix string
		wantAnchor string
	}{
		{
			// The package's own policy is nearer than the repository's .git.
			name:       "inside a package that has its own policy",
			root:       "packages/api/db/migrate",
			wantPrefix: "db/migrate",
			wantAnchor: ".reversibility.yml",
		},
		{
			name:       "the package root itself",
			root:       "packages/api",
			wantPrefix: "",
			wantAnchor: ".reversibility.yml",
		},
		{
			// No policy in this package, so the walk continues to the checkout.
			name:       "inside a package with no policy of its own",
			root:       "packages/web/src",
			wantPrefix: "packages/web/src",
			wantAnchor: ".git",
		},
		{
			name:       "the repository itself",
			root:       ".",
			wantPrefix: "",
			wantAnchor: ".git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := provider.ResolveRoot([]string{filepath.Join(repo, filepath.FromSlash(tc.root))})

			if got.Prefix != tc.wantPrefix {
				t.Errorf("Prefix = %q, want %q", got.Prefix, tc.wantPrefix)
			}
			if got.Anchor != tc.wantAnchor {
				t.Errorf("Anchor = %q, want %q — the nearest marker walking up wins",
					got.Anchor, tc.wantAnchor)
			}
		})
	}
}

// TestAnchorIsAMarkerNameAndNeverADirectory.
//
// The anchor reaches the certificate, and a certificate may not carry a path from the machine
// that produced it. Reporting the directory would be the more informative answer and would also
// publish somebody's home directory into a pull request comment.
func TestAnchorIsAMarkerNameAndNeverADirectory(t *testing.T) {
	t.Parallel()

	repo := repoWith(t, "db/migrate")
	got := provider.ResolveRoot([]string{filepath.Join(repo, "db", "migrate")})

	if got.Anchor != ".git" {
		t.Fatalf("Anchor = %q, want %q", got.Anchor, ".git")
	}
	if strings.ContainsAny(got.Anchor, `/\`) {
		t.Errorf("Anchor %q contains a path separator; it must be the marker name alone", got.Anchor)
	}
	if !got.Anchored() {
		t.Error("Anchored() is false with an anchor set")
	}
}

// TestNoProjectMarkerReportsNoAnchor.
//
// With no marker the prefix is absolute, so the absence of an anchor is what tells a caller the
// prefix must not be rendered — and tells a user why a project-relative glob matched nothing.
func TestNoProjectMarkerReportsNoAnchor(t *testing.T) {
	t.Parallel()

	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, "db", "migrate"), 0o755); err != nil {
		t.Fatalf("creating the tree: %v", err)
	}

	got := provider.ResolveRoot([]string{filepath.Join(bare, "db", "migrate")})

	if got.Anchored() || got.Anchor != "" {
		t.Errorf("Anchor = %q over a tree with no marker, want none", got.Anchor)
	}
	if got.Prefix == "" {
		t.Error("Prefix is empty; with no project root the absolute path is the namespace")
	}
}

// TestRootsInDifferentProjectsReportNoAnchor.
//
// Naming one of two disagreeing anchors would be worse than naming none: a reader would debug a
// glob against a project that only half the changeset is in.
func TestRootsInDifferentProjectsReportNoAnchor(t *testing.T) {
	t.Parallel()

	repo := repoWith(t, "packages/api/db", "packages/web/src")
	writeMarker(t, filepath.Join(repo, "packages", "api"), ".reversibility.yml", "version: 1\n")

	got := provider.ResolveRoot([]string{
		filepath.Join(repo, "packages", "api", "db"),
		filepath.Join(repo, "packages", "web", "src"),
	})

	if got.Anchor != "" {
		t.Errorf("Anchor = %q for roots in two different projects, want none", got.Anchor)
	}
}

// writeMarker creates a project marker inside dir.
func writeMarker(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// TestTheTwoSidesOfTheComparisonAgree is the property the CI failure violated.
//
// `--terraform-plan` names a file; the engine knows the same file by its changeset path plus the
// analysis root's prefix. Those are the two sides of one exact comparison, and they are computed
// by two different functions — so the property that matters is not what either returns, it is
// that they return the same thing.
//
// They did not, on Linux only: ResolveRoot dropped the empty segment a leading `/` produces and
// rejoined without it, so `/tmp/x` became `tmp/x` while QualifyPath kept `/tmp/x`. An absolute
// Windows path has no empty leading segment, so every local run agreed and the suite was green.
// **A test that only holds on the platform the author uses is not holding anything**, which is
// why this one asserts agreement rather than a value: agreement is checkable everywhere.
func TestTheTwoSidesOfTheComparisonAgree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		project func(t *testing.T) string
	}{
		{"inside a checkout", func(t *testing.T) string { return repoWith(t, "infra", "db/migrate") }},
		{"outside any project", func(t *testing.T) string {
			root := t.TempDir()
			for _, dir := range []string{"infra", "db/migrate"} {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
					t.Fatalf("creating %s: %v", dir, err)
				}
			}
			return root
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			project := tc.project(t)

			for _, rel := range []string{"infra/plan.json", "db/migrate/0001.up.sql"} {
				file := filepath.Join(project, filepath.FromSlash(rel))
				if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
					t.Fatalf("writing %s: %v", rel, err)
				}
			}

			for _, shape := range []struct {
				name string
				root string
				// within is the file's path as the changeset reports it, relative to root.
				within string
			}{
				{"rooted at the project", project, "infra/plan.json"},
				{"rooted at a subdirectory", filepath.Join(project, "infra"), "plan.json"},
				{"rooted at a nested subdirectory", filepath.Join(project, "db", "migrate"), "0001.up.sql"},
			} {
				t.Run(shape.name, func(t *testing.T) {
					root := provider.ResolveRoot([]string{shape.root})

					// What the engine decides about: the root's prefix joined onto the changeset
					// path. This is exactly what domain.NewLocator does.
					fromChangeset := path.Join(root.Prefix, shape.within)

					// What the flag resolves to for the same file on disk.
					fromFlag := provider.QualifyPath(filepath.Join(shape.root, filepath.FromSlash(shape.within)))

					if fromChangeset != fromFlag {
						t.Errorf(
							"the two sides of the comparison disagree for the same file:\n"+
								"  from the changeset: %q\n"+
								"  from the flag:      %q\n"+
								"An exact comparison between them is what claims a --terraform-plan "+
								"file, so a difference here means the flag stops claiming what it names.",
							fromChangeset, fromFlag)
					}
				})
			}
		})
	}
}
