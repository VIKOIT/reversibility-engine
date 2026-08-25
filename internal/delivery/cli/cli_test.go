// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// gitRepo builds a throwaway repository and returns its path, or skips when git is absent.
//
// The CLI resolves refs in the process working directory, because the user's shell already
// selected the repository. That is what makes this test the only one here that cannot run in
// parallel.
func gitRepo(t *testing.T, commits []map[string]string) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	git("init", "--quiet", ".")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	git("config", "user.name", "Reversibility Test")
	git("config", "user.email", "test@example.invalid")
	git("config", "commit.gpgsign", "false")

	for i, files := range commits {
		for name, content := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("creating %s: %v", filepath.Dir(path), err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("writing %s: %v", name, err)
			}
		}
		git("add", "--all")
		git("commit", "--quiet", "--message", fmt.Sprintf("commit %d", i))
	}

	return dir
}

// chdir moves into dir for the duration of the test.
func chdir(t *testing.T, dir string) {
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

func TestCheckResolvesAGitRange(t *testing.T) {
	repo := gitRepo(t, []map[string]string{
		{"migrations/0001_init.up.sql": "CREATE TABLE orders (id bigint);\n"},
		{
			"migrations/0002_drop.up.sql":   "DROP TABLE orders;\n",
			"migrations/0002_drop.down.sql": "CREATE TABLE orders (id bigint);\n",
		},
	})
	chdir(t, repo)

	// The working tree is left dirty. The certificate must describe the committed refs.
	if err := os.WriteFile(filepath.Join(repo, "migrations", "0002_drop.up.sql"),
		[]byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("dirtying the working tree: %v", err)
	}

	stdout, _, code := run("check", "--base", "HEAD~1", "--format", "json")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stdout)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F for a committed DROP TABLE. Findings: %+v", cert.Grade, cert.Findings)
	}

	var sawDrop bool
	for _, f := range cert.Findings {
		if f.RuleID == "PG001" {
			sawDrop = true
		}
		if f.File != filepath.ToSlash(f.File) {
			t.Errorf("finding file %q is not repository-relative with forward slashes", f.File)
		}
	}
	if !sawDrop {
		t.Errorf("the committed DROP TABLE was not detected; the working tree may have been read instead: %+v", cert.Findings)
	}
}

// A pathspec argument scopes the comparison, and the ref still decides what is compared.
func TestCheckScopesAGitRangeToASubtree(t *testing.T) {
	repo := gitRepo(t, []map[string]string{
		{"migrations/0001_init.up.sql": "CREATE TABLE orders (id bigint);\n"},
		{
			"migrations/0002_drop.up.sql": "DROP TABLE orders;\n",
			"other/0003_safe.up.sql":      "CREATE INDEX CONCURRENTLY i ON orders (id);\n",
		},
	})
	chdir(t, repo)

	stdout, _, code := run("check", "--base", "HEAD~1", "--format", "json", "other")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stdout)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	for _, f := range cert.Findings {
		if strings.HasPrefix(f.File, "migrations/") {
			t.Errorf("finding outside the requested subtree: %+v", f)
		}
	}
}

// The changeset sources are alternatives, not a precedence order. A user who believes they
// gated on one comparison must not have been given another.
func TestCheckRejectsAnAmbiguousChangesetSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		mustSay string
	}{
		{
			name:    "both git refs and directories",
			args:    []string{"check", "--base", "origin/main", "--before", "./old", "./new"},
			mustSay: "--base and --before",
		},
		{
			name:    "head without base",
			args:    []string{"check", "--head", "HEAD", "./migrations"},
			mustSay: "--head",
		},
		{
			name:    "nothing at all",
			args:    []string{"check"},
			mustSay: "no paths given",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr, code := run(tc.args...)
			if code != cli.ExitError {
				t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitError, stdout)
			}
			if !strings.Contains(stderr, tc.mustSay) {
				t.Errorf("stderr does not explain the conflict (%q):\n%s", tc.mustSay, stderr)
			}
		})
	}
}

// An unresolvable ref is a broken run, not an unsafe change: exit 2, not exit 1. Conflating the
// two is how a broken tool ends up ignored in a pipeline.
func TestCheckReportsAnUnknownRefAsABrokenRun(t *testing.T) {
	repo := gitRepo(t, []map[string]string{
		{"migrations/0001_init.up.sql": "CREATE TABLE orders (id bigint);\n"},
	})
	chdir(t, repo)

	_, stderr, code := run("check", "--base", "origin/does-not-exist", "--gate")
	if code != cli.ExitError {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitError)
	}
	if !strings.Contains(stderr, "does-not-exist") {
		t.Errorf("stderr does not name the ref:\n%s", stderr)
	}
}

// writePolicy writes a .reversibility.yml into dir and returns its path.
//
// Expiry dates are computed from the real clock rather than written as literals. The CLI reads
// the system date — that is what it does in production — so a literal would turn this suite into
// something that starts failing on a particular morning.
func writePolicy(t *testing.T, dir, body string) string {
	t.Helper()

	path := filepath.Join(dir, ".reversibility.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the policy: %v", err)
	}
	return path
}

func soon(t *testing.T) string {
	t.Helper()
	return time.Now().AddDate(0, 0, 30).Format("2006-01-02")
}

// A waiver unblocks the pipeline without rewriting the measurement. This is the whole contract
// of the policy file in one test.
func TestPolicyWaiverUnblocksTheGateWithoutMovingTheGrade(t *testing.T) {
	t.Parallel()

	root := destructiveMigrations(t)
	policyPath := writePolicy(t, t.TempDir(), fmt.Sprintf(`version: 1
waivers:
  - rule: PG001
    reason: "the table was already empty; verified in #482"
    expires: %s
    approved_by: "vikoit"
`, soon(t)))

	stdout, stderr, code := run("check", "--config", policyPath, "--min-grade", "A", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (the waiver should unblock the gate)\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F; a waiver must not move the measurement", cert.Grade)
	}
	if cert.EffectiveGrade != certificate.GradeA {
		t.Errorf("EffectiveGrade = %q, want A", cert.EffectiveGrade)
	}
	if cert.Passed() {
		t.Error("the AI merge gate passed on a waived irreversible change; a waiver must never let an agent merge")
	}
	if len(cert.Waived) != 1 {
		t.Fatalf("Waived = %+v, want the finding reported rather than suppressed", cert.Waived)
	}
	if cert.Waived[0].Reason == "" || cert.Waived[0].Expires == "" {
		t.Errorf("the waived finding lost its justification: %+v", cert.Waived[0])
	}
	if cert.PolicyDigest == "" {
		t.Error("PolicyDigest is empty despite a policy being applied")
	}
}

// The certificate a human reads has to show what was accepted, why, and until when.
func TestMarkdownReportsWaivedFindings(t *testing.T) {
	t.Parallel()

	root := destructiveMigrations(t)
	policyPath := writePolicy(t, t.TempDir(), fmt.Sprintf(`version: 1
waivers:
  - rule: PG001
    reason: "expand-contract; old code removed in #482"
    expires: %s
    approved_by: "vikoit"
`, soon(t)))

	stdout, _, code := run("check", "--config", policyPath, root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	for _, want := range []string{"### Waived", "expand-contract; old code removed in #482", "vikoit", "Grade after waivers"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the certificate does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestPolicyDiscoveryAndOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     func(root, policyPath string) []string
		wantCode int
		wantSay  string
	}{
		{
			// The policy beside the migrations is found without being named.
			name:     "a discovered policy applies",
			args:     func(root, _ string) []string { return []string{"check", "--min-grade", "A", root} },
			wantCode: cli.ExitOK,
		},
		{
			// --no-config is the documented way to see what the gate says without the policy.
			name:     "--no-config discards it",
			args:     func(root, _ string) []string { return []string{"check", "--no-config", "--min-grade", "A", root} },
			wantCode: cli.ExitGateFailed,
		},
		{
			name: "--config and --no-config together are refused",
			args: func(root, policyPath string) []string {
				return []string{"check", "--config", policyPath, "--no-config", root}
			},
			wantCode: cli.ExitError,
			wantSay:  "--config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := destructiveMigrations(t)
			policyPath := writePolicy(t, root, fmt.Sprintf(`version: 1
waivers:
  - rule: PG001
    reason: "the table was already empty; verified in #482"
    expires: %s
`, soon(t)))

			_, stderr, code := run(tc.args(root, policyPath)...)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", code, tc.wantCode, stderr)
			}
			if tc.wantSay != "" && !strings.Contains(stderr, tc.wantSay) {
				t.Errorf("stderr does not explain the problem (%q):\n%s", tc.wantSay, stderr)
			}
		})
	}
}

// A policy that cannot be resolved is a broken run, not an unsafe change: exit 2, not exit 1.
// Every one of these is an error rather than a warning, because a warning in a CI log is not
// read and the policy would take effect anyway.
func TestBrokenPolicyIsABrokenRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		mustSay string
	}{
		{
			name:    "a waiver with no reason",
			body:    "version: 1\nwaivers:\n  - rule: PG001\n    expires: 2026-10-01\n",
			mustSay: "reason is required",
		},
		{
			name:    "a waiver with no expiry",
			body:    "version: 1\nwaivers:\n  - rule: PG001\n    reason: \"later\"\n",
			mustSay: "expires is required",
		},
		{
			name:    "a waiver that outlives the window",
			body:    "version: 1\nwaivers:\n  - rule: PG001\n    reason: \"forever\"\n    expires: 2099-01-01\n",
			mustSay: "more than 180 days away",
		},
		{
			name:    "an override that loosens",
			body:    "version: 1\noverrides:\n  - rule: PG001\n    severity: REVERSIBLE\n",
			mustSay: "stricter",
		},
		{
			name:    "an unknown key",
			body:    "version: 1\nwaivers:\n  - rule: PG001\n    reason: \"x\"\n    expiress: 2026-10-01\n",
			mustSay: "expiress",
		},
		{
			name:    "a version this build does not know",
			body:    "version: 99\n",
			mustSay: "not supported",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := safeMigrations(t)
			policyPath := writePolicy(t, t.TempDir(), tc.body)

			_, stderr, code := run("check", "--config", policyPath, root)
			if code != cli.ExitError {
				t.Fatalf("exit code = %d, want %d (a broken policy is a broken run)\n%s", code, cli.ExitError, stderr)
			}
			if !strings.Contains(stderr, tc.mustSay) {
				t.Errorf("stderr does not explain the problem (%q):\n%s", tc.mustSay, stderr)
			}
		})
	}
}

// An ignored file is never read, so it never reaches an analyzer and never comes back as
// context either.
func TestPolicyIgnoreExcludesFilesFromAnalysis(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{
		"legacy/0001_drop.up.sql":     "DROP TABLE legacy_orders;\n",
		"current/0001_index.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"current/0001_index.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
	})
	writePolicy(t, root, "version: 1\nignore:\n  - \"legacy/**\"\n")

	stdout, stderr, code := run("check", "--min-grade", "A", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	for _, f := range cert.Findings {
		if strings.HasPrefix(f.File, "legacy/") {
			t.Errorf("an ignored file was analyzed anyway: %+v", f)
		}
	}
	if len(cert.Waived) != 0 {
		t.Errorf("an ignored file was reported as waived rather than skipped: %+v", cert.Waived)
	}
}

// The policy can set the threshold, and an explicit flag still wins: somebody who typed a
// threshold is making a decision about this run.
func TestPolicyGateIsOverriddenByTheFlag(t *testing.T) {
	t.Parallel()

	root := destructiveMigrations(t)
	writePolicy(t, root, "version: 1\ngate: F\n")

	if _, _, code := run("check", root); code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d; gate F accepts everything", code, cli.ExitOK)
	}
	if _, _, code := run("check", "--min-grade", "A", root); code != cli.ExitGateFailed {
		t.Errorf("exit code = %d, want %d; --min-grade must beat the policy", code, cli.ExitGateFailed)
	}
}

// A waiver's path matches the finding's file exactly as the certificate prints it, and a pattern
// that matches nothing is inert. Over-matching a waiver is the one direction this must not fail
// in, so the mismatch is deliberately a no-op rather than a near-miss that applies anyway.
func TestWaiverPathMatchesTheFindingAsReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		wantCode int
	}{
		{
			// `revctl check ./migrations` reports files relative to the directory named.
			name:     "a pattern matching the reported path applies",
			path:     "0001_*.sql",
			wantCode: cli.ExitOK,
		},
		{
			// The same waiver written for a repository-relative path matches nothing here.
			name:     "a pattern matching nothing is inert",
			path:     "migrations/0001_*.sql",
			wantCode: cli.ExitGateFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := destructiveMigrations(t)
			policyPath := writePolicy(t, t.TempDir(), fmt.Sprintf(`version: 1
waivers:
  - rule: PG001
    path: %q
    reason: "the table was already empty; verified in #482"
    expires: %s
`, tc.path, soon(t)))

			_, stderr, code := run("check", "--config", policyPath, "--min-grade", "A", root)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", code, tc.wantCode, stderr)
			}
		})
	}
}

// writeSnapshotFile writes a production snapshot and returns its path.
func writeSnapshotFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pg.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the snapshot: %v", err)
	}
	return path
}

func freshSnapshot(t *testing.T) string {
	t.Helper()

	return writeSnapshotFile(t, fmt.Sprintf(`{
  "schemaVersion": "1.0.0",
  "kind": "postgres",
  "collectedAt": %q,
  "sourceFingerprint": "clitestclitestclitestclitestclitestclitestclitestclitestclites0",
  "postgres": {
    "tables": [
      {"schema":"public","name":"legacy_orders","rowEstimate":212000000,"sizeBytes":34359738368,"totalSizeBytes":51539607552}
    ],
    "indexes": [],
    "columns": []
  }
}`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)))
}

// Production context is an enhancement, not a requirement. A workflow that passes --context
// unconditionally must keep working before the first snapshot is ever collected.
func TestMissingContextFileIsNotAnError(t *testing.T) {
	t.Parallel()

	root := safeMigrations(t)
	absent := filepath.Join(t.TempDir(), "never-collected.json")

	stdout, stderr, code := run("check", "--context", absent, "--gate", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cert.Grade != certificate.GradeA {
		t.Errorf("Grade = %q, want A; a missing snapshot must change nothing", cert.Grade)
	}
	if len(cert.ContextWarnings) != 0 {
		t.Errorf("a missing snapshot produced warnings: %v", cert.ContextWarnings)
	}
}

// The headline of the session: a category becomes a quantity.
func TestContextMakesTheCertificateConcrete(t *testing.T) {
	t.Parallel()

	root := destructiveMigrations(t)
	snap := freshSnapshot(t)

	stdout, stderr, code := run("check", "--context", snap, "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// PG001 is not a rule that gains context, so this asserts the shape rather than the number:
	// the run completed, the snapshot was accepted, and nothing was warned about.
	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
	if len(cert.ContextWarnings) != 0 {
		t.Errorf("a fresh snapshot warned: %v", cert.ContextWarnings)
	}

	// The same run without context must produce the same verdict and a different digest.
	plain, _, _ := run("check", "--format", "json", root)
	var without certificate.Certificate
	if err := json.Unmarshal([]byte(plain), &without); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if without.Grade != cert.Grade {
		t.Errorf("context changed the grade: %q without, %q with", without.Grade, cert.Grade)
	}
	if without.InputDigest == cert.InputDigest {
		t.Error("supplying a snapshot did not change the input digest")
	}
}

// A stale snapshot is used and flagged. Silently falling back to none would make the certificate
// quietly less informative at exactly the moment somebody stopped refreshing it.
func TestStaleContextIsReportedAndStillUsed(t *testing.T) {
	t.Parallel()

	root := safeMigrations(t)
	stale := writeSnapshotFile(t, `{
  "schemaVersion": "1.0.0",
  "kind": "postgres",
  "collectedAt": "2020-01-01T00:00:00Z",
  "sourceFingerprint": "staleeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
  "postgres": {"tables": [], "indexes": [], "columns": []}
}`)

	stdout, stderr, code := run("check", "--context", stale, "--gate", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d; a stale snapshot must not fail the run\n%s", code, cli.ExitOK, stderr)
	}
	if !strings.Contains(stderr, "days old") {
		t.Errorf("stderr does not report the stale snapshot:\n%s", stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(cert.ContextWarnings) != 1 {
		t.Errorf("ContextWarnings = %v, want one", cert.ContextWarnings)
	}
	if cert.Grade != certificate.GradeA {
		t.Errorf("Grade = %q, want A; a stale snapshot must not change a verdict", cert.Grade)
	}
}

// Context that is wrong is worse than context that is absent, because context is believed.
func TestUnreadableContextIsABrokenRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		mustSay string
	}{
		{
			name:    "a version this build cannot read",
			body:    `{"schemaVersion":"9.9.9","kind":"postgres","collectedAt":"2026-08-24T00:00:00Z","sourceFingerprint":"a","postgres":{"tables":[],"indexes":[],"columns":[]}}`,
			mustSay: "not supported",
		},
		{
			name:    "a field this build does not know",
			body:    `{"schemaVersion":"1.0.0","kind":"postgres","collectedAt":"2026-08-24T00:00:00Z","sourceFingerprint":"a","postgres":{"tables":[],"indexes":[],"columns":[]},"newThing":1}`,
			mustSay: "unknown field",
		},
		{
			name:    "not json at all",
			body:    `this is not a snapshot`,
			mustSay: "decoding",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, stderr, code := run("check", "--context", writeSnapshotFile(t, tc.body), safeMigrations(t))
			if code != cli.ExitError {
				t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitError, stderr)
			}
			if !strings.Contains(stderr, tc.mustSay) {
				t.Errorf("stderr does not explain the problem (%q):\n%s", tc.mustSay, stderr)
			}
		})
	}
}

// The snapshot command is a separate command for a reason: analysis never connects to anything.
func TestSnapshotCommandRefusesAnAmbiguousRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		mustSay string
	}{
		{"no output", []string{"snapshot", "--dsn", "postgres://x/y"}, "--out is required"},
		{"nothing to collect", []string{"snapshot", "--out", "x.json"}, "nothing to collect"},
		{
			"both sources at once",
			[]string{"snapshot", "--out", "x.json", "--dsn", "postgres://x/y", "--kube-context", "prod"},
			"run the command twice",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, stderr, code := run(tc.args...)
			if code != cli.ExitError {
				t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitError, stderr)
			}
			if !strings.Contains(stderr, tc.mustSay) {
				t.Errorf("stderr does not explain the problem (%q):\n%s", tc.mustSay, stderr)
			}
		})
	}
}

// "Metadata only" has to be verifiable, and the first place anybody looks is --help.
func TestSnapshotHelpListsWhatIsCollected(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("snapshot", "--help")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	for _, want := range []string{
		"WHAT IS COLLECTED", "WHAT IS NEVER COLLECTED",
		"reltuples", "null_frac", "reclaimPolicy",
		"No row of user data", "default_transaction_read_only", "pg_monitor",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the help text does not mention %q", want)
		}
	}
}
