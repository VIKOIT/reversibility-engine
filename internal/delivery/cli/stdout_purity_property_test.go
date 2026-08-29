// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/delivery/cli"
)

// This file holds one invariant:
//
//	**All diagnostics, deprecations and warnings go to stderr. stdout carries the certificate
//	and nothing else, in every format.**
//
// It exists because `revctl check --format json --require-full-coverage` wrote a deprecation
// notice to stdout ahead of the certificate, and the output stopped being parseable JSON. The
// flag was kept accepted so that an upgrade would not turn a pipeline into an unknown-flag
// error; keeping it turned the pipeline into a parse error instead — the same failure wearing a
// different coat, and a worse one, because an unknown flag names itself and a JSON syntax error
// does not.
//
// The notice came from cobra, not from this package. pflag writes a deprecation warning into
// cobra's flagErrorBuf during ParseFlags, and cobra flushes that buffer through `c.Print`, which
// is `OutOrStderr()` — stdout, because Execute sets the out writer to stdout. So a single call
// to MarkDeprecated is enough to corrupt every JSON certificate the command emits, from a line
// that reads like documentation.
//
// Two tests, because there are two ways to lose this: emitting something new on stdout, and
// re-introducing the mechanism that emits it. See docs/SPECIFICATION.md §16.11.

// stdoutFlagCombination is one command line whose stdout must be a certificate and nothing else.
type stdoutFlagCombination struct {
	name string
	args []string
}

// stdoutCombinations enumerates the flag space of `check` under --format json.
//
// Every flag `check` accepts appears at least once, including the ones that do nothing, because
// a flag that does nothing is exactly the kind that acquires a warning: it is the only kind
// anybody is ever tempted to deprecate.
func stdoutCombinations(t *testing.T) []stdoutFlagCombination {
	t.Helper()

	tree := writeTree(t, map[string]string{
		"migrations/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"migrations/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
		"migrations/0002_drop.up.sql":  "DROP TABLE legacy_orders;\n",
		"migrations/notes.py":          "print(1)\n",
	})
	empty := t.TempDir()

	// Each entry is one flag or flag pair. The space is the cross product of these with the
	// gatings, which is where the interesting interactions live: a gate that fails still writes
	// a certificate, and that certificate still has to parse.
	singles := [][]string{
		{},
		{"--require-full-coverage"},
		{"--gate"},
		{"--min-grade", "C"},
		{"--min-grade", "F"},
		{"--no-config"},
		{"--before", empty},
		{"--context", "no-such-snapshot.json"},
		{"--terraform-plan", "no-such-plan.json"},
		{"--require-full-coverage", "--gate"},
		{"--require-full-coverage", "--gate", "--no-config"},
		{"--require-full-coverage", "--min-grade", "A", "--no-config", "--before", empty},
	}

	var out []stdoutFlagCombination
	for _, args := range singles {
		for _, target := range []struct {
			name string
			path string
		}{
			{"migrations", tree + "/migrations"},
			{"the tree", tree},
			{"an empty directory", empty},
		} {
			line := append([]string{"check", "--format", "json"}, args...)
			line = append(line, target.path)

			name := target.name + " with " + strings.Join(args, " ")
			if len(args) == 0 {
				name = target.name + " with no flags"
			}
			out = append(out, stdoutFlagCombination{name: name, args: line})
		}
	}
	return out
}

// TestStdoutIsAlwaysParseableJSON is the property, over the whole flag space.
func TestStdoutIsAlwaysParseableJSON(t *testing.T) {
	t.Parallel()

	for _, c := range stdoutCombinations(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr, code := run(c.args...)

			// A run that did not complete may legitimately write no certificate at all. What it
			// may never do is write something that is not one.
			if strings.TrimSpace(stdout) == "" {
				if code != cli.ExitError {
					t.Errorf("stdout is empty and the run exited %d; absence of output is never success\n%s",
						code, stderr)
				}
				return
			}

			var cert map[string]any
			if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
				t.Fatalf("stdout does not parse as JSON: %v\n--- stdout ---\n%s\n--- stderr ---\n%s",
					err, stdout, stderr)
			}

			// Parsing is necessary and not sufficient. A diagnostic could in principle be valid
			// JSON, so the object also has to be the certificate: the field every consumer
			// pins against.
			if _, ok := cert["schemaVersion"]; !ok {
				t.Errorf("stdout parses but is not a certificate — no schemaVersion:\n%s", stdout)
			}
		})
	}
}

// TestDeprecationNoticesGoToStderr pins the specific case the audit reported, so that a failure
// says what went wrong rather than only that some JSON did not parse.
func TestDeprecationNoticesGoToStderr(t *testing.T) {
	t.Parallel()

	tree := writeTree(t, map[string]string{
		"0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
	})

	stdout, stderr, _ := run("check", "--format", "json", "--require-full-coverage", "--no-config", tree)

	// The notice is still given. Dropping it would be the other way to pass this test and the
	// wrong one: a pipeline carrying a dead flag has to hear about it, just not on the stream
	// carrying the answer.
	if !strings.Contains(stderr, "require-full-coverage") {
		t.Errorf("the deprecation notice was not written to stderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "deprecated") || strings.Contains(stdout, "require-full-coverage") {
		t.Errorf("the deprecation notice reached stdout:\n%s", stdout)
	}

	var cert map[string]any
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("stdout does not parse as JSON: %v\n%s", err, stdout)
	}
}

// TestNoFlagIsDeprecatedThroughCobra closes the mechanism rather than the instance.
//
// pflag prints a flag's Deprecated message through cobra's out writer, which is the stream the
// certificate goes to. There is no way to redirect one without redirecting the other, because
// cobra reaches for the same writer for both — so the only reliable answer is not to use the
// mechanism. A deprecation notice is a diagnostic, this package already writes diagnostics to
// stderr, and warnAboutDeprecatedFlags is where they go.
//
// It is a source scan rather than a walk of the command tree because the alternative is
// importing pflag directly to name the type its VisitAll callback takes, and §6's list of
// allowed dependencies is complete: pflag is cobra's, not this module's, and promoting it to a
// direct dependency to write one assertion is not a trade worth making.
//
// **Its limitation, stated so nobody reads it as stronger than it is: a source scan checks the
// source of this repository.** A deprecation arriving through a dependency's own mechanism —
// cobra marking one of its built-in flags deprecated, a future cobra printing some other warning
// through the same writer — passes this test untouched, because the call is not in these files.
// That gap is covered only by TestStdoutIsAlwaysParseableJSON, which watches the stream rather
// than the source and does not care where a stray byte came from. The two are not redundant:
// this one names the cause and can be acted on, that one is the one that actually holds.
//
// The property above would catch the symptom anyway. This catches the cause, and it says what to
// do about it, which the symptom cannot.
func TestNoFlagIsDeprecatedThroughCobra(t *testing.T) {
	t.Parallel()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing this package's sources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("no sources found; this test would assert nothing")
	}

	var offenders []string
	for _, name := range sources {
		// Test sources are skipped, this one included: it necessarily contains the strings it
		// is looking for. What is being guarded is the command tree, which is built by the
		// package's own code.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}

		for _, line := range strings.Split(string(body), "\n") {
			// The mechanism, both spellings. A comment explaining why it is not used is not an
			// offence, so a line has to actually call it.
			if strings.Contains(line, ".MarkDeprecated(") || strings.Contains(line, ".MarkShorthandDeprecated(") {
				offenders = append(offenders, fmt.Sprintf("%s: %s", name, strings.TrimSpace(line)))
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf(
			"pflag's deprecation mechanism is used here:\n  %s\n\n"+
				"pflag writes that warning into cobra's flagErrorBuf during ParseFlags, and cobra "+
				"flushes it through Print, which is the out writer — stdout. It lands ahead of the "+
				"certificate and stdout stops being parseable JSON. Register the flag normally, hide "+
				"it with MarkHidden, and add it to deprecatedFlags so the notice goes to stderr.",
			strings.Join(offenders, "\n  "))
	}
}
