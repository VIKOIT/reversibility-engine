// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// This file is the executable form of the authoritative scoring rules in docs/RULES.md §3:
//
//	Any analyzer error       -> F     (never degrade to a passing grade)
//	Any IRREVERSIBLE         -> F
//	Any UNKNOWN              -> F     (fail-closed, no exceptions)
//	Outcome != ANALYZED      -> N/A   (never A, never PASS)
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

	// outcome is what the run was able to do at all. Only ANALYZED can produce a measured
	// grade; the other two produce N/A, because a grade is a statement about something that
	// was read.
	outcome domain.AnalysisOutcome

	// unsupported holds every file inside a migration directory that no analyzer read. It
	// drives the UNSUPPORTED_CONTENT message when nothing at all was analyzed, and forces F
	// when something was — a changeset the engine only partly understands is one it cannot
	// vouch for.
	unsupported []string
}

// scoreResult is the grade and everything a reader needs to know why it is that grade.
//
// Causes exists because a capped grade used to be unexplainable from the rendered certificate:
// a changeset could arrive at C with every finding REVERSIBLE and nothing in the markdown said
// which condition applied the ceiling. The reader's only recourse was to read the scoring rules
// and re-derive it, which is exactly the work the engine is supposed to have done for them.
type scoreResult struct {
	grade domain.Grade

	// blockers are the reasons the grade is F, or the reasons nothing could be assessed.
	blockers []string

	// causes explain the grade to a human, in the order they were applied: the assignment
	// first, then every cap that lowered it. For grade A the single cause states that nothing
	// capped it, because "no explanation" and "nothing to explain" must not look the same.
	causes []string
}

// score computes the grade, the reasons for an F, and the causes of whatever grade results.
func score(in scoreInput) scoreResult {
	// An analysis that did not finish cannot certify anything. This is checked before the
	// findings, because a partial finding list is not evidence of safety.
	if len(in.analyzerErrors) > 0 {
		blockers := make([]string, 0, len(in.analyzerErrors))
		for _, e := range in.analyzerErrors {
			blockers = append(blockers, "analysis did not complete: "+e)
		}
		sort.Strings(blockers)
		return scoreResult{
			grade:    domain.GradeF,
			blockers: blockers,
			causes:   []string{"graded F: the analysis did not complete, so nothing about this change could be established"},
		}
	}

	if blockers := blockingFindings(in.findings); len(blockers) > 0 {
		return scoreResult{
			grade:    domain.GradeF,
			blockers: blockers,
			causes:   []string{fmt.Sprintf("graded F by %d blocking finding(s), listed above", len(blockers))},
		}
	}

	// What the run was able to do at all, per docs/RULES.md §3. This sits below the two checks
	// above deliberately: a broken analyzer outranks a changeset with nothing in it, because a
	// run that crashed on the way to finding nothing did not establish that there was nothing.
	//
	// Neither non-analyzed outcome grades. A used to mean both "analyzed and found reversible"
	// and "read nothing", and only readers of the second meaning were ever surprised.
	switch in.outcome {
	case domain.OutcomeAnalyzed:
		// Analyzed, but not completely. **A partial pass is a bypass**, so this fails closed
		// before anything is scored.
		//
		// This reverses the earlier ruling that partial coverage never moves the grade, and the
		// reversal is recorded rather than silently applied — see docs/SPECIFICATION.md §16.7.
		// The argument that changed it: F does not mean "this change is dangerous", it means
		// "this cannot be certified", which is already what it means for an analyzer error and
		// for PG027. An analysis that read four of five migrations did not complete. Grading it
		// on the four is a verdict about a changeset that does not exist.
		if len(in.unsupported) > 0 {
			blockers := make([]string, 0, len(in.unsupported)+1)
			blockers = append(blockers, PartialCoverageBlocker)
			for _, p := range in.unsupported {
				blockers = append(blockers, "not analyzed: "+p)
			}

			return scoreResult{
				grade:    domain.GradeF,
				blockers: blockers,
				causes: []string{fmt.Sprintf(
					"graded F: coverage is PARTIAL — %d file(s) in migration directories were not analyzed",
					len(in.unsupported))},
			}
		}

	case domain.OutcomeNoCandidates:
		// Genuinely nothing to assess. That is a real answer and it is reported as one — it is
		// simply not a passing one, and it carries no blockers because nothing is wrong.
		return scoreResult{
			grade:  domain.GradeNotApplicable,
			causes: []string{"not graded: the changeset held no file any analyzer could claim"},
		}

	case domain.OutcomeUnsupportedContent:
		// The Django case. Files that plausibly are migrations, and nothing that could read
		// them. The blockers name what was seen and what could not be done with it, because
		// "not applicable" on its own reads as "nothing here".
		return scoreResult{
			grade:    domain.GradeNotApplicable,
			blockers: unsupportedContentBlockers(in.unsupported),
			causes:   []string{"not graded: no analyzer could read the files in this changeset"},
		}

	default:
		// An outcome the domain does not recognise means the caller assembled a score input
		// incorrectly — most likely by leaving the field at its zero value. Fail closed: the
		// shape of the run is unknown, and unknown is unsafe.
		return scoreResult{
			grade: domain.GradeF,
			blockers: []string{
				"the engine could not establish what this run analyzed, so nothing about it can be certified",
			},
			causes: []string{"graded F: the shape of this run could not be established"},
		}
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

	// Assignment. Every branch records why, because a grade a reader cannot account for is a
	// grade they will argue with rather than act on.
	var causes []string
	grade := domain.GradeA
	switch {
	case costly >= 3:
		grade = domain.GradeC
		causes = append(causes, fmt.Sprintf("assigned C: %d findings are COSTLY to reverse", costly))
	case costly >= 1:
		grade = domain.GradeB
		causes = append(causes, fmt.Sprintf("assigned B: %d finding(s) are COSTLY to reverse", costly))
	default:
		causes = append(causes, "assigned A: every finding is REVERSIBLE")
	}

	// applyCap lowers the grade and records the condition that lowered it. Recording happens
	// only when the cap actually bites: listing a ceiling that changed nothing would bury the
	// one that did.
	applyCap := func(limit domain.Grade, reason string) {
		capped := grade.Cap(limit)
		if capped != grade {
			causes = append(causes, fmt.Sprintf("capped at %s: %s", limit, reason))
		}
		grade = capped
	}

	// Caps, each applied unconditionally so that order cannot matter.
	if !downOK {
		applyCap(domain.GradeC, "no usable down migration for "+strings.Join(unsoundDownMigrations(in.downMigrations), ", "))
	}
	if worstLock.AtLeast(domain.LockTableRewrite) {
		applyCap(domain.GradeB, fmt.Sprintf("a %s lock is held while the change is applied", worstLock))
	}

	// Production context, when there is any. A lock duration band lowers the ceiling and never
	// lifts it: a NEGLIGIBLE band caps nothing, so a small table cannot turn a C into a B. The
	// band is only ever set from a snapshot that established a size, so its absence — no
	// snapshot, a stale one, an unresolvable table — imposes nothing and changes nothing.
	for _, f := range in.findings {
		if f.Context == nil {
			continue
		}
		if cap := f.Context.LockDurationBand.Cap(); cap != "" {
			applyCap(cap, fmt.Sprintf("%s at %s holds its lock for an estimated %s (%s)",
				f.RuleID, location(f), f.Context.EstimatedLockDuration, f.Context.LockDurationBand))
		}
	}

	// The A row states the conditions for A, so failing any of them means the grade is not A.
	// B is the highest grade below A, which makes this the least punitive reading consistent
	// with the table. It matters for FULL_SCAN, which fails the "lock <= SHORT" condition but
	// does not reach the TABLE_REWRITE cap above.
	if !allReversible || worstLock.AtLeast(domain.LockFullScan) || !downOK {
		applyCap(domain.GradeB, "grade A requires every finding REVERSIBLE, a lock no worse than SHORT, "+
			"and a valid down migration; "+failedARowConditions(allReversible, worstLock, downOK))
	}

	if grade == domain.GradeA {
		// Said explicitly. "Nothing capped this" and "nobody checked" must not render the same,
		// and an A with no explanation beside it is indistinguishable from an unexplained one.
		causes = append(causes, "nothing capped this grade")
	}

	return scoreResult{grade: grade, causes: causes}
}

// failedARowConditions names which of grade A's three conditions this changeset missed.
//
// Naming the specific one matters: "did not meet the conditions for A" sends a reader back to
// the rule table to work out which, which is the work the engine already did.
func failedARowConditions(allReversible bool, worstLock domain.LockHazard, downOK bool) string {
	var missed []string
	if !allReversible {
		missed = append(missed, "not every finding is REVERSIBLE")
	}
	if worstLock.AtLeast(domain.LockFullScan) {
		missed = append(missed, "the worst lock is "+string(worstLock))
	}
	if !downOK {
		missed = append(missed, "a down migration is missing or unparseable")
	}
	return strings.Join(missed, ", ")
}

// unsoundDownMigrations names the migrations whose down file is missing or unparseable.
//
// The owner's example of a good cause line is "capped at C: no down migration for
// 0031_add_users.up.sql" — naming the file is the whole difference between a cause a reader can
// act on and one they have to investigate.
func unsoundDownMigrations(statuses []domain.DownMigrationStatus) []string {
	var out []string
	for _, s := range statuses {
		if !s.Exists || !s.Parses {
			out = append(out, or(s.UpFile, s.Migration))
		}
	}
	if len(out) == 0 {
		return []string{"a migration in this changeset"}
	}
	sort.Strings(out)
	return out
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
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
		case domain.ReversibilityWillFail:
			// Deliberately worded apart from irreversible. That one says the change cannot be
			// undone; this one says it will not apply at all, so the fix is to the migration
			// rather than to the rollback plan, and a reader must not confuse the two.
			blockers = append(blockers, fmt.Sprintf("%s at %s: will not apply — %s", f.RuleID, location(f), f.Rationale))
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
