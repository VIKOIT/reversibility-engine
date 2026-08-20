package engine

import (
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// finding builds a finding with just the fields scoring reads.
func finding(rev domain.Reversibility, lock domain.LockHazard) domain.Finding {
	return domain.Finding{
		RuleID:        "TEST",
		File:          "f.sql",
		Line:          1,
		Statement:     "SELECT 1",
		Reversibility: rev,
		LockHazard:    lock,
		Rationale:     "a rationale long enough to be a sentence",
	}
}

func repeat(f domain.Finding, n int) []domain.Finding {
	out := make([]domain.Finding, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, f)
	}
	return out
}

func downOK(migration string) domain.DownMigrationStatus {
	return domain.DownMigrationStatus{Migration: migration, Exists: true, Parses: true, Symmetric: true}
}

// The authoritative scoring table from CLAUDE.md §11, case by case.
func TestScore(t *testing.T) {
	t.Parallel()

	reversible := finding(domain.ReversibilityReversible, domain.LockNone)
	costly := finding(domain.ReversibilityCostly, domain.LockShort)

	tests := []struct {
		name string
		in   scoreInput
		want domain.Grade
	}{
		{
			name: "all reversible, no locks, down ok",
			in:   scoreInput{findings: repeat(reversible, 3), downMigrations: []domain.DownMigrationStatus{downOK("1")}, applicable: true},
			want: domain.GradeA,
		},
		{
			name: "reversible with a SHORT lock still reaches A",
			in:   scoreInput{findings: []domain.Finding{finding(domain.ReversibilityReversible, domain.LockShort)}, applicable: true},
			want: domain.GradeA,
		},
		{
			name: "one costly",
			in:   scoreInput{findings: repeat(costly, 1), applicable: true},
			want: domain.GradeB,
		},
		{
			name: "two costly",
			in:   scoreInput{findings: repeat(costly, 2), applicable: true},
			want: domain.GradeB,
		},
		{
			name: "three costly",
			in:   scoreInput{findings: repeat(costly, 3), applicable: true},
			want: domain.GradeC,
		},
		{
			name: "ten costly is still C",
			in:   scoreInput{findings: repeat(costly, 10), applicable: true},
			want: domain.GradeC,
		},
		{
			name: "any irreversible",
			in:   scoreInput{findings: append(repeat(reversible, 5), finding(domain.ReversibilityIrreversible, domain.LockExclusive)), applicable: true},
			want: domain.GradeF,
		},
		{
			name: "any unknown",
			in:   scoreInput{findings: append(repeat(reversible, 5), finding(domain.ReversibilityUnknown, domain.LockExclusive)), applicable: true},
			want: domain.GradeF,
		},
		{
			name: "analyzer error outranks a clean finding list",
			in:   scoreInput{findings: repeat(reversible, 3), analyzerErrors: []string{"parser unavailable"}, applicable: true},
			want: domain.GradeF,
		},

		// CLAUDE.md §15.1, the owner's ruling: a cap overrides an assignment.
		{
			name: "missing down.sql caps an otherwise perfect changeset at C",
			in: scoreInput{
				findings:       repeat(reversible, 3),
				downMigrations: []domain.DownMigrationStatus{{Migration: "1", Exists: false}},
				applicable:     true,
			},
			want: domain.GradeC,
		},
		{
			name: "unparseable down.sql caps at C",
			in: scoreInput{
				findings:       repeat(reversible, 3),
				downMigrations: []domain.DownMigrationStatus{{Migration: "1", Exists: true, Parses: false}},
				applicable:     true,
			},
			want: domain.GradeC,
		},
		{
			name: "missing down.sql cannot lift a C assignment",
			in: scoreInput{
				findings:       repeat(costly, 5),
				downMigrations: []domain.DownMigrationStatus{{Migration: "1", Exists: false}},
				applicable:     true,
			},
			want: domain.GradeC,
		},

		// Level 3 is advisory and must never move a grade on its own.
		{
			name: "asymmetric down.sql alone does not lower the grade",
			in: scoreInput{
				findings:       repeat(reversible, 3),
				downMigrations: []domain.DownMigrationStatus{{Migration: "1", Exists: true, Parses: true, Symmetric: false}},
				applicable:     true,
			},
			want: domain.GradeA,
		},

		{
			name: "table rewrite caps at B",
			in:   scoreInput{findings: []domain.Finding{finding(domain.ReversibilityReversible, domain.LockTableRewrite)}, applicable: true},
			want: domain.GradeB,
		},
		{
			name: "exclusive lock caps at B",
			in:   scoreInput{findings: []domain.Finding{finding(domain.ReversibilityReversible, domain.LockExclusive)}, applicable: true},
			want: domain.GradeB,
		},

		// FULL_SCAN fails the A row's "lock <= SHORT" condition without reaching the
		// TABLE_REWRITE cap. B is the highest grade below A.
		{
			name: "full scan fails the A condition and caps at B",
			in:   scoreInput{findings: []domain.Finding{finding(domain.ReversibilityReversible, domain.LockFullScan)}, applicable: true},
			want: domain.GradeB,
		},
		{
			name: "table rewrite cannot lift a C assignment",
			in:   scoreInput{findings: append(repeat(costly, 3), finding(domain.ReversibilityReversible, domain.LockTableRewrite)), applicable: true},
			want: domain.GradeC,
		},

		{
			name: "empty changeset grades A",
			in:   scoreInput{applicable: false},
			want: domain.GradeA,
		},
		{
			name: "an inapplicable changeset with a stale finding still fails closed",
			in:   scoreInput{findings: []domain.Finding{finding(domain.ReversibilityIrreversible, domain.LockExclusive)}, applicable: false},
			want: domain.GradeF,
		},
		{
			name: "applicable changeset with no findings grades A",
			in:   scoreInput{applicable: true},
			want: domain.GradeA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, blockers := score(tt.in)
			if got != tt.want {
				t.Errorf("score() = %q, want %q", got, tt.want)
			}

			// Blockers exist to explain an F. Any other grade must not carry them, and an F
			// must never be unexplained.
			if got == domain.GradeF && len(blockers) == 0 {
				t.Errorf("grade F with no blockers; an unexplained failure is not actionable")
			}
			if got != domain.GradeF && len(blockers) != 0 {
				t.Errorf("grade %q carries blockers %v; blockers are reasons for F only", got, blockers)
			}
		})
	}
}

// The gate is PASS if and only if the grade is A.
func TestScoreGateAgreesWithGrade(t *testing.T) {
	t.Parallel()

	inputs := []scoreInput{
		{applicable: false},
		{findings: repeat(finding(domain.ReversibilityReversible, domain.LockNone), 2), applicable: true},
		{findings: repeat(finding(domain.ReversibilityCostly, domain.LockShort), 1), applicable: true},
		{findings: repeat(finding(domain.ReversibilityCostly, domain.LockShort), 4), applicable: true},
		{findings: repeat(finding(domain.ReversibilityIrreversible, domain.LockExclusive), 1), applicable: true},
		{findings: repeat(finding(domain.ReversibilityUnknown, domain.LockExclusive), 1), applicable: true},
		{analyzerErrors: []string{"boom"}, applicable: true},
	}

	for _, in := range inputs {
		grade, _ := score(in)

		wantPass := grade == domain.GradeA
		if got := grade.Gate() == domain.GatePass; got != wantPass {
			t.Errorf("grade %q gates %q", grade, grade.Gate())
		}
	}
}

// An analyzer failure must outrank everything, including a findings list that looks perfect.
// This is the path where a broken toolchain could otherwise certify a dangerous change.
func TestAnalyzerErrorAlwaysFails(t *testing.T) {
	t.Parallel()

	grade, blockers := score(scoreInput{
		findings:       repeat(finding(domain.ReversibilityReversible, domain.LockNone), 20),
		downMigrations: []domain.DownMigrationStatus{downOK("1")},
		analyzerErrors: []string{"sql parser unavailable"},
		applicable:     true,
	})

	if grade != domain.GradeF {
		t.Fatalf("grade = %q, want F", grade)
	}
	if len(blockers) != 1 || !strings.Contains(blockers[0], "sql parser unavailable") {
		t.Errorf("blockers = %v, want the analyzer error named", blockers)
	}
}

func TestDownMigrationsAreSound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []domain.DownMigrationStatus
		want bool
	}{
		{"no migrations at all", nil, true},
		{"all sound", []domain.DownMigrationStatus{downOK("1"), downOK("2")}, true},
		{"one missing", []domain.DownMigrationStatus{downOK("1"), {Migration: "2"}}, false},
		{"one unparseable", []domain.DownMigrationStatus{{Migration: "1", Exists: true, Parses: false}}, false},

		// Level 3 is advisory: asymmetry alone must not make a changeset unsound.
		{"asymmetric only", []domain.DownMigrationStatus{{Migration: "1", Exists: true, Parses: true, Symmetric: false}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := downMigrationsAreSound(tt.in); got != tt.want {
				t.Errorf("downMigrationsAreSound() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Blockers reach the certificate, so their order must not depend on the order findings arrived.
func TestBlockersAreSorted(t *testing.T) {
	t.Parallel()

	findings := []domain.Finding{
		{RuleID: "PG003", File: "c.sql", Line: 1, Reversibility: domain.ReversibilityIrreversible},
		{RuleID: "PG001", File: "a.sql", Line: 1, Reversibility: domain.ReversibilityIrreversible},
		{RuleID: "PG002", File: "b.sql", Line: 1, Reversibility: domain.ReversibilityUnknown},
	}

	_, blockers := score(scoreInput{findings: findings, applicable: true})
	if len(blockers) != 3 {
		t.Fatalf("got %d blockers, want 3", len(blockers))
	}
	for i := 1; i < len(blockers); i++ {
		if blockers[i-1] > blockers[i] {
			t.Errorf("blockers are not sorted: %v", blockers)
		}
	}
}
