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

	// N/A, not A. Exit 0 is correct here — there was genuinely nothing to read — but the
	// certificate must not claim the change was found reversible, which is what A says.
	if cert.Grade != certificate.GradeNotApplicable {
		t.Errorf("Grade = %q, want N/A", cert.Grade)
	}
	if cert.Outcome != certificate.OutcomeNoCandidates {
		t.Errorf("Outcome = %q, want NO_CANDIDATES", cert.Outcome)
	}
	if cert.Passed() {
		t.Error("a changeset the engine never analyzed reports the gate as passed")
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

// TestBareInvocationIsNotAPass is the regression test for the fail-open that shipped in the
// v1.1.0 image: revctl with no arguments printed help and exited zero, so the single invocation
// that analyzes nothing was also the only one that could never fail. A container entrypoint, a
// wrapper script, or a CI template with an unset variable could therefore report success over no
// analysis at all — which is what happened, and it graded green for every @v1 consumer.
//
// A gate must prove it ran.
func TestBareInvocationIsNotAPass(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := run()

	if code != cli.ExitError {
		t.Errorf("exit code = %d, want %d — a run that analyzed nothing must not succeed", code, cli.ExitError)
	}

	// Help is still printed, because a bare invocation is usually a typo and the user needs to
	// see what to type instead. It goes to stderr: a caller piping stdout into a certificate
	// must not receive usage text where a verdict belongs.
	if !strings.Contains(stderr, "revctl") || !strings.Contains(stderr, "check") {
		t.Errorf("bare invocation did not print help to stderr:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("bare invocation wrote to stdout, which is reserved for the certificate:\n%s", stdout)
	}
}

// TestHelpIsStillASuccess separates asking for help from failing to give a command. Making the
// former non-zero would break every `revctl --help` in a Makefile and teach users to ignore the
// exit code, which is the habit the test above depends on them not having.
func TestHelpIsStillASuccess(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--help"}, {"help"}, {"help", "check"}} {
		if _, _, code := run(args...); code != cli.ExitOK {
			t.Errorf("revctl %v: exit code = %d, want %d", args, code, cli.ExitOK)
		}
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

// writePlan writes a Terraform plan document and returns the directory holding it.
func writePlan(t *testing.T, name, body string) string {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing the plan: %v", err)
	}
	return root
}

const destroyPlanJSON = `{"format_version":"1.1","terraform_version":"1.9.5","resource_changes":[
  {"address":"aws_db_instance.orders","mode":"managed","type":"aws_db_instance",
   "change":{"actions":["delete"],"before":{"id":"orders-prod","allocated_storage":500},"after":null}}]}`

const unknownTypePlanJSON = `{"format_version":"1.1","terraform_version":"1.9.5","resource_changes":[
  {"address":"aws_zeta_thing.a","mode":"managed","type":"aws_zeta_thing",
   "change":{"actions":["delete"],"before":{"id":"a"},"after":null}},
  {"address":"aws_alpha_thing.b","mode":"managed","type":"aws_alpha_thing",
   "change":{"actions":["delete"],"before":{"id":"b"},"after":null}}]}`

// A destroyed database in a plan grades F, and the catalog that said so is on the certificate.
func TestTerraformPlanIsAnalyzed(t *testing.T) {
	t.Parallel()

	root := writePlan(t, "main.tfplan.json", destroyPlanJSON)

	stdout, stderr, code := run("check", "--no-config", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if cert.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F. Findings: %+v", cert.Grade, cert.Findings)
	}
	if cert.CatalogVersion == "" {
		t.Error("CatalogVersion is empty despite a plan being classified")
	}
	if len(cert.Findings) != 1 || cert.Findings[0].RuleID != "TF001" {
		t.Errorf("findings = %+v, want one TF001", cert.Findings)
	}
}

// The extension convention is the default; the flag is the escape hatch for a plan named
// otherwise, so nobody has to rename a file to be analyzed.
func TestTerraformPlanFlagClaimsADifferentlyNamedFile(t *testing.T) {
	t.Parallel()

	root := writePlan(t, "tfplan-output.json", destroyPlanJSON)
	target := filepath.Join(root, "tfplan-output.json")

	// Without the flag the file is not claimed at all, so the changeset is not applicable.
	stdout, _, code := run("check", "--no-config", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	var ignored certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &ignored); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if ignored.Applicable {
		t.Errorf("a file named tfplan-output.json was claimed without the flag; the convention is *.tfplan.json")
	}

	// With it, the same file is analyzed.
	stdout, stderr, code := run("check", "--no-config", "--terraform-plan", target, "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	var claimed certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &claimed); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if claimed.Grade != certificate.GradeF {
		t.Errorf("Grade = %q, want F once the plan was claimed", claimed.Grade)
	}
}

// THE GROWTH LOOP. Unknown types produce ONE snippet and ONE issue link covering all of them.
// Six unknown types meaning six paste operations is where somebody disables the gate instead.
func TestUnclassifiedTypesProduceOneAggregatedSuggestion(t *testing.T) {
	t.Parallel()

	root := writePlan(t, "main.tfplan.json", unknownTypePlanJSON)

	stdout, _, code := run("check", "--no-config", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	if n := strings.Count(stdout, "terraform_types:"); n != 1 {
		t.Errorf("the certificate contains %d policy snippets, want exactly 1", n)
	}
	if n := strings.Count(stdout, "issues/new?"); n != 1 {
		t.Errorf("the certificate contains %d issue links, want exactly 1", n)
	}

	for _, want := range []string{"aws_alpha_thing", "aws_zeta_thing"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the suggestion does not cover %s", want)
		}
	}

	// Sorted, so the snippet is stable between runs.
	alpha := strings.Index(stdout, "type: aws_alpha_thing")
	zeta := strings.Index(stdout, "type: aws_zeta_thing")
	if alpha < 0 || zeta < 0 || alpha > zeta {
		t.Errorf("the snippet is not in sorted order (alpha at %d, zeta at %d)", alpha, zeta)
	}
}

// A user may classify a type the catalog does not know. Weakening one it does is a configuration
// error, and a configuration error is a broken run: exit 2, not exit 1.
func TestTerraformTypeOverrides(t *testing.T) {
	t.Parallel()

	t.Run("classifying an unknown type is permitted", func(t *testing.T) {
		t.Parallel()

		root := writePlan(t, "main.tfplan.json", unknownTypePlanJSON)
		policyPath := writePolicy(t, root, "version: 1\nterraform_types:\n  - type: aws_zeta_thing\n    class: STATEFUL\n  - type: aws_alpha_thing\n    class: STATELESS\n")
		_ = policyPath

		stdout, _, code := run("check", "--format", "json", root)
		if code != cli.ExitOK {
			t.Fatalf("exit code = %d", code)
		}

		var cert certificate.Certificate
		if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
			t.Fatalf("decoding: %v", err)
		}

		rules := map[string]string{}
		for _, f := range cert.Findings {
			rules[f.RuleID] = f.Statement
		}
		if _, ok := rules["TF010"]; ok {
			t.Errorf("a type the user classified is still unknown: %+v", cert.Findings)
		}
		if _, ok := rules["TF001"]; !ok {
			t.Errorf("the STATEFUL classification did not take effect: %+v", cert.Findings)
		}
	})

	t.Run("weakening a catalogued type is a broken run", func(t *testing.T) {
		t.Parallel()

		root := writePlan(t, "main.tfplan.json", destroyPlanJSON)
		writePolicy(t, root, "version: 1\nterraform_types:\n  - type: aws_db_instance\n    class: STATELESS\n")

		_, stderr, code := run("check", root)
		if code != cli.ExitError {
			t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitError, stderr)
		}
		if !strings.Contains(stderr, "waiver") {
			t.Errorf("stderr does not point at the waiver path:\n%s", stderr)
		}
	})
}

// The catalog is compiled in and its identity is printable without a network.
func TestCatalogShow(t *testing.T) {
	t.Parallel()

	stdout, _, code := run("catalog", "show")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	for _, want := range []string{"catalog version", "digest", "classified", "stateful", "stateless"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("catalog show does not report %q:\n%s", want, stdout)
		}
	}
}

// catalog scan is a maintainer tool. It must fail with a message that says what to install
// rather than an exec error, and nothing in the check path may depend on it.
func TestCatalogScanFailsClearlyWithoutTerraform(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("terraform"); err == nil {
		t.Skip("terraform is installed; this test covers the machine where it is not")
	}

	_, stderr, code := run("catalog", "scan", "--provider", "aws")
	if code != cli.ExitError {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitError)
	}
	if !strings.Contains(stderr, "terraform is not on PATH") {
		t.Errorf("stderr does not say what is missing:\n%s", stderr)
	}
}

// A docs-only changeset has full coverage: nothing was skipped, because there was nothing to
// skip. --require-full-coverage must not turn "nothing to do" into a broken run.
func TestFullCoverageAcceptsAChangesetWithNothingToAnalyze(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"README.md": "# hello\n", "main.go": "package main\n"})

	stdout, stderr, code := run("check", "--no-config", "--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cert.Coverage != certificate.CoverageFull {
		t.Errorf("Coverage = %q for a docs-only change, want FULL — nothing was skipped", cert.Coverage)
	}
}

// A partial pass is a bypass. This is the CLI half of the strict-coverage ruling, and it
// reverses an earlier one: partial coverage used to exit 0 by default and fail only behind
// --require-full-coverage. It now fails unconditionally.
//
// Exit 2, not 1. The grade was not too low; part of the changeset was never measured, which is a
// run that did not complete. Reporting it as a failed gate would invite the fix a failed gate
// invites — lower the threshold — and no threshold makes an unread migration safe.
func TestPartialCoverageFailsWithoutAnyFlag(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{
		"db/migrate/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"db/migrate/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
		"db/migrate/0002_backfill.rb":  "class Backfill < ActiveRecord::Migration\nend\n",
	})

	for _, args := range [][]string{
		{"check", "--no-config", "--format", "json", root},
		{"check", "--gate", "--no-config", "--format", "json", root},
		{"check", "--min-grade", "F", "--no-config", "--format", "json", root},
	} {
		_, stderr, code := run(args...)
		if code != cli.ExitError {
			t.Errorf("%v: exit code = %d, want %d\n%s", args, code, cli.ExitError, stderr)
		}
		if !strings.Contains(stderr, "Cannot guarantee reversibility") {
			t.Errorf("%v: stderr does not carry the ruling's message:\n%s", args, stderr)
		}
		if !strings.Contains(stderr, "0002_backfill.rb") {
			t.Errorf("%v: stderr does not name the unread file:\n%s", args, stderr)
		}
	}
}

// The denominator is every file in the migration directory, not every file an analyzer wanted.
// A README beside the migrations fails the changeset, and the config is the way out — which is
// what the message tells the reader to do.
func TestCoverageCountsNonMigrationFilesAndTheConfigIsTheEscape(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"db/migrate/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"db/migrate/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
		"db/migrate/README.md":         "# how to write migrations\n",
	}

	root := writeTree(t, files)
	_, stderr, code := run("check", "--no-config", "--format", "json", root)
	if code != cli.ExitError {
		t.Fatalf("a README in the migration directory exits %d, want %d\n%s", code, cli.ExitError, stderr)
	}

	// Now ignore it explicitly, which is the remedy the message names.
	files[".reversibility.yml"] = "version: 1\nignore:\n  - \"**/README.md\"\n"
	root = writeTree(t, files)

	stdout, stderr, code := run("check", "--gate", "--config", filepath.Join(root, ".reversibility.yml"),
		"--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("ignoring the README exits %d, want %d\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cert.Coverage != certificate.CoverageFull {
		t.Errorf("Coverage = %q after the README was ignored, want FULL", cert.Coverage)
	}

	// And the escape hatch has to actually work: ignoring a file that was never a migration
	// candidate must not close the AI gate. If it did, the only way to satisfy strict coverage
	// would permanently deny an agent a merge, and nobody would use either.
	if !cert.Passed() {
		t.Errorf("aiGateStatus = %q after ignoring a README; ignoring a non-candidate must not close the gate",
			cert.AIGateStatus)
	}
	if len(cert.IgnoredByPolicy) != 1 {
		t.Errorf("IgnoredByPolicy = %v; the ignored file is still reported", cert.IgnoredByPolicy)
	}
}

// Ignoring something that really is a migration is a different act, and it does close the gate.
// This is the §16.8 rule, and it is what stops the escape hatch from being a bypass.
func TestIgnoringARealMigrationStillClosesTheGate(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{
		"db/migrate/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
		"db/migrate/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
		"db/migrate/0002_backfill.rb":  "class Backfill < ActiveRecord::Migration\nend\n",
		".reversibility.yml":           "version: 1\nignore:\n  - \"**/*.rb\"\n",
	})

	stdout, stderr, code := run("check", "--gate", "--config", filepath.Join(root, ".reversibility.yml"),
		"--format", "json", root)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, cli.ExitOK, stderr)
	}

	var cert certificate.Certificate
	if err := json.Unmarshal([]byte(stdout), &cert); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cert.Passed() {
		t.Error("aiGateStatus is PASS after a real migration was ignored; a human decision never buys an agent a merge")
	}
}
