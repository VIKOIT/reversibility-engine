// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"fmt"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// noCompleteUndo heads the plan when the changeset cannot be fully reversed.
//
// It is written as a comment so that the plan stays a pasteable script: an operator who copies
// the whole thing under pressure runs the steps that exist and reads the warning, rather than
// hitting a syntax error and losing both.
const noCompleteUndo = "-- NO COMPLETE UNDO EXISTS. This changeset cannot be fully reversed."

// buildUndoPlan assembles the rollback script from the undo steps of the findings.
//
// Steps come back in reverse order of application, because undoing a sequence means unwinding
// it: the last change applied is the first that has to come off.
//
// Per docs/RULES.md §3, if any finding is IRREVERSIBLE the plan is replaced by an explicit
// statement that no complete undo exists, listing what cannot be undone. UNKNOWN findings
// trigger the same replacement — see the note below.
func buildUndoPlan(findings []domain.Finding) []domain.UndoStep {
	if blockers := unreversibleFindings(findings); len(blockers) > 0 {
		return blockers
	}

	// Findings arrive sorted in application order, so the plan walks them backwards.
	plan := make([]domain.UndoStep, 0, len(findings))
	for i := len(findings) - 1; i >= 0; i-- {
		if step := findings[i].UndoStep; step != "" {
			plan = append(plan, step)
		}
	}
	return plan
}

// unreversibleFindings returns the replacement plan when a complete undo does not exist, or nil
// when it does.
//
// UNKNOWN is treated the same as IRREVERSIBLE here, which extends the letter of §11. The reason
// is §2: an UNKNOWN finding is a change nobody understood, so a plan that lists steps for
// everything else claims a completeness it does not have. Emitting a confident-looking script
// beside an unclassified change is exactly the wrong-safe-verdict failure this product exists to
// prevent. Recorded as an open question in docs/SPECIFICATION.md §16.6.
func unreversibleFindings(findings []domain.Finding) []domain.UndoStep {
	var blocked []domain.UndoStep

	for _, f := range findings {
		var why string
		switch f.Reversibility {
		case domain.ReversibilityIrreversible:
			why = "cannot be undone"
		case domain.ReversibilityUnknown:
			why = "was not understood, so no undo can be written for it"
		case domain.ReversibilityWillFail:
			// A statement that aborts rolls its transaction back, so nothing it did survives to
			// be undone — and neither does anything else in the same migration. Printing a
			// confident rollback script beside a migration that will not apply would describe a
			// state the database is never going to be in.
			why = "will not apply, so there is nothing to undo; fix the migration instead"
		default:
			continue
		}

		blocked = append(blocked, domain.UndoStep(
			fmt.Sprintf("--   %s at %s: %s — %s", f.RuleID, location(f), statementOf(f), why),
		))
	}

	if len(blocked) == 0 {
		return nil
	}

	plan := make([]domain.UndoStep, 0, len(blocked)+2)
	plan = append(plan, noCompleteUndo)
	plan = append(plan, "-- The following changes have no reverse:")
	return append(plan, blocked...)
}

func statementOf(f domain.Finding) string {
	if f.Statement == "" {
		return "(statement unavailable)"
	}
	return f.Statement
}
