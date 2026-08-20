// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/delivery/cli"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

// run executes the command tree in-process and returns its streams and exit code.
//
// Nothing here touches the real process streams or calls os.Exit, which is why the CLI returns
// an exit code instead of taking one.
func run(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer

	code = cli.Execute(cli.Options{Stdout: &out, Stderr: &errOut, Args: args})
	return out.String(), errOut.String(), code
}

// writeTree materializes files under a temporary directory and returns its path.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	return root
}

// A reversible migration with a valid down file.
func safeMigrations(t *testing.T) string {
	return writeTree(t, map[string]string{
		"0001_index.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"0001_index.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
	})
}

func destructiveMigrations(t *testing.T) string {
	return writeTree(t, map[string]string{
		"0001_drop.up.sql":   "DROP TABLE legacy_orders;\n",
		"0001_drop.down.sql": "CREATE TABLE legacy_orders (id bigint);\n",
	})
}

func TestVersion(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("version")

	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(stdout, certificate.SchemaVersion) {
		t.Errorf("version output does not carry the schema version: %q", stdout)
	}
}

func TestCheckDefaultsToMarkdown(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("check", safeMigrations(t))

	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(stdout, "Reversibility Certificate") {
		t.Errorf("output is not the markdown certificate:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Grade A") {
		t.Errorf("expected grade A:\n%s", stdout)
	}
}

func TestCheckJSONDecodesIntoThePublicSchema(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("check", "--format", "json", safeMigrations(t))
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("output does not decode into the public schema: %v\n%s", err, stdout)
	}

	if cert.Grade != certificate.GradeA {
		t.Errorf("Grade = %q, want A", cert.Grade)
	}
	if !cert.Passed() {
		t.Error("a grade A certificate did not pass the gate")
	}
	if len(cert.InputDigest) != 64 {
		t.Errorf("InputDigest = %q", cert.InputDigest)
	}
}

func TestCheckSARIF(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("check", "--format", "sarif", destructiveMigrations(t))
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}

	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Level  string `json:"level"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &log); err != nil {
		t.Fatalf("output is not valid SARIF JSON: %v", err)
	}

	if log.Version != "2.1.0" {
		t.Errorf("SARIF version = %q, want 2.1.0", log.Version)
	}
	if len(log.Runs) != 1 || len(log.Runs[0].Results) == 0 {
		t.Fatalf("expected one run with results")
	}
	if log.Runs[0].Results[0].Level != "error" {
		t.Errorf("a DROP TABLE was reported at level %q, want error", log.Runs[0].Results[0].Level)
	}
}

// THE GATE CONTRACT. A pipeline distinguishes "the change is unsafe" from "the tool broke" by
// exit code, and conflating them is how a broken tool ends up ignored.
func TestGateExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no gate requested, unsafe change still exits zero", []string{"check", destructiveMigrations(t)}, cli.ExitOK},
		{"gate on a safe change", []string{"check", "--gate", safeMigrations(t)}, cli.ExitOK},
		{"gate on an unsafe change", []string{"check", "--gate", destructiveMigrations(t)}, cli.ExitGateFailed},
		{"min-grade A on an unsafe change", []string{"check", "--min-grade", "A", destructiveMigrations(t)}, cli.ExitGateFailed},
		{"min-grade F accepts anything", []string{"check", "--min-grade", "F", destructiveMigrations(t)}, cli.ExitOK},
		{"min-grade is case insensitive", []string{"check", "--min-grade", "a", safeMigrations(t)}, cli.ExitOK},

		{"invalid format", []string{"check", "--format", "xml", safeMigrations(t)}, cli.ExitError},
		{"invalid min-grade", []string{"check", "--min-grade", "Z", safeMigrations(t)}, cli.ExitError},
		{"nonexistent path", []string{"check", filepath.Join(t.TempDir(), "nope")}, cli.ExitError},
		{"no path given", []string{"check"}, cli.ExitError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, stderr, code := run(tt.args...)
			if code != tt.want {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, tt.want, stderr)
			}
		})
	}
}

// A failed gate must say why, where the operator is already looking.
func TestGateFailureExplainsItself(t *testing.T) {
	t.Parallel()

	_, stderr, code := run("check", "--gate", destructiveMigrations(t))

	if code != cli.ExitGateFailed {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitGateFailed)
	}
	if !strings.Contains(stderr, "gate failed") {
		t.Errorf("stderr does not announce the gate failure: %q", stderr)
	}
	if !strings.Contains(stderr, "PG001") {
		t.Errorf("stderr does not name the blocking rule: %q", stderr)
	}
}

// Even when the gate fails, the certificate itself must still be produced — that is the artifact
// the user asked for, and a pipeline usually uploads it regardless of the outcome.
func TestCertificateIsWrittenEvenWhenTheGateFails(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("check", "--gate", "--format", "json", destructiveMigrations(t))

	if code != cli.ExitGateFailed {
		t.Fatalf("exit code = %d", code)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("no certificate on stdout despite a failed gate: %v", err)
	}
	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
	if len(cert.Blockers) == 0 {
		t.Error("grade F certificate carries no blockers")
	}
}

func TestOutputToFile(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "certificate.json")

	stdout, _, code := run("check", "--format", "json", "--output", target, safeMigrations(t))
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when --output is used, got %q", stdout)
	}

	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal(written, &cert); err != nil {
		t.Fatalf("file does not contain a certificate: %v", err)
	}
	if cert.Grade != certificate.GradeA {
		t.Errorf("Grade = %q, want A", cert.Grade)
	}
}

// The --before form is what lets the Kubernetes rules see what a change replaced.
func TestCheckComparesTwoTrees(t *testing.T) {
	t.Parallel()

	const manifest = `apiVersion: v1
kind: Namespace
metadata:
  name: legacy
`

	before := writeTree(t, map[string]string{"ns.yaml": manifest})
	after := writeTree(t, map[string]string{"other.yaml": "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: kept\n"})

	stdout, _, code := run("check", "--before", before, "--format", "json", after)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// Removing the namespace is irreversible; adding one is not.
	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F. Findings: %+v", cert.Grade, cert.Findings)
	}

	var sawRemoval bool
	for _, f := range cert.Findings {
		if f.RuleID == "K8S006" {
			sawRemoval = true
		}
	}
	if !sawRemoval {
		t.Errorf("the namespace removal was not detected: %+v", cert.Findings)
	}
}

// A changeset with nothing the engine understands must say so rather than quietly passing.
func TestCheckOnAnIrrelevantChangeset(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"README.md": "# hello\n", "main.go": "package main\n"})

	stdout, _, code := run("check", "--gate", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cert.Applicable {
		t.Error("Applicable = true for a changeset with no migrations or manifests")
	}
	if cert.Grade != certificate.GradeA {
		t.Errorf("Grade = %q, want A", cert.Grade)
	}
}

// Running the CLI twice over the same input must produce identical bytes.
func TestCLIOutputIsDeterministic(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{
		"0001_a.up.sql":   "CREATE TABLE a (id bigint);\nDROP TABLE b;\n",
		"0001_a.down.sql": "DROP TABLE a;\n",
		"k8s.yaml":        "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: n\n",
	})

	for _, format := range []string{"json", "markdown", "sarif"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			first, _, _ := run("check", "--format", format, root)
			for i := 0; i < 20; i++ {
				got, _, _ := run("check", "--format", format, root)
				if got != first {
					t.Fatalf("run %d differed", i)
				}
			}
		})
	}
}

func TestBareInvocationPrintsHelp(t *testing.T) {
	t.Parallel()

	stdout, _, code := run()

	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(stdout, "revctl") || !strings.Contains(stdout, "check") {
		t.Errorf("bare invocation did not print help:\n%s", stdout)
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	if _, _, code := run("frobnicate"); code != cli.ExitError {
		t.Errorf("exit code = %d, want %d", code, cli.ExitError)
	}
}
