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
// A means fully reversible. F means a rollback would lose data, hit an unknown construct, or
// depend on an analysis that failed.
type Grade string

// The complete set of grades.
const (
	GradeA Grade = "A"
	GradeB Grade = "B"
	GradeC Grade = "C"
	GradeF Grade = "F"
)

// GateStatus is the merge-gate verdict. Autonomous agents merge on PASS and nothing else.
type GateStatus string

// The complete set of gate statuses.
const (
	GatePass GateStatus = "PASS"
	GateFail GateStatus = "FAIL"
)

// Reversibility is the verdict on a single change.
type Reversibility string

// The complete set of reversibility verdicts.
const (
	Reversible   Reversibility = "REVERSIBLE"
	Costly       Reversibility = "COSTLY"
	Irreversible Reversibility = "IRREVERSIBLE"
	Unknown      Reversibility = "UNKNOWN"
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

	Grade Grade `json:"grade"`

	// AIGateStatus is PASS if and only if Grade is A.
	AIGateStatus GateStatus `json:"aiGateStatus"`

	// Applicable is false when the changeset contained no files the engine understands. Such a
	// changeset grades A: the engine has no opinion, rather than a positive one.
	Applicable bool `json:"applicable"`

	// InputDigest is the SHA-256 over the analyzed changeset, which is what makes a certificate
	// attributable to an exact input.
	InputDigest string `json:"inputDigest"`

	// Findings is sorted by File, then Line, then RuleID.
	Findings []Finding `json:"findings"`

	// UndoPlan is the rollback script, in reverse order of application. When any finding is
	// irreversible or unknown it is replaced by a statement that no complete undo exists.
	UndoPlan []string `json:"undoPlan"`

	// Blockers lists the reasons the grade is F. Empty for any other grade.
	Blockers []string `json:"blockers"`

	DownMigrations []DownMigrationStatus `json:"downMigrations"`
}

// Passed reports whether the merge gate allows this change through.
//
// Consumers should call this rather than comparing the grade themselves, so there is one
// definition of the gate rather than one per consumer.
func (c Certificate) Passed() bool { return c.AIGateStatus == GatePass }

// FromDomain converts the internal certificate to the public schema.
//
// Slices are normalized to empty rather than nil so the JSON form is stable: encoding/json
// renders a nil slice as null and an empty one as [], and determinism has to survive that.
func FromDomain(in domain.ReversibilityCertificate) Certificate {
	out := Certificate{
		SchemaVersion:  in.SchemaVersion,
		Grade:          Grade(in.Grade),
		AIGateStatus:   GateStatus(in.AIGateStatus),
		Applicable:     in.Applicable,
		InputDigest:    in.InputDigest,
		Findings:       make([]Finding, 0, len(in.Findings)),
		UndoPlan:       make([]string, 0, len(in.UndoPlan)),
		Blockers:       make([]string, 0, len(in.Blockers)),
		DownMigrations: make([]DownMigrationStatus, 0, len(in.DownMigrations)),
	}

	for _, f := range in.Findings {
		out.Findings = append(out.Findings, Finding{
			RuleID:        f.RuleID,
			File:          f.File,
			Line:          f.Line,
			Statement:     f.Statement,
			Reversibility: Reversibility(f.Reversibility),
			LockHazard:    LockHazard(f.LockHazard),
			Rationale:     f.Rationale,
			UndoStep:      string(f.UndoStep),
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
