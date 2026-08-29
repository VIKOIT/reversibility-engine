// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy_test

import (
	"errors"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
)

func finding(rule, file string, r domain.Reversibility) domain.Finding {
	return domain.Finding{
		RuleID:        rule,
		File:          file,
		Line:          1,
		Reversibility: r,
		LockHazard:    domain.LockNone,
		Rationale:     "because",
	}
}

func waiver(rule, path, expires string) policy.Waiver {
	return policy.Waiver{Rule: rule, Path: path, Reason: "accepted", Expires: expires, ApprovedBy: "vikoit"}
}

func TestApplyWaivers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		waivers    []policy.Waiver
		findings   []domain.Finding
		today      time.Time
		wantScored []string // rule IDs still counting toward the effective grade
		wantWaived []string
	}{
		{
			name:       "no waivers leaves everything scored",
			findings:   []domain.Finding{finding("PG001", "a.sql", domain.ReversibilityIrreversible)},
			today:      today,
			wantScored: []string{"PG001"},
		},
		{
			name:       "a live waiver moves the finding aside",
			waivers:    []policy.Waiver{waiver("PG012", "migrations/0031_*.sql", "2026-10-01")},
			findings:   []domain.Finding{finding("PG012", "migrations/0031_backfill.sql", domain.ReversibilityCostly)},
			today:      today,
			wantWaived: []string{"PG012"},
		},
		{
			// The finding comes back on its own, with no edit to the policy and no
			// announcement. That is the entire point of requiring a date.
			name:       "an expired waiver is inert",
			waivers:    []policy.Waiver{waiver("PG012", "", "2026-08-24")},
			findings:   []domain.Finding{finding("PG012", "a.sql", domain.ReversibilityCostly)},
			today:      today,
			wantScored: []string{"PG012"},
		},
		{
			name:       "a waiver covers its whole expiry day",
			waivers:    []policy.Waiver{waiver("PG012", "", "2026-08-25")},
			findings:   []domain.Finding{finding("PG012", "a.sql", domain.ReversibilityCostly)},
			today:      today,
			wantWaived: []string{"PG012"},
		},
		{
			name:       "the path narrows the waiver",
			waivers:    []policy.Waiver{waiver("PG012", "migrations/0031_*.sql", "2026-10-01")},
			findings:   []domain.Finding{finding("PG012", "migrations/0099_other.sql", domain.ReversibilityCostly)},
			today:      today,
			wantScored: []string{"PG012"},
		},
		{
			name:       "a waiver for another rule does not apply",
			waivers:    []policy.Waiver{waiver("PG012", "", "2026-10-01")},
			findings:   []domain.Finding{finding("PG001", "a.sql", domain.ReversibilityIrreversible)},
			today:      today,
			wantScored: []string{"PG001"},
		},
		{
			// Accepting a risk nobody has characterised is not a decision anyone is in a
			// position to make. This is the fail-closed boundary a waiver may not cross.
			name:       "a waiver cannot cover UNKNOWN",
			waivers:    []policy.Waiver{waiver("PG027", "", "2026-10-01")},
			findings:   []domain.Finding{finding("PG027", "a.sql", domain.ReversibilityUnknown)},
			today:      today,
			wantScored: []string{"PG027"},
		},
		{
			// Not a risk to accept but a certainty of failure. A waiver documents a trade-off,
			// and there is none in a statement that cannot apply.
			name:       "a waiver cannot cover WILL_FAIL",
			waivers:    []policy.Waiver{waiver("PG017", "", "2026-10-01")},
			findings:   []domain.Finding{finding("PG017", "a.sql", domain.ReversibilityWillFail)},
			today:      today,
			wantScored: []string{"PG017"},
		},
		{
			// A verdict the domain cannot read is a broken analyzer, not a classified change.
			name:       "a waiver cannot cover an unrecognised verdict",
			waivers:    []policy.Waiver{waiver("PG012", "", "2026-10-01")},
			findings:   []domain.Finding{finding("PG012", "a.sql", domain.Reversibility("nonsense"))},
			today:      today,
			wantScored: []string{"PG012"},
		},
		{
			name:    "an empty path covers every file",
			waivers: []policy.Waiver{waiver("PG012", "", "2026-10-01")},
			findings: []domain.Finding{
				finding("PG012", "a/deep/path.sql", domain.ReversibilityCostly),
				finding("PG012", "top.sql", domain.ReversibilityCostly),
			},
			today:      today,
			wantWaived: []string{"PG012", "PG012"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &policy.Policy{Waivers: tc.waivers}

			decision, err := p.Apply(tc.findings, tc.today, domain.Identity())
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if got := ruleIDs(decision.Findings); !equal(got, tc.wantScored) {
				t.Errorf("scored findings = %v, want %v", got, tc.wantScored)
			}

			waivedIDs := make([]string, 0, len(decision.Waived))
			for _, w := range decision.Waived {
				waivedIDs = append(waivedIDs, w.Finding.RuleID)
			}
			if !equal(waivedIDs, tc.wantWaived) {
				t.Errorf("waived findings = %v, want %v", waivedIDs, tc.wantWaived)
			}

			// Whatever was waived, the full set has to stay whole: the grade and the undo plan
			// are computed from it, and a waiver must not make a change look reversible.
			if len(decision.All) != len(tc.findings) {
				t.Errorf("All has %d findings, want all %d", len(decision.All), len(tc.findings))
			}
		})
	}
}

// A waived finding keeps the reason and the expiry beside it. Silent suppression is how a safety
// tool stops being one, so the certificate has to be able to show why and until when.
func TestWaivedFindingsCarryTheirJustification(t *testing.T) {
	t.Parallel()

	p := &policy.Policy{Waivers: []policy.Waiver{{
		Rule:       "PG012",
		Reason:     "expand-contract; old code removed in #482",
		Expires:    "2026-10-01",
		ApprovedBy: "vikoit",
	}}}

	decision, err := p.Apply([]domain.Finding{finding("PG012", "a.sql", domain.ReversibilityCostly)}, today, domain.Identity())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(decision.Waived) != 1 {
		t.Fatalf("Waived = %v, want 1", decision.Waived)
	}

	w := decision.Waived[0]
	if w.Reason != "expand-contract; old code removed in #482" {
		t.Errorf("Reason = %q", w.Reason)
	}
	if w.Expires != "2026-10-01" {
		t.Errorf("Expires = %q", w.Expires)
	}
	if w.ApprovedBy != "vikoit" {
		t.Errorf("ApprovedBy = %q", w.ApprovedBy)
	}
	if w.Finding.RuleID != "PG012" || w.Finding.Rationale == "" {
		t.Errorf("the waived finding was summarised away: %+v", w.Finding)
	}
}

func TestApplyOverrides(t *testing.T) {
	t.Parallel()

	t.Run("tightening is applied", func(t *testing.T) {
		t.Parallel()

		p := &policy.Policy{Overrides: []policy.Override{{
			Rule:     "K8S008",
			Severity: domain.ReversibilityIrreversible,
		}}}

		decision, err := p.Apply([]domain.Finding{finding("K8S008", "d.yaml", domain.ReversibilityCostly)}, today, domain.Identity())
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got := decision.Findings[0].Reversibility; got != domain.ReversibilityIrreversible {
			t.Errorf("Reversibility = %q, want IRREVERSIBLE", got)
		}
	})

	t.Run("loosening is refused", func(t *testing.T) {
		t.Parallel()

		// Parse rejects an override to REVERSIBLE outright, so this is the case that only shows
		// up at apply time: COSTLY is weaker than the IRREVERSIBLE the analyzer found.
		p := &policy.Policy{Overrides: []policy.Override{{
			Rule:     "PG001",
			Severity: domain.ReversibilityCostly,
		}}}

		_, err := p.Apply([]domain.Finding{finding("PG001", "a.sql", domain.ReversibilityIrreversible)}, today, domain.Identity())
		if err == nil {
			t.Fatal("an override that weakened a finding was accepted")
		}
		if !errors.Is(err, domain.ErrInvalidPolicy) {
			t.Errorf("error = %v, want one wrapping ErrInvalidPolicy", err)
		}
	})

	t.Run("an override for a rule that did not fire changes nothing", func(t *testing.T) {
		t.Parallel()

		p := &policy.Policy{Overrides: []policy.Override{{
			Rule:     "K8S008",
			Severity: domain.ReversibilityIrreversible,
		}}}

		decision, err := p.Apply([]domain.Finding{finding("PG001", "a.sql", domain.ReversibilityIrreversible)}, today, domain.Identity())
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if decision.Findings[0].Reversibility != domain.ReversibilityIrreversible {
			t.Errorf("an unrelated finding was rewritten: %+v", decision.Findings[0])
		}
	})

	// Overrides run first so that a waiver written for a mild classification cannot swallow a
	// finding the operator separately decided to treat as severe.
	t.Run("tightening happens before waiving", func(t *testing.T) {
		t.Parallel()

		p := &policy.Policy{
			Overrides: []policy.Override{{Rule: "K8S008", Severity: domain.ReversibilityUnknown}},
			Waivers:   []policy.Waiver{waiver("K8S008", "", "2026-10-01")},
		}

		decision, err := p.Apply([]domain.Finding{finding("K8S008", "d.yaml", domain.ReversibilityCostly)}, today, domain.Identity())
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(decision.Waived) != 0 {
			t.Errorf("a finding tightened to UNKNOWN was waived anyway: %+v", decision.Waived)
		}
	})
}

func TestIgnores(t *testing.T) {
	t.Parallel()

	p := &policy.Policy{Ignore: []string{"legacy/**", "**/*.generated.sql"}}

	for path, want := range map[string]bool{
		"legacy/0001.sql":         true,
		"db/schema.generated.sql": true,
		"migrations/0001.sql":     false,
	} {
		if got := p.Ignores(domain.Located(path)); got != want {
			t.Errorf("Ignores(%q) = %v, want %v", path, got, want)
		}
	}
}

// A nil policy is the no-policy case, and it has to behave exactly as the engine did before
// policies existed rather than force a branch on every caller.
func TestNilPolicyIsInert(t *testing.T) {
	t.Parallel()

	var p *policy.Policy

	if p.Ignores("anything.sql") {
		t.Error("a nil policy ignored a path")
	}

	findings := []domain.Finding{finding("PG001", "a.sql", domain.ReversibilityIrreversible)}
	decision, err := p.Apply(findings, today, domain.Identity())
	if err != nil {
		t.Fatalf("Apply on a nil policy: %v", err)
	}
	if len(decision.Findings) != 1 || len(decision.Waived) != 0 || len(decision.All) != 1 {
		t.Errorf("a nil policy changed the findings: %+v", decision)
	}
}

func ruleIDs(findings []domain.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.RuleID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
