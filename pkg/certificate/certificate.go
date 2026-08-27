// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package certificate is the public, versioned wire schema of the ReversibilityCertificate.
//
// It exists so that external consumers — CI gates, dashboards, autonomous merge bots — can
// depend on a stable contract. Go forbids importing anything under internal/, so without this
// package there would be no supported way to read a certificate from outside the module, and
// consumers would end up parsing JSON by hand against a type that is free to change.
//
// The internal domain model may be refactored at will. This schema may not: SchemaVersion
// follows semantic versioning and is bumped on any breaking field change.
package certificate

import (
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// SchemaVersion is the version of this wire format.
const SchemaVersion = domain.SchemaVersion

// Grade is the overall verdict on a changeset.
//
// A means analyzed and found fully reversible, and nothing else. F means a rollback would lose
// data, hit an unknown construct, or depend on an analysis that failed.
type Grade string

// The complete set of grades.
const (
	GradeA Grade = "A"
	GradeB Grade = "B"
	GradeC Grade = "C"
	GradeF Grade = "F"

	// GradeNotApplicable means the engine did not analyze this changeset and so has no
	// measurement to report. Added in schema 1.5.0.
	//
	// A gate written as grade == "A" is unaffected. A gate written as grade != "F" is not:
	// switch on Outcome, or compare against A explicitly.
	GradeNotApplicable Grade = "N/A"
)

// GateStatus is the merge-gate verdict. Autonomous agents merge on PASS and nothing else.
type GateStatus string

// The complete set of gate statuses.
const (
	GatePass GateStatus = "PASS"
	GateFail GateStatus = "FAIL"

	// GateNotApplicable means there is no gate verdict because there was no analysis. Added in
	// schema 1.5.0. It blocks an agent exactly as FAIL does; the distinction is for the human
	// deciding whether to fix a migration or to teach the engine a new format.
	GateNotApplicable GateStatus = "NOT_APPLICABLE"
)

// Coverage is how much of the changeset the engine actually read. Added in schema 1.5.0.
//
// It is a second axis, not a modifier of the grade. PARTIAL never changes Grade — a file the
// engine cannot parse is not evidence that the change is unsafe. It changes only the gate:
// AIGateStatus is PASS only when Grade is A and Coverage is FULL.
type Coverage string

// The complete set of coverage states.
const (
	// CoverageFull means every file any analyzer could claim was claimed. A changeset with
	// nothing claimable is vacuously full: nothing was skipped.
	CoverageFull Coverage = "FULL"

	// CoveragePartial means files that plausibly are migrations went unread. UnanalyzedFiles
	// names each one and why.
	CoveragePartial Coverage = "PARTIAL"
)

// UnanalyzedFile is one file the engine could not read, and the reason. Added in schema 1.5.0.
type UnanalyzedFile struct {
	Path string `json:"path"`

	// Reason states why no analyzer claimed this file. It describes the engine's limitation,
	// never the file's quality.
	Reason string `json:"reason"`
}

// AnalysisOutcome records what the run was able to do at all, before any question of grading.
// Added in schema 1.5.0.
//
// This is the field to switch on. It is the only one that separates "there was nothing here to
// check" from "there was something here I could not check", and those need different responses
// from whoever reads the certificate.
type AnalysisOutcome string

// The complete set of outcomes.
const (
	// OutcomeAnalyzed means at least one analyzer claimed at least one file. Only this outcome
	// can produce a measured grade, and therefore only this outcome can produce a PASS.
	OutcomeAnalyzed AnalysisOutcome = "ANALYZED"

	// OutcomeNoCandidates means the changeset held nothing any analyzer could ever claim — a
	// docs-only pull request, Go source alone. Nothing to assess, which is a real answer.
	OutcomeNoCandidates AnalysisOutcome = "NO_CANDIDATES"

	// OutcomeUnsupportedContent means files that plausibly are migrations or manifests were
	// present and no analyzer claimed them: Django .py migrations, for instance. Blockers names
	// what was seen. This must never be treated as a pass.
	OutcomeUnsupportedContent AnalysisOutcome = "UNSUPPORTED_CONTENT"
)

// Reversibility is the verdict on a single change.
type Reversibility string

// The complete set of reversibility verdicts.
const (
	Reversible   Reversibility = "REVERSIBLE"
	Costly       Reversibility = "COSTLY"
	Irreversible Reversibility = "IRREVERSIBLE"
	Unknown      Reversibility = "UNKNOWN"

	// WillFail means production state was checked and the change is certain to abort. It is a
	// different failure from Irreversible: that one cannot be undone, this one will not run.
	WillFail Reversibility = "WILL_FAIL"
)

// LockHazard describes the locking cost of applying a change to a live database.
type LockHazard string

// The complete set of lock hazards, in increasing severity.
const (
	LockNone         LockHazard = "NONE"
	LockShort        LockHazard = "SHORT"
	LockFullScan     LockHazard = "FULL_SCAN"
	LockTableRewrite LockHazard = "TABLE_REWRITE"
	LockExclusive    LockHazard = "EXCLUSIVE"
)

// Finding is one classified change.
type Finding struct {
	// RuleID is a stable identifier such as "PG001" or "K8S003". Safe to suppress, alert on,
	// and dashboard against.
	RuleID string `json:"ruleId"`

	File string `json:"file"`

	// Line is 1-based. Zero means the finding is a property of the whole file or object rather
	// than of a particular line.
	Line int `json:"line"`

	Statement     string        `json:"statement"`
	Reversibility Reversibility `json:"reversibility"`
	LockHazard    LockHazard    `json:"lockHazard"`
	Rationale     string        `json:"rationale"`

	// UndoStep is the exact command that reverses this change, or empty when none exists.
	UndoStep string `json:"undoStep,omitempty"`

	// Context is what a production snapshot added, if one was supplied. Absent by default: the
	// engine works exactly as it did before snapshots existed, and every number in here is an
	// estimate rather than a measurement. See docs/ESTIMATES.md.
	Context *FindingContext `json:"context,omitempty"`
}

// FindingContext is what a production snapshot told the engine about a finding's subject.
//
// EVERY NUMBER HERE IS AN ESTIMATE, derived from planner statistics that the database itself
// keeps approximately. They exist to turn "this rewrites the table" into "this rewrites a table
// of roughly this size" — never to promise how long anything will take.
type FindingContext struct {
	// RowEstimate is the planner's row count for the subject relation.
	RowEstimate int64 `json:"rowEstimate,omitempty"`

	// SizeBytes is the on-disk size of the subject, table or index.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// EstimatedLockDuration is an approximation such as "~14m", always carrying a leading tilde
	// so it cannot be read as a measurement.
	EstimatedLockDuration string `json:"estimatedLockDuration,omitempty"`

	// LockDurationBand buckets that duration: NEGLIGIBLE, NOTICEABLE, DISRUPTIVE, or OUTAGE.
	// Empty when no band was computed. DISRUPTIVE and OUTAGE lower the grade; the milder two
	// and an absent band change nothing, because a small table is not evidence of safety.
	LockDurationBand string `json:"lockDurationBand,omitempty"`

	// ContextNote states, in one sentence, a fact the snapshot established.
	ContextNote string `json:"contextNote,omitempty"`
}

// WaivedFinding is a finding a policy waiver downgraded to advisory.
//
// It carries the whole finding, not a summary. A reader can only judge whether an accepted risk
// still holds if the thing that was accepted is in front of them.
type WaivedFinding struct {
	Finding Finding `json:"finding"`

	// Reason is why the risk was accepted. A waiver cannot exist without one.
	Reason string `json:"reason"`

	// Expires is the last day the waiver applies, as YYYY-MM-DD. After it the finding returns
	// on its own, with no edit to the policy.
	Expires string `json:"expires"`

	// ApprovedBy is who accepted the risk, if the policy recorded it.
	ApprovedBy string `json:"approvedBy,omitempty"`
}

// DownMigrationStatus records down-migration validation for one migration pair.
//
// The three levels are reported separately because they carry different authority: Exists and
// Parses are facts, Symmetric is a heuristic.
type DownMigrationStatus struct {
	Migration string `json:"migration"`
	UpFile    string `json:"upFile"`
	DownFile  string `json:"downFile,omitempty"`

	Exists bool `json:"exists"`
	Parses bool `json:"parses"`

	// Symmetric is advisory only and never causes a failing grade on its own.
	Symmetric     bool     `json:"symmetric"`
	SymmetryNotes []string `json:"symmetryNotes,omitempty"`
}

// Certificate is the engine's complete verdict on a changeset.
//
// Identical input produces a byte-identical certificate: there are deliberately no timestamps,
// run IDs, or hostnames, so a rerun never changes the answer and two certificates can be
// compared directly.
type Certificate struct {
	SchemaVersion string `json:"schemaVersion"`

	// Grade is what the evidence says about the change. No policy setting moves it. It is N/A
	// when Outcome is not ANALYZED — the engine never reports a passing grade for a changeset
	// it did not read.
	Grade Grade `json:"grade"`

	// Outcome records what the run was able to do at all. Switch on this rather than inferring
	// it from Grade: N/A says there is no measurement, and only Outcome says why.
	Outcome AnalysisOutcome `json:"outcome"`

	// EffectiveGrade is Grade with waived findings set aside — the one to compare against a CI
	// threshold. With no policy in play it equals Grade, so this is always the right field to
	// gate on and a consumer never has to know whether a policy existed.
	EffectiveGrade Grade `json:"effectiveGrade"`

	// AIGateStatus is PASS if and only if Grade is A and Coverage is FULL. It follows Grade
	// rather than EffectiveGrade: a waiver may unblock a human's pipeline, never an agent's
	// merge. Coverage enters for the same reason from the other side — a human can read the
	// list of files nobody analyzed and judge for themselves, and an agent cannot.
	AIGateStatus GateStatus `json:"aiGateStatus"`

	// Coverage is how much of the changeset the engine read: FULL or PARTIAL. It never changes
	// Grade, only this gate.
	Coverage Coverage `json:"coverage"`

	// UnanalyzedFiles names every file the engine did not read, and why. Empty when Coverage is
	// FULL. Sorted by path.
	UnanalyzedFiles []UnanalyzedFile `json:"unanalyzedFiles"`

	// IgnoredByPolicy names every candidate file a .reversibility.yml excluded. Added in schema
	// 1.5.0.
	//
	// These do not count against Coverage — the engine could have read them and was told not to
	// — but they do close the merge gate. An ignore is a human accepting a risk, and an agent
	// never inherits a human's acceptance.
	IgnoredByPolicy []string `json:"ignoredByPolicy"`

	// Applicable is true exactly when Outcome is ANALYZED. Retained for consumers pinned to
	// schema 1.4.0; new code should read Outcome, which distinguishes the two ways a changeset
	// can be inapplicable.
	Applicable bool `json:"applicable"`

	// InputDigest is the SHA-256 over the analyzed changeset, which is what makes a certificate
	// attributable to an exact input.
	InputDigest string `json:"inputDigest"`

	// Findings is sorted by File, then Line, then RuleID. Waived findings are in Waived.
	Findings []Finding `json:"findings"`

	// Waived lists findings a policy waiver downgraded to advisory, each with the reason it was
	// accepted and the day the waiver lapses. They are reported, never suppressed.
	Waived []WaivedFinding `json:"waived"`

	// PolicyDigest is the SHA-256 over the resolved policy, or "" when none applied.
	PolicyDigest string `json:"policyDigest,omitempty"`

	// CatalogVersion identifies the Terraform resource-type catalog that classified a plan, or
	// "" when no plan was analyzed. The same plan can grade differently under two catalogs.
	CatalogVersion string `json:"catalogVersion,omitempty"`

	// ContextWarnings records what was wrong with the production snapshots supplied — a stale
	// one, most often. Stale context is used and flagged rather than discarded, because silently
	// falling back to none would make a certificate quietly less informative at exactly the
	// moment somebody stopped refreshing the snapshot.
	ContextWarnings []string `json:"contextWarnings,omitempty"`

	// UndoPlan is the rollback script, in reverse order of application. When any finding is
	// irreversible or unknown it is replaced by a statement that no complete undo exists.
	UndoPlan []string `json:"undoPlan"`

	// Blockers lists the reasons this changeset cannot be certified as reversible: every reason
	// the grade is F, and for UNSUPPORTED_CONTENT the files that could not be assessed. Empty
	// for A, B, C, and NO_CANDIDATES.
	Blockers []string `json:"blockers"`

	// GradeCauses explains the grade: the assignment, then every cap that lowered it. Added in
	// schema 1.5.0. Never empty for a graded certificate — an A says that nothing capped it.
	GradeCauses []string `json:"gradeCauses"`

	DownMigrations []DownMigrationStatus `json:"downMigrations"`
}

// Passed reports whether the merge gate allows this change through.
//
// Consumers should call this rather than comparing the grade themselves, so there is one
// definition of the gate rather than one per consumer.
func (c Certificate) Passed() bool { return c.AIGateStatus == GatePass }

// Assessed reports whether the engine actually analyzed this changeset.
//
// A consumer that treats "not F" as safe should call this first. It is false for both
// non-analyzed outcomes, and false for a certificate whose outcome is missing entirely — an
// unreadable certificate is not an assessed one.
func (c Certificate) Assessed() bool { return c.Outcome == OutcomeAnalyzed }

// FullyCovered reports whether the engine read every file it could have.
//
// False for a certificate with no coverage field at all, which is what a pre-1.5.0 producer
// emits: an unknown coverage is not a full one.
func (c Certificate) FullyCovered() bool { return c.Coverage == CoverageFull }

// FromDomain converts the internal certificate to the public schema.
//
// Slices are normalized to empty rather than nil so the JSON form is stable: encoding/json
// renders a nil slice as null and an empty one as [], and determinism has to survive that.
func FromDomain(in domain.ReversibilityCertificate) Certificate {
	out := Certificate{
		SchemaVersion:   in.SchemaVersion,
		Grade:           Grade(in.Grade),
		EffectiveGrade:  Grade(in.EffectiveGrade),
		AIGateStatus:    GateStatus(in.AIGateStatus),
		Outcome:         AnalysisOutcome(in.Outcome),
		Coverage:        Coverage(in.Coverage),
		UnanalyzedFiles: make([]UnanalyzedFile, 0, len(in.UnanalyzedFiles)),
		IgnoredByPolicy: append([]string{}, in.IgnoredByPolicy...),
		Applicable:      in.Applicable,
		InputDigest:     in.InputDigest,
		PolicyDigest:    in.PolicyDigest,
		CatalogVersion:  in.CatalogVersion,
		ContextWarnings: in.ContextWarnings,
		Findings:        make([]Finding, 0, len(in.Findings)),
		Waived:          make([]WaivedFinding, 0, len(in.Waived)),
		UndoPlan:        make([]string, 0, len(in.UndoPlan)),
		Blockers:        make([]string, 0, len(in.Blockers)),
		GradeCauses:     append([]string{}, in.GradeCauses...),
		DownMigrations:  make([]DownMigrationStatus, 0, len(in.DownMigrations)),
	}

	for _, f := range in.Findings {
		out.Findings = append(out.Findings, fromDomainFinding(f))
	}

	for _, u := range in.UnanalyzedFiles {
		out.UnanalyzedFiles = append(out.UnanalyzedFiles, UnanalyzedFile{Path: u.Path, Reason: u.Reason})
	}

	for _, w := range in.Waived {
		out.Waived = append(out.Waived, WaivedFinding{
			Finding:    fromDomainFinding(w.Finding),
			Reason:     w.Reason,
			Expires:    w.Expires,
			ApprovedBy: w.ApprovedBy,
		})
	}

	for _, step := range in.UndoPlan {
		out.UndoPlan = append(out.UndoPlan, string(step))
	}

	out.Blockers = append(out.Blockers, in.Blockers...)

	for _, d := range in.DownMigrations {
		out.DownMigrations = append(out.DownMigrations, DownMigrationStatus{
			Migration:     d.Migration,
			UpFile:        d.UpFile,
			DownFile:      d.DownFile,
			Exists:        d.Exists,
			Parses:        d.Parses,
			Symmetric:     d.Symmetric,
			SymmetryNotes: d.SymmetryNotes,
		})
	}

	return out
}

func fromDomainFinding(f domain.Finding) Finding {
	out := Finding{
		RuleID:        f.RuleID,
		File:          f.File,
		Line:          f.Line,
		Statement:     f.Statement,
		Reversibility: Reversibility(f.Reversibility),
		LockHazard:    LockHazard(f.LockHazard),
		Rationale:     f.Rationale,
		UndoStep:      string(f.UndoStep),
	}

	if f.Context != nil {
		out.Context = &FindingContext{
			RowEstimate:           f.Context.RowEstimate,
			SizeBytes:             f.Context.SizeBytes,
			EstimatedLockDuration: f.Context.EstimatedLockDuration,
			LockDurationBand:      string(f.Context.LockDurationBand),
			ContextNote:           f.Context.ContextNote,
		}
	}

	return out
}
