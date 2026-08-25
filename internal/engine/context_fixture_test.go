// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"os"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// The context fixture group pins what a production snapshot does to a verdict, as data.
//
// Each fixture carries a changeset, a snapshot, and the two grades it has with and without that
// snapshot. Writing both down is what makes the direction of the rule checkable rather than
// merely stated: context may lower a grade and may never raise one.
func TestContextFixtures(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	cases, err := fixture.Cases(root, "context")
	if err != nil {
		t.Fatalf("loading context fixtures: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the context fixture group is empty")
	}

	files := provider.NewFake(root)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			path := fixture.ContextPath(root, "context", tc.Name)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("a context fixture with no %s proves nothing: %v", fixture.ContextFile, err)
			}

			set, err := snapshot.Load([]string{path}, snapshot.Options{Now: contextDay})
			if err != nil {
				t.Fatalf("loading the fixture's snapshot: %v", err)
			}

			changed, err := files.ChangedFiles(context.Background(), tc.Ref)
			if err != nil {
				t.Fatalf("reading the fixture: %v", err)
			}

			analyzers := []analyzer.Analyzer{postgres.New(), kubernetes.New()}

			withContext, err := engine.New(analyzers, engine.WithContext(set)).
				Certify(context.Background(), changed)
			if err != nil {
				t.Fatalf("Certify with context: %v", err)
			}

			without, err := engine.New(analyzers).Certify(context.Background(), changed)
			if err != nil {
				t.Fatalf("Certify without context: %v", err)
			}

			if tc.Expect.Grade != "" && withContext.Grade != tc.Expect.Grade {
				t.Errorf("grade with context = %q, want %q. Blockers: %v",
					withContext.Grade, tc.Expect.Grade, withContext.Blockers)
			}
			if tc.Expect.GradeWithoutContext != "" && without.Grade != tc.Expect.GradeWithoutContext {
				t.Errorf("grade without context = %q, want %q",
					without.Grade, tc.Expect.GradeWithoutContext)
			}

			// The rule itself, checked against the fixture's own two grades rather than only
			// against the engine's behaviour.
			if withContext.Grade.Rank() > without.Grade.Rank() {
				t.Errorf("this fixture claims context RAISES the grade from %q to %q; that is the one thing it may never do",
					without.Grade, withContext.Grade)
			}

			assertFindings(t, tc, withContext.Findings)
		})
	}
}

// assertFindings compares the certificate against the fixture's expectations, including the band.
func assertFindings(t *testing.T, tc fixture.Case, got []domain.Finding) {
	t.Helper()

	if len(got) != len(tc.Expect.Findings) {
		t.Fatalf("got %d findings, want %d: %+v", len(got), len(tc.Expect.Findings), got)
	}

	for i, want := range tc.Expect.Findings {
		g := got[i]

		if g.RuleID != want.RuleID {
			t.Errorf("finding %d: RuleID = %q, want %q", i, g.RuleID, want.RuleID)
		}
		if g.File != want.File {
			t.Errorf("finding %d: File = %q, want %q", i, g.File, want.File)
		}
		if g.Line != want.Line {
			t.Errorf("finding %d: Line = %d, want %d", i, g.Line, want.Line)
		}
		if g.Reversibility != want.Reversibility {
			t.Errorf("finding %d (%s): Reversibility = %q, want %q", i, g.RuleID, g.Reversibility, want.Reversibility)
		}
		if g.LockHazard != want.LockHazard {
			t.Errorf("finding %d (%s): LockHazard = %q, want %q", i, g.RuleID, g.LockHazard, want.LockHazard)
		}

		band := domain.LockDurationBand("")
		if g.Context != nil {
			band = g.Context.LockDurationBand
		}
		if band != want.LockDurationBand {
			t.Errorf("finding %d (%s): LockDurationBand = %q, want %q", i, g.RuleID, band, want.LockDurationBand)
		}

		if hasUndo := g.UndoStep != ""; hasUndo != want.WantUndoStep {
			t.Errorf("finding %d (%s): undo step present = %v, want %v", i, g.RuleID, hasUndo, want.WantUndoStep)
		}
	}
}
