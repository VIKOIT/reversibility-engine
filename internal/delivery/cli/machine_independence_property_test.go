// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/render"
)

// This file holds one requirement, and it belongs beside the no-timestamp rule rather than in
// anybody's memory:
//
//	**No rendered field, in any format, may contain an absolute filesystem path, a hostname, or
//	a username.**
//
// It is the same class as "no timestamps, no UUIDs, no hostnames": identical input must produce a
// byte-identical certificate, and a value that varies with the machine breaks that as surely as a
// clock does. It is listed separately because the determinism tests compare two runs *on one
// machine*, where an absolute path is perfectly stable and perfectly wrong.
//
// It exists because it nearly shipped. Qualifying paths for classification (§16.10) also made
// them available for rendering, and the first version of the UNSUPPORTED_CONTENT message used
// the qualified directory — which outside a checkout is absolute. Two runs of the same tree
// unpacked in two places would have produced different certificates, and every one of them would
// have carried the analyst's home directory into a pull request comment.
//
// The rule that came out of it: **the qualified namespace classifies; it never renders.** This
// is what enforces it, over every format, rather than over the one message that happened to be
// caught. See docs/SPECIFICATION.md §16.14.

// machineSpecific matches the shapes a path, host or user leaks in.
//
// Deliberately broad. A false positive here is a test that has to be looked at; a false negative
// is somebody's home directory in a certificate posted to a public pull request.
func machineSpecificPatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()

	patterns := []*regexp.Regexp{
		// A Windows drive-letter path, in either slash style. The letter must stand alone: without
		// the leading boundary this matches the "s:/" inside "https://", and a SARIF document is
		// full of schema URLs.
		regexp.MustCompile(`(^|[^A-Za-z])[A-Za-z]:[\\/]`),
		// A POSIX absolute path with at least one segment. Anchored on the common roots rather
		// than on a bare leading slash, because certificate prose legitimately contains "/" in
		// SQL, in globs, and in relative paths.
		regexp.MustCompile(`(^|[\s"'(\[])/(home|Users|root|tmp|var|private|mnt|srv)/`),
		// A UNC share.
		regexp.MustCompile(`\\\\[A-Za-z0-9_.-]+\\`),
	}

	// The current user and host, by name. These catch a leak that no shape-based pattern would:
	// a bare username embedded in prose.
	if u, err := user.Current(); err == nil {
		if name := strings.TrimSpace(filepath.Base(u.Username)); len(name) > 2 {
			patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(name)+`\b`))
		}
	}
	if host, err := os.Hostname(); err == nil {
		if len(host) > 2 {
			patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(host)+`\b`))
		}
	}

	return patterns
}

// TestNoRenderedOutputCarriesAMachineSpecificValue is the property, over every format and every
// shape of run that produces a certificate.
func TestNoRenderedOutputCarriesAMachineSpecificValue(t *testing.T) {
	// Not parallel: several shapes run from inside the tree, which is the case most likely to
	// tempt an absolute path into the output.

	patterns := machineSpecificPatterns(t)

	for _, scenario := range machineIndependenceScenarios() {
		for _, format := range render.Formats() {
			t.Run(scenario.name+"/"+format, func(t *testing.T) {
				project, workdir, args := scenario.build(t)
				chdir(t, workdir)

				out, stderr, _ := run(append([]string{"check", "--format", format}, args...)...)
				if strings.TrimSpace(out) == "" {
					t.Fatalf("no certificate was rendered\n%s", stderr)
				}

				for _, p := range patterns {
					if m := p.FindString(out); m != "" {
						t.Errorf(
							"the %s certificate contains %q, which is machine-specific.\n"+
								"Identical input must render byte-identically on any machine, and this "+
								"would also publish a local path into a pull request comment.\n"+
								"The qualified namespace classifies; it never renders.\n"+
								"project was %s\n--- output ---\n%s",
							format, m, project, out)
						break
					}
				}
			})
		}
	}
}

type machineIndependenceScenario struct {
	name string
	// build returns the project root (for the failure message), the working directory to run
	// from, and the arguments after --format.
	build func(t *testing.T) (project, workdir string, args []string)
}

// machineIndependenceScenarios covers the runs whose messages name a location.
//
// Every outcome that produces prose about *where* something is: the unsupported-content message
// names a directory, the coverage message names files, and a finding names its file. Each is
// exercised from inside the tree and from outside it, because the tempting absolute path differs
// between the two.
func machineIndependenceScenarios() []machineIndependenceScenario {
	django := map[string]string{
		"app/migrations/0001_initial.py": "from django.db import migrations\n",
		"app/migrations/0002_alter.py":   "from django.db import migrations\n",
	}
	mixed := map[string]string{
		"app/migrations/0001_drop.up.sql": "DROP TABLE legacy_orders;\n",
		"app/migrations/0002_backfill.rb": "class Backfill < ActiveRecord::Migration\nend\n",
	}
	destructive := map[string]string{
		"app/migrations/0001_drop.up.sql":   "DROP TABLE legacy_orders;\n",
		"app/migrations/0001_drop.down.sql": "CREATE TABLE legacy_orders (id bigint);\n",
	}

	return []machineIndependenceScenario{
		{"unsupported content, named absolutely", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, django)
			return p, t.TempDir(), []string{p}
		}},
		{"unsupported content, rooted at the migrations directory", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, django)
			return p, filepath.Join(p, "app", "migrations"), []string{"."}
		}},
		{"partial coverage, named absolutely", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, mixed)
			return p, t.TempDir(), []string{p}
		}},
		{"partial coverage, rooted at the migrations directory", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, mixed)
			return p, filepath.Join(p, "app", "migrations"), []string{"."}
		}},
		{"findings, rooted at the migrations directory", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, destructive)
			return p, filepath.Join(p, "app", "migrations"), []string{"."}
		}},
		{"dead config, rooted at the migrations directory", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, destructive)
			writePolicy(t, p, "version: 1\nignore:\n  - \"nowhere/**\"\n")
			return p, filepath.Join(p, "app", "migrations"), []string{"."}
		}},
		{"a single file named absolutely", func(t *testing.T) (string, string, []string) {
			p := writeTree(t, destructive)
			return p, t.TempDir(), []string{filepath.Join(p, "app", "migrations", "0001_drop.up.sql")}
		}},
	}
}

// TestTheSameTreeInTwoPlacesRendersIdentically is the property stated as the thing it protects.
//
// The pattern test above catches the shapes a leak takes. This catches a leak of any shape at
// all, by the only definition that finally matters: unpack one tree twice, in two directories,
// and the certificates must not differ by one byte.
func TestTheSameTreeInTwoPlacesRendersIdentically(t *testing.T) {
	// Not parallel: it runs from inside each copy.

	content := map[string]string{
		"app/migrations/0001_drop.up.sql":  "DROP TABLE legacy_orders;\n",
		"app/migrations/0002_initial.py":   "from django.db import migrations\n",
		"app/migrations/README.md":         "# how to write migrations\n",
		"app/migrations/0003_add.up.sql":   "ALTER TABLE orders ADD COLUMN notes text;\n",
		"app/migrations/0003_add.down.sql": "ALTER TABLE orders DROP COLUMN notes;\n",
	}

	for _, format := range render.Formats() {
		t.Run(format, func(t *testing.T) {
			render := func() string {
				t.Helper()

				project := writeTree(t, content)
				writePolicy(t, project, "version: 1\nignore:\n  - \"nowhere/**\"\n")
				chdir(t, filepath.Join(project, "app", "migrations"))

				out, stderr, _ := run("check", ".", "--format", format)
				if strings.TrimSpace(out) == "" {
					t.Fatalf("no certificate was rendered\n%s", stderr)
				}
				return out
			}

			first, second := render(), render()

			if first != second {
				t.Errorf(
					"the same tree unpacked in two directories rendered differently in %s.\n"+
						"Something in the output depends on where the tree lives, which breaks the "+
						"byte-identical requirement and would publish a local path.\n--- first ---\n%s\n--- second ---\n%s",
					format, first, second)
			}
		})
	}
}

// TestGoldenCertificatesCarryNoMachineSpecificValue extends the rule to the committed fixtures.
//
// A golden file is regenerated by whoever last touched the renderers, on their machine. If a
// machine-specific value ever reaches one, it is committed and then asserted as correct forever.
func TestGoldenCertificatesCarryNoMachineSpecificValue(t *testing.T) {
	t.Parallel()

	patterns := machineSpecificPatterns(t)

	golden, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "fixtures", "golden", "*"))
	if err != nil {
		t.Fatalf("listing the golden files: %v", err)
	}
	if len(golden) == 0 {
		t.Fatal("no golden files found; this test would assert nothing")
	}

	for _, name := range golden {
		t.Run(filepath.Base(name), func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			for _, p := range patterns {
				if m := p.FindString(string(body)); m != "" {
					t.Errorf("the golden file contains %q, which is machine-specific (GOOS %s)",
						m, runtime.GOOS)
				}
			}
		})
	}
}
