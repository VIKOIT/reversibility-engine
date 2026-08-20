// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

// SchemaVersion is the version of the certificate wire format. It follows semantic versioning
// and is bumped on any breaking field change, because downstream merge gates parse this.
const SchemaVersion = "1.0.0"

// DownMigrationStatus records the outcome of down-migration validation for one migration pair,
// at the three levels defined in docs/RULES.md §1.
//
// The levels are reported separately rather than collapsed into a single boolean because they
// carry very different authority: levels 1 and 2 are facts, level 3 is a guess.
type DownMigrationStatus struct {
	// Migration is the identity shared by the up and down files, such as "0042_add_orders".
	Migration string `json:"migration"`

	UpFile string `json:"upFile"`

	// DownFile is empty when no down migration was found.
	DownFile string `json:"downFile,omitempty"`

	// Exists is level 1: a down file was found.
	Exists bool `json:"exists"`

	// Parses is level 2: the down file is non-empty and parses.
	Parses bool `json:"parses"`

	// Symmetric is level 3: every CREATE in the up file has a matching DROP in the down file
	// and vice versa.
	//
	// ADVISORY ONLY. This is a heuristic and it must never, on its own, produce grade F. A
	// migration can be perfectly reversible without textual symmetry.
	Symmetric bool `json:"symmetric"`

	// SymmetryNotes explains what level 3 objected to, so a reviewer can dismiss a false
	// positive without reading the analyzer's source.
	SymmetryNotes []string `json:"symmetryNotes,omitempty"`
}

// ReversibilityCertificate is the engine's complete verdict on a changeset.
//
// Identical input must produce a byte-identical certificate. There are deliberately no
// timestamps, no run IDs, no hostnames, and no map-ordered fields: the certificate is a merge
// gate, and reruns must not change the answer.
type ReversibilityCertificate struct {
	SchemaVersion string `json:"schemaVersion"`

	Grade Grade `json:"grade"`

	// AIGateStatus is PASS if and only if Grade is A. Autonomous agents merge on PASS only.
	AIGateStatus GateStatus `json:"aiGateStatus"`

	// Applicable is false when the changeset contained no files any analyzer understands.
	// Such a changeset grades A and passes the gate: the engine has no opinion, and inventing
	// one would train users to ignore it.
	Applicable bool `json:"applicable"`

	// InputDigest is the SHA-256 over the sorted (path, content) pairs of the analyzed input.
	// It is what makes a certificate attributable to an exact changeset.
	InputDigest string `json:"inputDigest"`

	// Findings is sorted by File, then Line, then RuleID.
	Findings []Finding `json:"findings"`

	// UndoPlan is built only from the UndoStep fields of Findings, in reverse order of
	// application. It is empty when any finding is IRREVERSIBLE — see Blockers.
	UndoPlan []UndoStep `json:"undoPlan"`

	// Blockers lists, in human-readable form, every reason the grade is F.
	Blockers []string `json:"blockers"`

	// DownMigrations records down-migration validation per migration pair.
	DownMigrations []DownMigrationStatus `json:"downMigrations"`
}
