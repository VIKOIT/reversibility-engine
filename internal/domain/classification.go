// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

// Reversibility is the verdict on whether a single change can be undone.
type Reversibility string

// The complete set of reversibility verdicts. There is deliberately no "probably fine".
const (
	// ReversibilityReversible means the change can be undone with no data loss.
	ReversibilityReversible Reversibility = "REVERSIBLE"

	// ReversibilityCostly means the change can be undone, but the undo is expensive, slow, or
	// only correct within a window — for example a rename that breaks the previous application
	// version while it is in effect.
	ReversibilityCostly Reversibility = "COSTLY"

	// ReversibilityIrreversible means undoing the change cannot restore the prior state.
	ReversibilityIrreversible Reversibility = "IRREVERSIBLE"

	// ReversibilityUnknown means the engine could not determine the verdict. It is treated as
	// unsafe, never as safe.
	ReversibilityUnknown Reversibility = "UNKNOWN"

	// ReversibilityWillFail means the change will not apply at all: production state has been
	// checked and the statement is certain to abort.
	//
	// It is a different failure from IRREVERSIBLE and is reported as one. IRREVERSIBLE means
	// "you cannot undo this". WILL_FAIL means "this will not even run" — there is nothing to
	// undo because nothing will have happened, and the fix is to the migration rather than to
	// the rollback plan.
	//
	// It is only ever reached from evidence, never from a guess: today that means a production
	// snapshot showing nulls in a column a migration is about to constrain.
	ReversibilityWillFail Reversibility = "WILL_FAIL"
)

// Valid reports whether r is one of the defined verdicts.
//
// The zero value is not valid: a Finding that was never classified must not pass for
// REVERSIBLE simply because nobody set the field.
func (r Reversibility) Valid() bool {
	switch r {
	case ReversibilityReversible, ReversibilityCostly, ReversibilityIrreversible,
		ReversibilityUnknown, ReversibilityWillFail:
		return true
	default:
		return false
	}
}

// Severity orders verdicts for rule precedence only — when several rules match one statement,
// the most severe wins.
//
// This is not the scoring order. Scoring treats IRREVERSIBLE and UNKNOWN identically (both F);
// the split here exists only so precedence is total and deterministic.
func (r Reversibility) Severity() int {
	switch r {
	case ReversibilityReversible:
		return 0
	case ReversibilityCostly:
		return 1
	case ReversibilityUnknown:
		return 2
	case ReversibilityIrreversible:
		return 3
	case ReversibilityWillFail:
		// Above IRREVERSIBLE. A change that cannot be undone is a risk to weigh; a change that
		// will not apply is a defect, and when both describe one statement the defect is the
		// thing to say first.
		return 4
	default:
		// An unset or corrupt verdict outranks everything, so it can never be silently
		// discarded in favour of a milder one.
		return 5
	}
}

// LockHazard describes the locking cost of applying a change to a live database.
type LockHazard string

// The complete set of lock hazards, in increasing severity.
const (
	// LockNone takes no blocking lock.
	LockNone LockHazard = "NONE"

	// LockShort takes a brief exclusive lock on catalog rows only.
	LockShort LockHazard = "SHORT"

	// LockFullScan holds a lock while every row is validated.
	LockFullScan LockHazard = "FULL_SCAN"

	// LockTableRewrite holds a lock while the entire table is rewritten.
	LockTableRewrite LockHazard = "TABLE_REWRITE"

	// LockExclusive blocks all concurrent access to the object.
	LockExclusive LockHazard = "EXCLUSIVE"
)

// Valid reports whether l is one of the defined hazards.
func (l LockHazard) Valid() bool {
	switch l {
	case LockNone, LockShort, LockFullScan, LockTableRewrite, LockExclusive:
		return true
	default:
		return false
	}
}

// Severity orders hazards so the scoring rules can express "LockHazard >= TABLE_REWRITE" and
// "lock <= SHORT". The order is NONE < SHORT < FULL_SCAN < TABLE_REWRITE < EXCLUSIVE.
func (l LockHazard) Severity() int {
	switch l {
	case LockNone:
		return 0
	case LockShort:
		return 1
	case LockFullScan:
		return 2
	case LockTableRewrite:
		return 3
	case LockExclusive:
		return 4
	default:
		// Unset or corrupt hazards sort as the worst possible, per fail-closed.
		return 5
	}
}

// AtLeast reports whether l is at least as severe as other.
func (l LockHazard) AtLeast(other LockHazard) bool { return l.Severity() >= other.Severity() }

// Grade is the overall verdict on a changeset.
type Grade string

// The complete set of grades. F means a rollback would lose data, hit an unknown construct, or
// depend on an analysis that failed.
const (
	GradeA Grade = "A"
	GradeB Grade = "B"
	GradeC Grade = "C"
	GradeF Grade = "F"
)

// Valid reports whether g is one of the defined grades.
func (g Grade) Valid() bool {
	switch g {
	case GradeA, GradeB, GradeC, GradeF:
		return true
	default:
		return false
	}
}

// Rank orders grades from worst to best, so that A > B > C > F.
func (g Grade) Rank() int {
	switch g {
	case GradeF:
		return 0
	case GradeC:
		return 1
	case GradeB:
		return 2
	case GradeA:
		return 3
	default:
		// An unset grade is the worst grade. Nothing may escape scoring by being empty.
		return -1
	}
}

// Cap applies a ceiling, returning whichever of g and limit is worse.
//
// Per the owner's ruling in docs/RULES.md §4, a cap overrides an assignment rather than competing
// with it: a changeset with no COSTLY findings but a missing down migration is capped to C.
func (g Grade) Cap(limit Grade) Grade {
	if limit.Rank() < g.Rank() {
		return limit
	}
	return g
}

// GateStatus is the merge-gate verdict handed to autonomous agents.
type GateStatus string

// The complete set of gate statuses.
const (
	GatePass GateStatus = "PASS"
	GateFail GateStatus = "FAIL"
)

// Gate returns PASS if and only if the grade is A, per docs/RULES.md §3.
//
// This is the single definition of the gate. No caller may re-derive it, because a second
// definition is a second chance to get it wrong in the permissive direction.
func (g Grade) Gate() GateStatus {
	if g == GradeA {
		return GatePass
	}
	return GateFail
}
