package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/abdo-s1/reversibility-engine/internal/domain"
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
}

func TestLockHazardOrdering(t *testing.T) {
	t.Parallel()

	// The order mandated by CLAUDE.md §8.
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

		// CLAUDE.md §15.1: zero COSTLY findings, everything REVERSIBLE, but a missing
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
	if domain.SchemaVersion != "1.0.0" {
		t.Errorf("SchemaVersion = %q, want %q", domain.SchemaVersion, "1.0.0")
	}
}
