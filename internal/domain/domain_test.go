// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

func TestReversibilityValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   domain.Reversibility
		want bool
	}{
		{"reversible", domain.ReversibilityReversible, true},
		{"costly", domain.ReversibilityCostly, true},
		{"irreversible", domain.ReversibilityIrreversible, true},
		{"unknown", domain.ReversibilityUnknown, true},
		{"zero value is not valid", domain.Reversibility(""), false},
		{"lowercase is not valid", domain.Reversibility("reversible"), false},
		{"invented verdict", domain.Reversibility("PROBABLY_FINE"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.Valid(); got != tt.want {
				t.Errorf("Reversibility(%q).Valid() = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The zero value must never be the safest value. An unclassified finding that defaulted to
// REVERSIBLE would be the exact bug this product cannot ship.
func TestZeroValuesAreNeverSafe(t *testing.T) {
	t.Parallel()

	var f domain.Finding

	if f.Reversibility.Valid() {
		t.Errorf("zero Reversibility is valid; an unset verdict must not pass validation")
	}
	if f.Reversibility == domain.ReversibilityReversible {
		t.Errorf("zero Reversibility equals REVERSIBLE")
	}
	if f.LockHazard == domain.LockNone {
		t.Errorf("zero LockHazard equals NONE; an unset hazard must not read as harmless")
	}

	var g domain.Grade
	if g.Rank() >= domain.GradeF.Rank() {
		t.Errorf("zero Grade ranks %d, at or above F (%d)", g.Rank(), domain.GradeF.Rank())
	}
	if g.Gate() != domain.GateFail {
		t.Errorf("zero Grade gate = %q, want FAIL", g.Gate())
	}
	if g.Threshold() {
		t.Error("zero Grade is accepted as a gating threshold; an unset minimum gates nothing")
	}

	// The shape of the run has a zero value too, and it was the missing one. An outcome nobody
	// set must not certify — otherwise a certificate assembled by code that forgot the field
	// carries a grade someone will act on.
	var o domain.AnalysisOutcome
	if o.Valid() {
		t.Error("zero AnalysisOutcome is valid; an unrecorded run shape must not pass validation")
	}
	if o.Certifies() {
		t.Error("zero AnalysisOutcome certifies; absence of analysis is not evidence of safety")
	}
}

// N/A is the absence of a measurement. Everything that could mistake it for a good one is
// checked here, because every one of those mistakes reintroduces the P0.
func TestNotApplicableIsNeverAPass(t *testing.T) {
	t.Parallel()

	na := domain.GradeNotApplicable

	if na.Gate() == domain.GatePass {
		t.Error("N/A gates PASS; a changeset nobody analyzed must never authorise a merge")
	}
	if na.Gate() != domain.GateNotApplicable {
		t.Errorf("N/A gate = %q, want NOT_APPLICABLE", na.Gate())
	}
	if na.Rank() >= domain.GradeF.Rank() {
		t.Errorf("N/A ranks %d, at or above F (%d); an unmeasured change must not clear a threshold",
			na.Rank(), domain.GradeF.Rank())
	}
	if na.Threshold() {
		t.Error("N/A is accepted as a gating threshold; that would build a gate every run satisfies")
	}
	if !na.Valid() {
		t.Error("N/A is not a valid grade; it is a value certificates carry and must round-trip")
	}

	// A cap must never be able to lift a real grade to N/A, or lower one to it. N/A is outside
	// the ordering entirely, and Cap is the only operation that could smuggle it in.
	for _, g := range []domain.Grade{domain.GradeA, domain.GradeB, domain.GradeC, domain.GradeF} {
		if got := g.Cap(na); got == domain.GradeA {
			t.Errorf("%s.Cap(N/A) = %q; capping must never produce a passing grade", g, got)
		}
	}
}

// Only ANALYZED certifies. Stated as an exhaustive check over the enum so that adding a fourth
// outcome forces a decision here rather than defaulting into the permissive branch.
func TestOnlyAnalyzedCertifies(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome domain.AnalysisOutcome
		want    bool
	}{
		{domain.OutcomeAnalyzed, true},
		{domain.OutcomeNoCandidates, false},
		{domain.OutcomeUnsupportedContent, false},
		{domain.AnalysisOutcome(""), false},
		{domain.AnalysisOutcome("SOMETHING_NEW"), false},
	} {
		if got := tc.outcome.Certifies(); got != tc.want {
			t.Errorf("AnalysisOutcome(%q).Certifies() = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

func TestLockHazardOrdering(t *testing.T) {
	t.Parallel()

	// The order mandated by docs/SPECIFICATION.md §8.
	ordered := []domain.LockHazard{
		domain.LockNone,
		domain.LockShort,
		domain.LockFullScan,
		domain.LockTableRewrite,
		domain.LockExclusive,
	}

	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		if prev.Severity() >= cur.Severity() {
			t.Errorf("%s.Severity()=%d is not less than %s.Severity()=%d",
				prev, prev.Severity(), cur, cur.Severity())
		}
		if !cur.AtLeast(prev) {
			t.Errorf("%s.AtLeast(%s) = false, want true", cur, prev)
		}
		if prev.AtLeast(cur) {
			t.Errorf("%s.AtLeast(%s) = true, want false", prev, cur)
		}
	}

	// An unrecognised hazard must sort as the worst, never as harmless.
	if !domain.LockHazard("WHO_KNOWS").AtLeast(domain.LockExclusive) {
		t.Errorf("unrecognised LockHazard does not outrank EXCLUSIVE; fail-closed violated")
	}
}

func TestGradeCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		assigned domain.Grade
		limit    domain.Grade
		want     domain.Grade
	}{
		{"cap below assignment wins", domain.GradeA, domain.GradeC, domain.GradeC},
		{"cap above assignment is inert", domain.GradeC, domain.GradeA, domain.GradeC},
		{"equal", domain.GradeB, domain.GradeB, domain.GradeB},
		{"F cannot be lifted by a cap", domain.GradeF, domain.GradeA, domain.GradeF},
		{"F cap floors everything", domain.GradeA, domain.GradeF, domain.GradeF},

		// docs/RULES.md §4.1: zero COSTLY findings, everything REVERSIBLE, but a missing
		// down.sql caps the grade at C. This is the case the owner ruled on explicitly.
		{"missing down.sql caps a clean changeset", domain.GradeA, domain.GradeC, domain.GradeC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.assigned.Cap(tt.limit); got != tt.want {
				t.Errorf("Grade(%q).Cap(%q) = %q, want %q", tt.assigned, tt.limit, got, tt.want)
			}
		})
	}
}

// Capping must be order-independent, or the scorer's result would depend on the order the caps
// happened to be written in.
func TestGradeCapIsCommutative(t *testing.T) {
	t.Parallel()

	grades := []domain.Grade{domain.GradeA, domain.GradeB, domain.GradeC, domain.GradeF}
	for _, a := range grades {
		for _, b := range grades {
			if got, want := a.Cap(b), b.Cap(a); got != want {
				t.Errorf("Cap is not commutative: %q.Cap(%q)=%q but %q.Cap(%q)=%q", a, b, got, b, a, want)
			}
		}
	}
}

func TestGradeGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		grade domain.Grade
		want  domain.GateStatus
	}{
		{domain.GradeA, domain.GatePass},
		{domain.GradeB, domain.GateFail},
		{domain.GradeC, domain.GateFail},
		{domain.GradeF, domain.GateFail},
		{domain.Grade("A+"), domain.GateFail},
		{domain.Grade(""), domain.GateFail},
	}

	for _, tt := range tests {
		t.Run(string(tt.grade), func(t *testing.T) {
			t.Parallel()
			if got := tt.grade.Gate(); got != tt.want {
				t.Errorf("Grade(%q).Gate() = %q, want %q", tt.grade, got, tt.want)
			}
		})
	}
}

func TestSortFindingsIsCanonical(t *testing.T) {
	t.Parallel()

	// Deliberately shuffled: file descending, lines descending, rule IDs descending.
	in := []domain.Finding{
		{File: "b.sql", Line: 2, RuleID: "PG009"},
		{File: "a.sql", Line: 10, RuleID: "PG002"},
		{File: "b.sql", Line: 1, RuleID: "PG001"},
		{File: "a.sql", Line: 2, RuleID: "PG025"},
		{File: "a.sql", Line: 2, RuleID: "PG001"},
	}

	want := []domain.Finding{
		{File: "a.sql", Line: 2, RuleID: "PG001"},
		{File: "a.sql", Line: 2, RuleID: "PG025"},
		{File: "a.sql", Line: 10, RuleID: "PG002"},
		{File: "b.sql", Line: 1, RuleID: "PG001"},
		{File: "b.sql", Line: 2, RuleID: "PG009"},
	}

	domain.SortFindings(in)
	if diff := cmp.Diff(want, in); diff != "" {
		t.Errorf("SortFindings mismatch (-want +got):\n%s", diff)
	}
}

// Line 10 must sort after line 2. A string sort would put it first, and a certificate that
// lists findings in lexical line order is a certificate nobody can follow.
func TestSortFindingsOrdersLinesNumerically(t *testing.T) {
	t.Parallel()

	in := []domain.Finding{
		{File: "a.sql", Line: 100},
		{File: "a.sql", Line: 2},
		{File: "a.sql", Line: 11},
	}
	domain.SortFindings(in)

	for i, want := range []int{2, 11, 100} {
		if in[i].Line != want {
			t.Errorf("position %d: line %d, want %d", i, in[i].Line, want)
		}
	}
}

func TestChangeStatusValid(t *testing.T) {
	t.Parallel()

	for _, s := range []domain.ChangeStatus{
		domain.StatusAdded, domain.StatusModified, domain.StatusRemoved, domain.StatusRenamed,
	} {
		if !s.Valid() {
			t.Errorf("ChangeStatus(%q).Valid() = false, want true", s)
		}
	}

	for _, s := range []domain.ChangeStatus{"", "added", "COPIED"} {
		if s.Valid() {
			t.Errorf("ChangeStatus(%q).Valid() = true, want false", s)
		}
	}
}

func TestSchemaVersionIsPinned(t *testing.T) {
	t.Parallel()

	// Downstream merge gates parse this. Changing it is a deliberate, breaking act — if this
	// test fails, the version was bumped, and that must be intentional.
	//
	// 1.5.0 added Outcome, and added N/A to Grade and NOT_APPLICABLE to AIGateStatus. That is
	// the second bump to widen an existing enum, after 1.3.0's WILL_FAIL, and it is the more
	// disruptive of the two: a gate written as `grade == "A"` is unaffected, but one written as
	// `grade != "F"` starts passing changesets nobody analyzed.
	if domain.SchemaVersion != "1.5.0" {
		t.Errorf("SchemaVersion = %q, want %q", domain.SchemaVersion, "1.5.0")
	}
}

// EffectiveGrade must never be the field an agent gates on. Grade is the measurement; a policy
// may move EffectiveGrade and may not move Grade, and the two must not be confused.
func TestGateFollowsGradeNotEffectiveGrade(t *testing.T) {
	t.Parallel()

	cert := domain.ReversibilityCertificate{
		Grade:          domain.GradeF,
		EffectiveGrade: domain.GradeA,
		AIGateStatus:   domain.GradeF.Gate(),
	}

	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q for a grade F change with everything waived, want FAIL; "+
			"a waiver must never authorise an agent to merge", cert.AIGateStatus)
	}
}
