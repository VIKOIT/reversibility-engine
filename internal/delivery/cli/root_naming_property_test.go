// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

// This file holds one invariant, and it exists because the Django P0 was fixed once and stayed
// live in the invocation the README documents.
//
//	**Candidate detection must not depend on how the analysis root was named.**
//	`revctl check ./migrations` and `revctl check .` from its parent must reach the same
//	outcome for the same files.
//
// The engine reports a changeset's paths relative to whatever directory it was pointed at, so
// `revctl check django/contrib/auth/migrations` handed the classifier `0001_initial.py` — with
// the one segment that identifies a Django migration stripped off the front. RULES.md §3 keys on
// exactly that segment, so the run reached NO_CANDIDATES and exit 0, while
// `revctl check django/contrib/auth` reached UNSUPPORTED_CONTENT and exit 2 over the same
// thirteen files. **The permissive answer was the one the documented invocation reached**, and
// the audit hit it 247 times across 60 repositories.
//
// So this is a differential property rather than a list of expected outcomes, and it needs no
// oracle: the shapes are compared against each other. An engine that classified everything
// wrongly but consistently would pass here and fail the oracle properties in
// bypass_property_test.go, which is the division of labour between the two files.
//
// See docs/SPECIFICATION.md §16.10.

// namingShape is one way of pointing revctl at the same directory.
//
// Each returns the working directory to run from and the path argument to pass. Between them
// they cover every way the root's own name can be lost: stripped by relativisation, hidden
// behind `.`, or spelled absolutely.
type namingShape struct {
	name string

	// resolve receives the staged tree root and the changeset directory inside it, and returns
	// the directory to run from and the path argument to give revctl.
	resolve func(staged, changeset string) (workdir, arg string)
}

func namingShapes() []namingShape {
	return []namingShape{
		{"the changeset directory, absolutely", func(staged, changeset string) (string, string) {
			return staged, changeset
		}},
		{"the changeset directory, relative to its parent", func(_, changeset string) (string, string) {
			return filepath.Dir(changeset), filepath.Base(changeset)
		}},
		{"the changeset directory, as ./name from its parent", func(_, changeset string) (string, string) {
			return filepath.Dir(changeset), "." + string(filepath.Separator) + filepath.Base(changeset)
		}},
		{"dot, from inside the changeset directory", func(_, changeset string) (string, string) {
			return changeset, "."
		}},
		{"the parent, from inside it", func(staged, changeset string) (string, string) {
			return filepath.Dir(changeset), "."
		}},
		{"the staged root, from its own parent", func(staged, _ string) (string, string) {
			return filepath.Dir(staged), filepath.Base(staged)
		}},
	}
}

// verdict is the part of a run that must not vary with the root's name.
//
// It is deliberately not the whole certificate. Paths do vary, and they are *supposed* to: a
// file named `0001_initial.py` by a run rooted at the migrations directory is exactly what the
// reader should paste back into the same command. What must not vary is every field a gate,
// a merge bot, or a pipeline reads.
type verdict struct {
	exit     int
	outcome  string
	coverage string
	grade    string
	gate     string

	// unanalyzed is the *count* of files the run could not read, not their paths. The count is
	// a statement about the changeset; the paths are a statement about how it was addressed.
	unanalyzed int

	// findings is the count of findings, for the same reason.
	findings int
}

func (v verdict) String() string {
	return fmt.Sprintf("exit %d, outcome %s, coverage %s, grade %s, gate %s, %d unanalyzed, %d findings",
		v.exit, v.outcome, v.coverage, v.grade, v.gate, v.unanalyzed, v.findings)
}

func TestOutcomeDoesNotDependOnHowTheAnalysisRootWasNamed(t *testing.T) {
	// Not parallel: the shapes differ by working directory, which is process-wide state. That
	// is the same reason TestCheckResolvesAGitRange runs alone.

	for _, c := range namingCorpus(t) {
		t.Run(c.name, func(t *testing.T) {
			var (
				first  verdict
				firstS string
			)

			for i, shape := range namingShapes() {
				staged, changeset := c.stage(t)
				workdir, arg := shape.resolve(staged, changeset)

				got := runForVerdict(t, workdir, arg)

				if i == 0 {
					first, firstS = got, shape.name
					continue
				}

				if got != first {
					t.Errorf(
						"the verdict depends on how the root was named.\n"+
							"  %s\n    -> %s\n"+
							"  %s\n    -> %s\n"+
							"Candidate detection must not depend on how the analysis root was named.",
						firstS, first, shape.name, got)
				}
			}
		})
	}
}

// runForVerdict runs one check from workdir and reduces it to the fields a gate reads.
func runForVerdict(t *testing.T, workdir, arg string) verdict {
	t.Helper()

	chdir(t, workdir)

	certPath := filepath.Join(t.TempDir(), "certificate.json")

	// --gate, because the whole point is which runs a gate lets through, and --no-config so a
	// stray policy above the temporary directory cannot make two shapes differ for a reason
	// that has nothing to do with naming.
	_, stderr, code := run("check", arg, "--gate", "--no-config", "--format", "json", "--output", certPath)

	cert, ok := readCertificate(t, certPath)
	if !ok {
		t.Fatalf("no certificate was written for %q from %s (exit %d)\n%s", arg, workdir, code, stderr)
	}

	return verdict{
		exit:       code,
		outcome:    string(cert.Outcome),
		coverage:   string(cert.Coverage),
		grade:      string(cert.Grade),
		gate:       string(cert.AIGateStatus),
		unanalyzed: len(cert.UnanalyzedFiles),
		findings:   len(cert.Findings),
	}
}

// ---------------------------------------------------------------------------------------
// The corpus: every fixture, plus the shapes the audit reported
// ---------------------------------------------------------------------------------------

// namingCase is one changeset, and a way to stage a private copy of it.
type namingCase struct {
	name string

	// stage writes the changeset into a fresh temporary tree and returns the staged root and
	// the changeset directory inside it.
	//
	// A fresh copy per shape, rather than one copy pointed at six ways, is what lets the
	// "staged root" shapes exist at all: the staged root must contain the changeset and nothing
	// else, or the shapes would not be naming the same set of files and any difference between
	// them would prove nothing.
	stage func(t *testing.T) (staged, changeset string)
}

// namingCorpus is every fixture changeset in testdata, plus the trees the audit named.
//
// **Every fixture**, not a chosen few. The defect was in the classifier's view of a path, which
// every fixture exercises and none of them was written to test, so a sample would be a sample of
// the wrong thing.
func namingCorpus(t *testing.T) []namingCase {
	t.Helper()

	cases := []namingCase{
		{"django app migrations", stagedFrom(map[string]string{
			"0001_initial.py":  "from django.db import migrations\n",
			"0002_alter.py":    "from django.db import migrations\n",
			"__init__.py":      "",
			"0003_add_perm.py": "from django.db import migrations\n",
		}, "django/contrib/auth/migrations")},
		{"rails db migrate", stagedFrom(map[string]string{
			"20240101_create_orders.rb": "class CreateOrders < ActiveRecord::Migration\nend\n",
			"20240102_add_index.rb":     "class AddIndex < ActiveRecord::Migration\nend\n",
		}, "db/migrate")},
		{"alembic versions under a migrations directory", stagedFrom(map[string]string{
			"a1b2c3_add_orders.py": "def upgrade(): pass\n",
			"d4e5f6_add_index.py":  "def upgrade(): pass\n",
		}, "migrations/versions")},
		{"node migrations", stagedFrom(map[string]string{
			"001_create.js": "exports.up = () => {};\n",
			"002_alter.ts":  "export const up = () => {};\n",
		}, "migrations")},
		{"sql beside unreadable migrations", stagedFrom(map[string]string{
			"0001_add.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
			"0001_add.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
			"0002_backfill.rb":  "class Backfill < ActiveRecord::Migration\nend\n",
		}, "db/migrate")},
		{"migrations beside a readme", stagedFrom(map[string]string{
			"0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
			"0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
			"README.md":         "# how to write migrations\n",
		}, "db/migrate")},
		{"an unconventionally named directory", stagedFrom(map[string]string{
			"0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
			"0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
			"notes.txt":         "written by hand\n",
		}, "db/schema")},
		{"scripts that are not migrations", stagedFrom(map[string]string{
			"migrate.py": "print(1)\n",
			"seed.rb":    "puts 1\n",
			"build.js":   "module.exports = {};\n",
		}, "tools")},
	}

	return append(cases, fixtureCases(t)...)
}

// stagedFrom returns a stage function that writes files into <tmp>/<at>.
//
// The directory the changeset lands in is named, because the name is the variable under test:
// staging Django migrations anywhere but a directory called `migrations` would test nothing.
func stagedFrom(files map[string]string, at string) func(*testing.T) (string, string) {
	return func(t *testing.T) (string, string) {
		t.Helper()

		staged := t.TempDir()
		changeset := filepath.Join(staged, filepath.FromSlash(at))

		if err := os.MkdirAll(changeset, 0o755); err != nil {
			t.Fatalf("creating %s: %v", changeset, err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(changeset, name), []byte(content), 0o644); err != nil {
				t.Fatalf("writing %s: %v", name, err)
			}
		}
		return staged, changeset
	}
}

// fixtureCases turns every fixture in testdata into a naming case.
//
// A fixture directory holds an `expected.json` beside the changeset, and the changeset itself
// lives in a subdirectory whose name varies by group — `migrations/` for PostgreSQL, `new/` and
// `old/` for Kubernetes, `plan/` for Terraform. That subdirectory is what gets staged, alone, so
// the sibling `expected.json` cannot make the "staged root" shapes see a different file set.
func fixtureCases(t *testing.T) []namingCase {
	t.Helper()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating the fixture root: %v", err)
	}

	var cases []namingCase

	for _, group := range []string{"postgres", "kubernetes", "terraform", "context"} {
		entries, err := os.ReadDir(filepath.Join(root, group))
		if err != nil {
			t.Fatalf("reading the %s fixtures: %v", group, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			for _, changeset := range changesetDirs(t, filepath.Join(root, group, entry.Name())) {
				cases = append(cases, namingCase{
					name:  group + "/" + entry.Name() + "/" + filepath.Base(changeset),
					stage: stagedCopyOf(changeset),
				})
			}
		}
	}

	if len(cases) == 0 {
		// A corpus that silently emptied would turn this whole file into a test that cannot
		// fail, which is the failure mode every property in this package was written against.
		t.Fatal("no fixture changesets were found; this property would assert nothing")
	}
	return cases
}

// changesetDirs lists the subdirectories of a fixture that hold its changeset.
func changesetDirs(t *testing.T, fixtureDir string) []string {
	t.Helper()

	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("reading %s: %v", fixtureDir, err)
	}

	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, filepath.Join(fixtureDir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// stagedCopyOf copies a fixture's changeset directory into a fresh temporary tree, keeping its
// own name and burying it two levels down so that the parent shapes have somewhere to stand.
func stagedCopyOf(source string) func(*testing.T) (string, string) {
	return func(t *testing.T) (string, string) {
		t.Helper()

		staged := t.TempDir()
		changeset := filepath.Join(staged, "app", filepath.Base(source))

		copyTree(t, source, changeset)
		return staged, changeset
	}
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()

	err := filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}

		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer func() { _ = out.Close() }()

		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("staging %s: %v", source, err)
	}
}

// ---------------------------------------------------------------------------------------
// The Django case itself, named rather than left to the corpus
// ---------------------------------------------------------------------------------------

// TestTheDjangoCaseFailsFromEveryDirectionItCanBeNamed is the audit's reproduction, kept as a
// case of its own beside the property.
//
// The property above would catch a regression here, but it would report it as "two shapes
// disagree" — and the thing worth reading in a failure is which answer was reached, because
// making the shapes agree on **exit 0** would satisfy the property and reinstate the P0.
func TestTheDjangoCaseFailsFromEveryDirectionItCanBeNamed(t *testing.T) {
	stage := stagedFrom(map[string]string{
		"0001_initial.py": "from django.db import migrations\n",
		"0002_alter.py":   "from django.db import migrations\n",
		"__init__.py":     "",
	}, "django/contrib/auth/migrations")

	staged, changeset := stage(t)

	for _, shape := range []struct {
		name    string
		workdir string
		arg     string
	}{
		{"the migrations directory itself", staged, changeset},
		{"the migrations directory, relatively", filepath.Join(staged, "django", "contrib", "auth"), "migrations"},
		{"the migrations directory, as ./migrations", filepath.Join(staged, "django", "contrib", "auth"), "./migrations"},
		{"dot, from inside it", changeset, "."},
		{"the app directory", staged, filepath.Join(staged, "django", "contrib", "auth")},
		{"the repository", filepath.Dir(staged), filepath.Base(staged)},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got := runForVerdict(t, shape.workdir, shape.arg)

			if got.outcome != string(certificate.OutcomeUnsupportedContent) {
				t.Errorf("outcome = %q, want %q — thirteen unreadable Django migrations are not an empty changeset",
					got.outcome, certificate.OutcomeUnsupportedContent)
			}
			if got.grade == string(certificate.GradeA) {
				t.Error("grade A over migrations no analyzer read")
			}
			if got.gate == string(certificate.GatePass) {
				t.Error("gate PASS over migrations no analyzer read")
			}
			if got.exit != 2 {
				t.Errorf("exit = %d, want 2; a gate over content nobody could read did not complete", got.exit)
			}
		})
	}
}

// TestUnreadableMigrationsAreNamedWhicheverWayTheRootWas holds the half of the fix a human
// reads: whichever way the root was named, the certificate names every file it could not read
// and says why, or the refusal is one nobody can act on.
//
// The *path* of each file legitimately varies with the root — that is the whole point of
// reporting paths as the caller named them — so this asserts the file, not the path: the base
// name is there, and the reason names the format rather than blaming the changeset.
func TestUnreadableMigrationsAreNamedWhicheverWayTheRootWas(t *testing.T) {
	stage := stagedFrom(map[string]string{
		"0001_initial.py": "from django.db import migrations\n",
	}, "django/contrib/auth/migrations")

	staged, changeset := stage(t)

	for _, shape := range []struct {
		name    string
		workdir string
		arg     string
	}{
		{"the migrations directory itself", changeset, "."},
		{"the app above it", filepath.Join(staged, "django", "contrib"), "auth"},
		{"the repository", filepath.Dir(staged), filepath.Base(staged)},
	} {
		t.Run(shape.name, func(t *testing.T) {
			chdir(t, shape.workdir)

			certPath := filepath.Join(t.TempDir(), "certificate.json")
			_, stderr, _ := run("check", shape.arg, "--gate", "--no-config",
				"--format", "json", "--output", certPath)

			cert, ok := readCertificate(t, certPath)
			if !ok {
				t.Fatalf("no certificate was written\n%s", stderr)
			}

			if len(cert.UnanalyzedFiles) != 1 {
				t.Fatalf("UnanalyzedFiles = %+v, want the one .py migration", cert.UnanalyzedFiles)
			}

			got := cert.UnanalyzedFiles[0]
			if filepath.Base(got.Path) != "0001_initial.py" {
				t.Errorf("unanalyzed file = %q, want it to name 0001_initial.py", got.Path)
			}
			if !strings.Contains(got.Reason, ".py migrations") {
				t.Errorf("reason = %q, want it to name the format this engine cannot read", got.Reason)
			}

			// The message on stderr names the extension too, from every direction. A refusal
			// that says only "not assessed" is what let the Django case read as "nothing here".
			if !strings.Contains(stderr, ".py migrations") {
				t.Errorf("the refusal does not name what it could not read:\n%s", stderr)
			}
		})
	}
}
