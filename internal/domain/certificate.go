// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

// SchemaVersion is the version of the certificate wire format. It follows semantic versioning
// and is bumped on any breaking field change, because downstream merge gates parse this.
const SchemaVersion = "1.5.0"

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

	// Grade is what the evidence says about the change itself. No configuration moves it: a
	// DROP TABLE is irreversible whoever signed off on it, and a grade that policy could
	// improve would stop meaning "reversibility" and start meaning "reversibility, unless
	// somebody filed a form".
	//
	// It is N/A when Outcome is not ANALYZED. A means analyzed and found reversible, and
	// nothing else — the engine never reports a passing grade for a changeset it did not read.
	Grade Grade `json:"grade"`

	// Outcome records what the run was able to do at all: whether any analyzer claimed any
	// file, and if not, whether the changeset held anything that looked like it should have
	// been claimed. It is the field a consumer should switch on, because it is the only one
	// that distinguishes "nothing to check here" from "something to check that I could not".
	//
	// It is deliberately not derivable from Grade. N/A tells a reader there is no measurement;
	// only this says why, and the exit code the CLI produces follows from it.
	Outcome AnalysisOutcome `json:"outcome"`

	// EffectiveGrade is Grade with waived findings set aside — the grade a CI gate compares
	// against a threshold. Without a policy it equals Grade, so a consumer has exactly one
	// field to compare and never has to know whether a policy existed.
	//
	// It is never used for AIGateStatus. That is the whole point of the split.
	EffectiveGrade Grade `json:"effectiveGrade"`

	// AIGateStatus is PASS if and only if Grade is A. Autonomous agents merge on PASS only.
	//
	// It follows Grade, not EffectiveGrade, so a waiver can unblock a human's pipeline without
	// ever authorising an agent to merge something nobody could undo.
	AIGateStatus GateStatus `json:"aiGateStatus"`

	// Applicable is true exactly when Outcome is ANALYZED. It is retained for consumers pinned
	// to schema 1.4.0 and it is derived, never set independently — two fields that can disagree
	// about the same fact are two chances to read the wrong one.
	//
	// It used to be the whole story, and it was not enough: a false Applicable sat beside grade
	// A and gate PASS, and nothing downstream read past the grade. Outcome is the field to
	// branch on.
	Applicable bool `json:"applicable"`

	// InputDigest is the SHA-256 over the sorted (path, content) pairs of the analyzed input.
	// It is what makes a certificate attributable to an exact changeset.
	InputDigest string `json:"inputDigest"`

	// Findings is sorted by File, then Line, then RuleID. Waived findings are not here; they
	// are in Waived.
	Findings []Finding `json:"findings"`

	// Waived lists findings a live policy waiver downgraded to advisory, with the reason and
	// expiry beside each. They are reported rather than removed: silent suppression is how a
	// safety tool stops being one, and a reader cannot judge an accepted risk they cannot see.
	Waived []WaivedFinding `json:"waived"`

	// PolicyDigest is the SHA-256 over the resolved policy, or "" when no policy applied. It
	// makes a verdict attributable to the configuration that produced it, not just the input.
	PolicyDigest string `json:"policyDigest,omitempty"`

	// CatalogVersion identifies the resource-type catalog that classified a Terraform plan, or
	// "" when no plan was analyzed. The same plan can grade differently under two catalogs, and
	// without this there would be no way to tell which one produced the verdict.
	CatalogVersion string `json:"catalogVersion,omitempty"`

	// ContextWarnings records what was wrong with the production snapshots that were supplied:
	// a stale one, most often. They are warnings rather than findings because they are about
	// the evidence rather than about the change.
	//
	// A warning never improves anything. Stale context is used and flagged rather than
	// discarded, because the alternative — silently falling back to no context — would make a
	// certificate quietly less informative at exactly the moment somebody stopped refreshing
	// the snapshot.
	ContextWarnings []string `json:"contextWarnings,omitempty"`

	// UndoPlan is built from the UndoStep fields of Findings and of Waived alike, in reverse
	// order of application. It is empty when any of them is IRREVERSIBLE — see Blockers.
	//
	// Waived findings count here even though they do not count toward EffectiveGrade. A waiver
	// accepts a risk; it does not make the change reversible, and a plan that quietly omitted
	// the waived half would claim a completeness it does not have.
	UndoPlan []UndoStep `json:"undoPlan"`

	// Blockers lists, in human-readable form, every reason this changeset cannot be certified
	// as reversible. That is every reason the grade is F, and — since the P0 — the reason an
	// UNSUPPORTED_CONTENT run could not assess anything, naming the files it could not read.
	//
	// It is empty for A, B, and C, and for NO_CANDIDATES, where there is nothing to report.
	Blockers []string `json:"blockers"`

	// DownMigrations records down-migration validation per migration pair.
	DownMigrations []DownMigrationStatus `json:"downMigrations"`
}
