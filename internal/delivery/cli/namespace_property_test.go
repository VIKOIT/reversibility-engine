// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// This file holds the generalisation of the root-naming invariant, and it exists because fixing
// candidate detection alone left the cause in place.
//
//	**Path-keyed decisions use one namespace, and it is not the one the caller typed.**
//	A decision that depends on how the analysis root was named is a decision that changes
//	answer for the same files.
//
// Candidate detection was fixed first, and `ignore:` globs, waiver `path:` globs and
// `--terraform-plan` were still matching against the changeset's spelling. Two namespaces in one
// decision surface is what produced the P0; keeping one of them was keeping the cause.
//
// The property here is differential, like the root-naming one: the same files, named from
// different directions, must reach the same decision — and now that covers configuration and not
// only classification. See docs/SPECIFICATION.md §16.13.

// TestPolicyDecisionsDoNotDependOnHowTheRootWasNamed is the property.
//
// A project with a policy at its root, migrations below it, and every way of pointing the engine
// at those migrations. The ignore list either works from all of them or from none.
func TestPolicyDecisionsDoNotDependOnHowTheRootWasNamed(t *testing.T) {
	// Not parallel: the shapes differ by working directory.

	for _, shape := range []struct {
		name string
		// from returns the working directory and the path argument, given the project root.
		from func(project string) (workdir, arg string)
	}{
		{"the project, absolutely", func(p string) (string, string) { return p, p }},
		{"the project, as dot from inside", func(p string) (string, string) { return p, "." }},
		{"the app directory", func(p string) (string, string) { return p, "app" }},
		{"the app directory, as ./app", func(p string) (string, string) { return p, "./app" }},
		{"the migrations directory", func(p string) (string, string) { return p, "app/migrations" }},
		{"the migrations directory from the app", func(p string) (string, string) {
			return filepath.Join(p, "app"), "migrations"
		}},
		{"dot, from inside the migrations directory", func(p string) (string, string) {
			return filepath.Join(p, "app", "migrations"), "."
		}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			project := writeTree(t, map[string]string{
				"app/migrations/0001_drop.up.sql":   "DROP TABLE legacy_orders;\n",
				"app/migrations/0001_drop.down.sql": "CREATE TABLE legacy_orders (id bigint);\n",
			})

			// Written the way anybody writes one: relative to the project, which is where the
			// policy file sits. Before the fix this matched nothing whenever the run was rooted
			// below the project root, and nothing said so.
			writePolicy(t, project, "version: 1\nignore:\n  - \"app/migrations/**\"\n")

			workdir, arg := shape.from(project)
			chdir(t, workdir)

			certPath := filepath.Join(t.TempDir(), "certificate.json")
			_, stderr, _ := run("check", arg, "--min-grade", "A", "--format", "json", "--output", certPath)

			cert, ok := readCertificate(t, certPath)
			if !ok {
				t.Fatalf("no certificate was written\n%s", stderr)
			}

			// The ignore covered the only migration in the tree, from every direction.
			if len(cert.Findings) != 0 {
				t.Errorf("the ignore list did not apply: %d finding(s) survived it\n%s",
					len(cert.Findings), stderr)
			}
			if len(cert.IgnoredByPolicy) == 0 {
				t.Errorf("nothing was recorded as ignored, so the glob matched nothing\n%s", stderr)
			}

			// And it is never reported as dead config, because it was not.
			for _, w := range cert.PolicyWarnings {
				if strings.Contains(w, "ignore pattern") {
					t.Errorf("a pattern that did apply was reported as dead: %q", w)
				}
			}
		})
	}
}

// TestDeadIgnorePatternIsReported holds the other half.
//
// **A pattern that matches nothing is dead config, and dead config in a safety tool reads as
// protection the user does not have.** The same requirement as naming unanalyzed files: never let
// the reader infer.
func TestDeadIgnorePatternIsReported(t *testing.T) {
	t.Parallel()

	project := writeTree(t, map[string]string{
		"app/migrations/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"app/migrations/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
	})
	writePolicy(t, project, "version: 1\nignore:\n  - \"nowhere/**\"\n  - \"app/migrations/**\"\n")

	certPath := filepath.Join(t.TempDir(), "certificate.json")
	_, stderr, _ := run("check", project, "--format", "json", "--output", certPath)

	cert, ok := readCertificate(t, certPath)
	if !ok {
		t.Fatalf("no certificate was written\n%s", stderr)
	}

	// The dead one is named, on the certificate and on stderr.
	if !containsSubstring(cert.PolicyWarnings, "nowhere/**") {
		t.Errorf("the dead pattern is not on the certificate: %v", cert.PolicyWarnings)
	}
	if !strings.Contains(stderr, "nowhere/**") {
		t.Errorf("the dead pattern was not reported on stderr:\n%s", stderr)
	}

	// The live one is not, because reporting a working pattern as dead would teach the reader
	// to ignore the whole category.
	if containsSubstring(cert.PolicyWarnings, "app/migrations/**") {
		t.Errorf("a pattern that matched files was reported as dead: %v", cert.PolicyWarnings)
	}

	// It is a warning and nothing more. Dead config never moves a grade.
	if len(cert.Blockers) != 0 {
		t.Errorf("dead config produced blockers: %v", cert.Blockers)
	}
}

// TestDeadWaiverIsReported is the same for waivers, where the stakes are higher: a waiver that
// matches nothing is indistinguishable from a waiver that has not been needed yet, and the
// operator believes a risk has been accepted either way.
func TestDeadWaiverIsReported(t *testing.T) {
	t.Parallel()

	project := writeTree(t, map[string]string{
		"app/migrations/0001_drop.up.sql":   "DROP TABLE legacy_orders;\n",
		"app/migrations/0001_drop.down.sql": "CREATE TABLE legacy_orders (id bigint);\n",
	})
	writePolicy(t, project, fmt.Sprintf(`version: 1
waivers:
  - rule: PG001
    path: "somewhere/else/**"
    reason: "not this changeset"
    expires: %s
`, soon(t)))

	certPath := filepath.Join(t.TempDir(), "certificate.json")
	_, stderr, _ := run("check", project, "--format", "json", "--output", certPath)

	cert, ok := readCertificate(t, certPath)
	if !ok {
		t.Fatalf("no certificate was written\n%s", stderr)
	}

	if !containsSubstring(cert.PolicyWarnings, "PG001 at somewhere/else/**") {
		t.Errorf("the dead waiver is not on the certificate: %v", cert.PolicyWarnings)
	}
	if !strings.Contains(stderr, "covered no finding") {
		t.Errorf("the dead waiver was not reported on stderr:\n%s", stderr)
	}
}

// TestTerraformPlanFlagClaimsExactlyTheFileItNames pins the fourth instance.
//
// `--terraform-plan` used to be compared to the changeset path by bidirectional suffix match,
// because the two spellings were in different namespaces. It over-claimed: naming `a/plan.json`
// also claimed `b/a/plan.json`, handing the Terraform analyzer a file nobody named — and a file
// it claims and cannot read is UNKNOWN, which grades F.
//
// Both sides are now resolved into one namespace, so the comparison is exact.
func TestTerraformPlanFlagClaimsExactlyTheFileItNames(t *testing.T) {
	// Not parallel: it runs from inside the project so the flag is spelled the way a user
	// spells it.

	plan := `{"format_version":"1.0","resource_changes":[]}`

	project := writeTree(t, map[string]string{
		"a/plan.json":   plan,
		"b/a/plan.json": "this is not a plan and would grade F if claimed\n",
	})
	// A marker, so the project has a root to be relative to — the same thing a checkout gives.
	writePolicy(t, project, "version: 1\n")

	chdir(t, project)

	certPath := filepath.Join(t.TempDir(), "certificate.json")
	_, stderr, _ := run("check", ".", "--terraform-plan", "a/plan.json",
		"--format", "json", "--output", certPath)

	cert, ok := readCertificate(t, certPath)
	if !ok {
		t.Fatalf("no certificate was written\n%s", stderr)
	}

	// The decoy must not have been claimed. If it were, it would parse as garbage and the
	// analyzer would report UNKNOWN against it.
	for _, f := range cert.Findings {
		if strings.Contains(f.File, "b/a/plan.json") {
			t.Errorf("--terraform-plan a/plan.json also claimed b/a/plan.json: %+v", f)
		}
	}
	for _, u := range cert.UnanalyzedFiles {
		if u.Path == "a/plan.json" {
			t.Errorf("the plan that was named went unanalyzed: %+v", u)
		}
	}
}

// TestTerraformPlanFlagIsFoundHoweverItIsSpelled is the other direction: exactness must not have
// cost the flag its job. Absolute, relative and dotted spellings all name one file.
func TestTerraformPlanFlagIsFoundHoweverItIsSpelled(t *testing.T) {
	// Not parallel: the spellings are relative to the working directory.

	plan := `{"format_version":"1.0","resource_changes":[]}`

	for _, spelling := range []string{"plans/main.json", "./plans/main.json", ""} {
		name := spelling
		if name == "" {
			name = "absolute"
		}

		t.Run(name, func(t *testing.T) {
			project := writeTree(t, map[string]string{"plans/main.json": plan})
			writePolicy(t, project, "version: 1\n")
			chdir(t, project)

			arg := spelling
			if arg == "" {
				arg = filepath.Join(project, "plans", "main.json")
			}

			certPath := filepath.Join(t.TempDir(), "certificate.json")
			_, stderr, _ := run("check", ".", "--terraform-plan", arg,
				"--format", "json", "--output", certPath)

			cert, ok := readCertificate(t, certPath)
			if !ok {
				t.Fatalf("no certificate was written\n%s", stderr)
			}

			// Claimed means analyzed. An empty plan has no findings, so the evidence that it was
			// claimed is that it is not sitting in the unanalyzed list.
			for _, u := range cert.UnanalyzedFiles {
				if strings.HasSuffix(u.Path, "main.json") {
					t.Errorf("--terraform-plan %s did not claim the file it named: %+v", arg, u)
				}
			}
		})
	}
}

func containsSubstring(in []string, want string) bool {
	for _, s := range in {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
