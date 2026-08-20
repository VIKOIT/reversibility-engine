// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"fmt"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// This file is the executable form of the authoritative scoring rules in docs/RULES.md §3:
//
//	Any IRREVERSIBLE   -> F
//	Any UNKNOWN        -> F        (fail-closed, no exceptions)
//	Any analyzer error -> F        (never degrade to a passing grade)
//
//	Otherwise:
//	  missing or unparseable down.sql            -> cap at C
//	  >= 3 COSTLY findings                       -> C
//	  1-2 COSTLY findings                        -> B
//	  LockHazard >= TABLE_REWRITE present        -> cap at B
//	  all REVERSIBLE, lock <= SHORT, down.sql ok -> A
//
// Per the owner's ruling in §15.1 a cap overrides an assignment: a grade is assigned, then every
// active cap is applied, and the worst result wins.

// scoreInput is everything the grade depends on.
type scoreInput struct {
	findings       []domain.Finding
	downMigrations []domain.DownMigrationStatus

	// analyzerErrors are failures to complete analysis. They are not findings, and they are not
	// survivable: an incomplete analysis cannot certify anything.
	analyzerErrors []string

	// applicable is false when no analyzer claimed any file in the changeset.
	applicable bool
}

// score computes the grade and, when the grade is F, the human-readable reasons for it.
func score(in scoreInput) (domain.Grade, []string) {
	// An analysis that did not finish cannot certify anything. This is checked before the
	// findings, because a partial finding list is not evidence of safety.
	if len(in.analyzerErrors) > 0 {
		blockers := make([]string, 0, len(in.analyzerErrors))
		for _, e := range in.analyzerErrors {
			blockers = append(blockers, "analysis did not complete: "+e)
		}
		sort.Strings(blockers)
		return domain.GradeF, blockers
	}

	if blockers := blockingFindings(in.findings); len(blockers) > 0 {
		return domain.GradeF, blockers
	}

	// An empty changeset has nothing to say. Inventing an opinion would train users to ignore
	// the gate; per docs/RULES.md §3 it grades A with Applicable false.
	if !in.applicable {
		return domain.GradeA, nil
	}

	costly := 0
	allReversible := true
	worstLock := domain.LockNone

	for _, f := range in.findings {
		if f.Reversibility == domain.ReversibilityCostly {
			costly++
		}
		if f.Reversibility != domain.ReversibilityReversible {
			allReversible = false
		}
		if f.LockHazard.AtLeast(worstLock) {
			worstLock = f.LockHazard
		}
	}

	downOK := downMigrationsAreSound(in.downMigrations)

	// Assignment.
	grade := domain.GradeA
	switch {
	case costly >= 3:
		grade = domain.GradeC
	case costly >= 1:
		grade = domain.GradeB
	}

	// Caps, each applied unconditionally so that order cannot matter.
	if !downOK {
		grade = grade.Cap(domain.GradeC)
	}
	if worstLock.AtLeast(domain.LockTableRewrite) {
		grade = grade.Cap(domain.GradeB)
	}

	// The A row states the conditions for A, so failing any of them means the grade is not A.
	// B is the highest grade below A, which makes this the least punitive reading consistent
	// with the table. It matters for FULL_SCAN, which fails the "lock <= SHORT" condition but
	// does not reach the TABLE_REWRITE cap above.
	if !allReversible || worstLock.AtLeast(domain.LockFullScan) || !downOK {
		grade = grade.Cap(domain.GradeB)
	}

	return grade, nil
}

// blockingFindings returns the reasons any finding forces grade F.
//
// IRREVERSIBLE and UNKNOWN are reported separately because they are different failures: one is
// a change that destroys something, the other is a change nobody understood. Both fail.
func blockingFindings(findings []domain.Finding) []string {
	var blockers []string

	for _, f := range findings {
		// A verdict the domain does not recognise is not a mild finding to be averaged in. It
		// means an analyzer produced something incoherent — a zero value, a typo, a corrupted
		// struct — and a classification nobody can read is exactly what UNKNOWN is for. Without
		// this, an empty Reversibility would merely fail the "all REVERSIBLE" test and cap the
		// grade at B, quietly turning a broken analyzer into a passing-ish result.
		if !f.Reversibility.Valid() {
			blockers = append(blockers, fmt.Sprintf("%s at %s: produced an unrecognised verdict %q",
				f.RuleID, location(f), f.Reversibility))
			continue
		}

		switch f.Reversibility {
		case domain.ReversibilityIrreversible:
			blockers = append(blockers, fmt.Sprintf("%s at %s: irreversible — %s", f.RuleID, location(f), f.Rationale))
		case domain.ReversibilityUnknown:
			blockers = append(blockers, fmt.Sprintf("%s at %s: unknown — %s", f.RuleID, location(f), f.Rationale))
		}
	}

	// Findings arrive sorted, so blockers derived from them are already stable; sorting again
	// costs nothing and removes any dependence on that assumption holding in future.
	sort.Strings(blockers)
	return blockers
}

// downMigrationsAreSound reports whether every migration has a down migration that exists and
// parses.
//
// Symmetry — level 3 — is deliberately excluded. docs/RULES.md §1 marks it advisory and forbids it
// from producing grade F on its own, and a migration can be perfectly reversible without textual
// symmetry: a data backfill has nothing to create or drop.
func downMigrationsAreSound(statuses []domain.DownMigrationStatus) bool {
	for _, s := range statuses {
		if !s.Exists || !s.Parses {
			return false
		}
	}
	return true
}

func location(f domain.Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}
