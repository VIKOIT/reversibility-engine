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

	// GradeNotApplicable means the engine did not analyze this changeset, so it has no
	// measurement to report. It is not a good grade and it is not a bad one: it is the absence
	// of one.
	//
	// It exists because A used to carry two meanings — "analyzed and found reversible" and
	// "nothing here was analyzed" — and every reader, merge bot, and branch protection rule
	// took the first. A grade that can say "no answer" is what stops absence of analysis from
	// reading as evidence of safety. See docs/SPECIFICATION.md §2 and docs/RULES.md §3.
	GradeNotApplicable Grade = "N/A"
)

// Valid reports whether g is one of the defined grades.
//
// N/A is included: it is a value a certificate may legitimately carry. It is deliberately NOT
// a value a --min-grade threshold may carry, and that is a separate question — see Threshold.
func (g Grade) Valid() bool {
	switch g {
	case GradeA, GradeB, GradeC, GradeF, GradeNotApplicable:
		return true
	default:
		return false
	}
}

// Threshold reports whether g may be used as a gating minimum.
//
// Only the four measured grades can. "At least N/A" is not a comparison anyone can mean: N/A is
// the absence of a measurement, so nothing is above or below it, and accepting it as a
// threshold would create a gate that every run satisfies.
func (g Grade) Threshold() bool {
	switch g {
	case GradeA, GradeB, GradeC, GradeF:
		return true
	default:
		return false
	}
}

// Rank orders grades from worst to best, so that A > B > C > F.
//
// N/A has no rank. It returns the same value an unset grade does — below F — so that a caller
// who compares it against a threshold without first branching on the outcome fails closed. The
// branch that gives N/A its real meaning lives in exactly one place, the CLI's applyGate, and
// it decides the exit code from AnalysisOutcome rather than from this number.
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
		// An unset grade — and N/A, which is not a measurement — is the worst grade. Nothing
		// may escape scoring by being empty or by being unmeasured.
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

	// GateNotApplicable means there is no gate verdict because there was no analysis. It is
	// reported instead of FAIL because the change is not being accused of anything, and
	// instead of PASS because nothing was checked.
	//
	// An autonomous agent merges on PASS and on nothing else, so NOT_APPLICABLE blocks it
	// exactly as FAIL does. The distinction is for the human reading the certificate, who
	// needs to know whether to fix a migration or to teach the engine a new format.
	GateNotApplicable GateStatus = "NOT_APPLICABLE"
)

// Gate returns PASS if and only if the grade is A, per docs/RULES.md §3.
//
// This is the single definition of the gate. No caller may re-derive it, because a second
// definition is a second chance to get it wrong in the permissive direction.
func (g Grade) Gate() GateStatus {
	switch g {
	case GradeA:
		return GatePass
	case GradeNotApplicable:
		return GateNotApplicable
	default:
		return GateFail
	}
}

// AnalysisOutcome records what the run was able to do at all, before any question of grading.
//
// It is the first question, and the grade is the second. Without it the certificate had no way
// to distinguish "analyzed and found reversible" from "analyzed nothing", and the two shared
// the value A — which is the P0 recorded in docs/SPECIFICATION.md §2.
type AnalysisOutcome string

// The complete set of outcomes, per docs/RULES.md §3.
const (
	// OutcomeAnalyzed means at least one analyzer claimed at least one file. Only this outcome
	// can produce a measured grade, and therefore only this outcome can produce a PASS.
	OutcomeAnalyzed AnalysisOutcome = "ANALYZED"

	// OutcomeNoCandidates means the changeset holds no file any analyzer could ever claim: a
	// docs-only pull request, or Go source alone. There was genuinely nothing to assess, which
	// is a real and useful answer — it is simply not a passing one.
	OutcomeNoCandidates AnalysisOutcome = "NO_CANDIDATES"

	// OutcomeUnsupportedContent means files that plausibly ARE migrations or manifests were
	// present and no analyzer claimed them. This is the Django case: thirteen .py migrations
	// the engine cannot read. It must never pass a gate.
	OutcomeUnsupportedContent AnalysisOutcome = "UNSUPPORTED_CONTENT"
)

// Valid reports whether o is one of the defined outcomes. The zero value is not.
func (o AnalysisOutcome) Valid() bool {
	switch o {
	case OutcomeAnalyzed, OutcomeNoCandidates, OutcomeUnsupportedContent:
		return true
	default:
		return false
	}
}

// Certifies reports whether this outcome permits a measured grade at all.
//
// The zero value returns false, so a certificate assembled by code that forgot to set the
// outcome cannot carry a grade anyone should act on. That is the same rule as "an unset
// Reversibility is invalid", applied to the shape of the run rather than to a finding.
func (o AnalysisOutcome) Certifies() bool { return o == OutcomeAnalyzed }
