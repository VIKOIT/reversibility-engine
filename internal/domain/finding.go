// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

import "sort"

// MaxStatementLength bounds the Statement field of a Finding.
//
// A certificate is rendered into PR comments and check runs; an unbounded statement from a
// generated migration could bury the verdict under a megabyte of SQL.
const MaxStatementLength = 200

// RuleEnginePanic is the rule ID attached to a finding synthesized when the engine recovers
// from a panic. It exists so that a crash is reported as a graded, visible failure rather than
// disappearing into a log line.
const RuleEnginePanic = "ENGINE_PANIC"

// RuleProviderError is the rule ID attached to a finding synthesized when the changeset could
// not be fetched at all.
//
// A rate limit, a network failure, or a file too large to retrieve all end here. The alternative
// — analyzing whichever files happened to arrive — would produce a confident grade for a change
// the engine only partly saw, which is the exact failure the fail-closed rule exists to prevent.
const RuleProviderError = "PROVIDER_ERROR"

// UndoStep is the exact command that reverses one change — an SQL statement or a kubectl
// invocation, never prose.
//
// It is a command because an undo plan is meant to be executed under incident pressure, by
// someone who did not write the migration.
type UndoStep string

// Finding is one classified change: what it is, whether it can be undone, and what undoing it
// would take.
type Finding struct {
	// RuleID is a stable identifier from the authoritative tables, such as "PG001". Stability
	// matters because users suppress, alert on, and dashboard these.
	RuleID string `json:"ruleId"`

	File string `json:"file"`

	// Line is 1-based. Zero means the finding is a property of the whole file rather than of a
	// particular statement.
	Line int `json:"line"`

	// Statement is the normalized, truncated source of the change.
	Statement string `json:"statement"`

	Reversibility Reversibility `json:"reversibility"`
	LockHazard    LockHazard    `json:"lockHazard"`

	// Rationale explains why this verdict was reached, in one sentence, for a reader who did
	// not write the change.
	Rationale string `json:"rationale"`

	// UndoStep is empty when no undo is possible.
	UndoStep UndoStep `json:"undoStep,omitempty"`

	// Subject names what the change acts on, so that a later stage can look the object up
	// without re-reading the source.
	//
	// It exists because production context has to be matched to a finding by object name, and
	// the only alternative would be re-parsing Statement — which is truncated, normalized, and
	// would need a regex. Analyzers already know these names; this is them saying so.
	//
	// It is serialized here but deliberately absent from pkg/certificate: it is how the engine
	// joins a finding to a snapshot, not a promise to external consumers about how object names
	// are spelled.
	Subject Subject `json:"subject,omitempty"`

	// Context is what a production snapshot added, if one was supplied. Every field is optional
	// and absent by default: the engine works exactly as it did before snapshots existed.
	Context *FindingContext `json:"context,omitempty"`
}

// Subject identifies the object a finding is about.
//
// The meaning of Object depends on the rule — a column for a type change, an index for a drop,
// a constraint for a validation. Interpreting it is the reader's job, because the alternative is
// one field per kind of object and a struct nobody can read.
type Subject struct {
	// Relation is the table, or the Kubernetes object's namespaced name, the change acts on.
	Relation string `json:"relation,omitempty"`

	// Object is the thing within that relation: a column, an index, a constraint. Empty when
	// the change is about the relation itself.
	Object string `json:"object,omitempty"`
}

// FindingContext is what a production snapshot told the engine about a finding's subject.
//
// EVERY NUMBER HERE IS AN ESTIMATE derived from planner statistics, which Postgres itself keeps
// approximately and updates lazily. They exist to turn "this rewrites the table" into "this
// rewrites a table of roughly this size", which is a different and much more useful sentence —
// not to promise how long anything will take. See docs/ESTIMATES.md.
type FindingContext struct {
	// RowEstimate is the planner's row count for the subject relation. Negative means unknown;
	// Postgres reports -1 for a relation that has never been analyzed.
	RowEstimate int64 `json:"rowEstimate,omitempty"`

	// SizeBytes is the on-disk size of the subject, table or index.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// EstimatedLockDuration is a human-readable approximation such as "~14m", always rendered
	// with a leading tilde so it cannot be mistaken for a measurement.
	EstimatedLockDuration string `json:"estimatedLockDuration,omitempty"`

	// ContextNote states a fact the snapshot established, in one sentence. It is the field that
	// carries a finding the context made *worse* as well as one it merely explained.
	ContextNote string `json:"contextNote,omitempty"`

	// LockDurationBand is how long the lock is expected to be held, bucketed. Empty when no
	// band was computed — which is the case for every finding without a snapshot, and for any
	// whose size could not be established.
	//
	// Unlike the other fields here, this one has scoring consequences: see Cap.
	LockDurationBand LockDurationBand `json:"lockDurationBand,omitempty"`
}

// LockDurationBand buckets an estimated lock duration.
//
// Bands rather than numbers, because the underlying estimate is derived from planner statistics
// and a hard-coded throughput assumption. A band is the most precision that arithmetic supports,
// and scoring against a bucket means a 10% error in the estimate almost never changes a verdict.
type LockDurationBand string

// The complete set of bands, in increasing severity.
const (
	// BandNegligible is under a second: nobody will notice.
	BandNegligible LockDurationBand = "NEGLIGIBLE"

	// BandNoticeable is one to thirty seconds: visible in latency graphs, survivable.
	BandNoticeable LockDurationBand = "NOTICEABLE"

	// BandDisruptive is thirty seconds to five minutes: requests will fail.
	BandDisruptive LockDurationBand = "DISRUPTIVE"

	// BandOutage is over five minutes: this needs a maintenance window.
	BandOutage LockDurationBand = "OUTAGE"
)

// Valid reports whether b is a defined band. The zero value is not a band; it means no band was
// computed, which is different from a band of zero duration.
func (b LockDurationBand) Valid() bool {
	switch b {
	case BandNegligible, BandNoticeable, BandDisruptive, BandOutage:
		return true
	default:
		return false
	}
}

// Cap returns the best grade this band permits, or "" when the band imposes no ceiling.
//
// This is the one place production context touches scoring, and it only ever LOWERS a grade —
// makes it worse. There is no band that improves anything: a small table does not turn a C into
// a B, because the absence of evidence of a problem is not evidence of safety.
func (b LockDurationBand) Cap() Grade {
	switch b {
	case BandDisruptive:
		return GradeB
	case BandOutage:
		return GradeC
	default:
		// NEGLIGIBLE and NOTICEABLE impose nothing, and neither does an uncomputed band.
		return ""
	}
}

// SortFindings orders findings canonically by File, then Line, then RuleID.
//
// Determinism is a product requirement, not a nicety: the certificate is a merge gate, and a
// gate whose output shuffles between runs is a gate nobody trusts.
func SortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.RuleID < b.RuleID
	})
}

// WaivedFinding is a finding a policy downgraded to advisory.
//
// The finding is carried whole rather than summarised. A waiver is a decision to accept a
// specific risk, and a reader can only judge whether that decision still holds if the thing
// being accepted is still in front of them — which is also why a waived finding is reported at
// all rather than filtered out.
type WaivedFinding struct {
	Finding Finding `json:"finding"`

	// Reason is why the risk was accepted. A waiver cannot exist without one.
	Reason string `json:"reason"`

	// Expires is the last day the waiver applies, as YYYY-MM-DD. After it, the finding
	// returns on its own.
	Expires string `json:"expires"`

	// ApprovedBy is who accepted the risk, if the policy said.
	ApprovedBy string `json:"approvedBy,omitempty"`
}
