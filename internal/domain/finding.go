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
