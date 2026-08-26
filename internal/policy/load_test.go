// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
)

// today is fixed in every test. Waiver expiry compares against a date, so a suite that used the
// real clock would start failing on a specific morning for reasons unrelated to any change.
var today = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func load(t *testing.T, name string) (*policy.Policy, error) {
	t.Helper()
	return policy.Load(filepath.Join("testdata", name), today)
}

func TestLoadValidPolicy(t *testing.T) {
	t.Parallel()

	p, err := load(t, "valid.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p.Gate != domain.GradeA {
		t.Errorf("Gate = %q, want A", p.Gate)
	}
	if len(p.Ignore) != 2 {
		t.Errorf("Ignore = %v, want 2 patterns", p.Ignore)
	}
	if len(p.Waivers) != 1 {
		t.Fatalf("Waivers = %v, want 1", p.Waivers)
	}

	w := p.Waivers[0]
	if w.Rule != "PG012" || w.Path != "migrations/0031_*.sql" || w.ApprovedBy != "vikoit" {
		t.Errorf("waiver = %+v", w)
	}

	// The documented spelling is an unquoted YAML date. A decoder that turned it into a
	// timestamp would give a string this package cannot parse, so the exact round trip is
	// pinned rather than assumed.
	if w.Expires != "2026-10-01" {
		t.Errorf("Expires = %q, want the literal date 2026-10-01", w.Expires)
	}

	if len(p.Overrides) != 1 || p.Overrides[0].Rule != "K8S008" ||
		p.Overrides[0].Severity != domain.ReversibilityIrreversible {
		t.Errorf("Overrides = %+v", p.Overrides)
	}

	if p.Digest == "" {
		t.Error("Digest is empty; a policy that cannot be attributed is not evidence")
	}
}

func TestLoadMinimalPolicy(t *testing.T) {
	t.Parallel()

	p, err := load(t, "minimal.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Gate != "" {
		t.Errorf("Gate = %q, want empty when the file did not say", p.Gate)
	}
}

// Every one of these is a configuration error rather than a warning. A warning in a CI log is
// not read, and the policy would take effect anyway — which is the unexplained suppression this
// design exists to prevent.
func TestLoadRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file    string
		mustSay string
	}{
		{"missing_reason.yml", "reason is required"},
		{"missing_expires.yml", "expires is required"},
		{"bad_date.yml", "YYYY-MM-DD"},
		{"duration_not_date.yml", "YYYY-MM-DD"},
		{"far_future.yml", "more than 180 days away"},
		{"unknown_field.yml", "expiress"},
		{"bad_version.yml", "version 2 is not supported"},
		{"no_version.yml", "version 0 is not supported"},
		{"loosening_override.yml", "only make a rule stricter"},
		{"bad_gate.yml", "is not a grade"},
		{"bad_pattern.yml", "ignore[0]"},
		{"no_rule.yml", "rule is required"},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()

			p, err := load(t, tc.file)
			if err == nil {
				t.Fatalf("Load(%s) succeeded with %+v, want an error", tc.file, p)
			}
			if p != nil {
				t.Errorf("a rejected policy returned %+v; a half-read policy must never be used", p)
			}
			if !errors.Is(err, domain.ErrInvalidPolicy) {
				t.Errorf("error = %v, want one wrapping ErrInvalidPolicy", err)
			}
			if !strings.Contains(err.Error(), tc.mustSay) {
				t.Errorf("error %q does not explain the problem (%q)", err, tc.mustSay)
			}
		})
	}
}

// The window is measured from the day the policy is parsed, so the same file is accepted today
// and refused once the date it names is more than the window away.
func TestWaiverWindowIsMeasuredFromToday(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "far_future.yml"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	// 2027-12-31 is far away from 2026-08-25 and close to 2027-08-01.
	if _, err := policy.Parse(raw, "far_future.yml", today); err == nil {
		t.Error("a waiver expiring in 2027 was accepted as of 2026-08-25")
	}

	near := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := policy.Parse(raw, "far_future.yml", near); err != nil {
		t.Errorf("the same waiver was refused as of 2027-08-01, which is inside the window: %v", err)
	}
}

func TestDigestCoversTheResolvedPolicy(t *testing.T) {
	t.Parallel()

	first, err := load(t, "valid.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	again, err := load(t, "valid.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if first.Digest != again.Digest {
		t.Errorf("the same file produced two digests: %s and %s", first.Digest, again.Digest)
	}

	other, err := load(t, "minimal.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if other.Digest == first.Digest {
		t.Error("two different policies share a digest; a digest that collides is not evidence")
	}

	// Comments and formatting are not the configuration. A digest that moved when somebody
	// reflowed the file would report a policy change that did not happen.
	reformatted := []byte("version: 1\ngate: A\nignore: [\"legacy/**\", \"**/*.generated.sql\"]\n" +
		"waivers: [{rule: PG012, path: \"migrations/0031_*.sql\", " +
		"reason: \"expand-contract; old code removed in #482\", expires: \"2026-10-01\", approved_by: vikoit}]\n" +
		"overrides: [{rule: K8S008, severity: IRREVERSIBLE}]\n" +
		"terraform_types: [{type: google_sql_database_instance, class: STATEFUL}]\n")

	compact, err := policy.Parse(reformatted, "compact.yml", today)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if compact.Digest != first.Digest {
		t.Errorf("reformatting changed the digest:\n  block form:   %s\n  flow form:    %s", first.Digest, compact.Digest)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("creating %s: %v", deep, err)
	}

	// A repository boundary with no policy in it.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("creating .git: %v", err)
	}

	found, err := policy.Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found != "" {
		t.Errorf("Discover found %q in a tree with no policy", found)
	}

	want := filepath.Join(root, policy.FileName)
	if err := os.WriteFile(want, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("writing the policy: %v", err)
	}

	found, err = policy.Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found != want {
		t.Errorf("Discover = %q, want %q", found, want)
	}

	// The nearest policy wins, so a subdirectory can tighten without editing the root.
	nearer := filepath.Join(root, "a", policy.FileName)
	if err := os.WriteFile(nearer, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("writing the nearer policy: %v", err)
	}

	found, err = policy.Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found != nearer {
		t.Errorf("Discover = %q, want the nearest policy %q", found, nearer)
	}
}

// Without the repository boundary a project with no policy would inherit whichever file happened
// to sit above it — a home directory, or the temp directory during a test run.
func TestDiscoverStopsAtTheRepositoryRoot(t *testing.T) {
	t.Parallel()

	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, policy.FileName), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("writing the outer policy: %v", err)
	}

	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("creating the repo: %v", err)
	}

	found, err := policy.Discover(repo)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found != "" {
		t.Errorf("Discover walked past the repository root and found %q", found)
	}
}
