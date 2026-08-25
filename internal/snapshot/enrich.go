// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot

import (
	"fmt"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Enrich attaches production facts to the findings a snapshot can speak to.
//
// IT NEVER CHANGES A CLASSIFICATION. Reversibility and LockHazard are exactly what the analyzer
// decided, before and after; only the Context field is written. That is a stronger guarantee
// than "context may not improve a grade" — it makes the grade *identical* with and without a
// snapshot, so no configuration of the collector and no state of the database can move a
// verdict. See docs/ESTIMATES.md and CLAUDE.md §11g for why the permission to raise severity was
// not taken up: doing so needs a threshold, and thresholds are scoring weights the owner owns.
//
// A finding whose subject cannot be resolved unambiguously is left alone. Context that names the
// wrong table is worse than no context, because context is believed.
func (s *Set) Enrich(findings []domain.Finding) []domain.Finding {
	if s == nil || len(findings) == 0 {
		return findings
	}

	out := make([]domain.Finding, len(findings))
	copy(out, findings)

	for i := range out {
		before := out[i].Reversibility
		beforeLock := out[i].LockHazard

		if c := s.contextFor(out[i]); c != nil {
			out[i].Context = c
		}

		// Belt and braces. The invariant above is what the whole feature rests on, so it is
		// asserted here rather than only in a test: a future edit that reclassifies from inside
		// enrichment restores the classification instead of taking effect.
		out[i].Reversibility = before
		out[i].LockHazard = beforeLock
	}

	return out
}

// contextFor returns what the snapshot knows about one finding, or nil.
//
// The rules that gain context are exactly those listed in the session brief. A rule not named
// here gets nothing, deliberately: inventing enrichment for a rule nobody specified would be
// inventing an interpretation of that rule.
func (s *Set) contextFor(f domain.Finding) *domain.FindingContext {
	switch f.RuleID {
	case "PG006", "PG007":
		return s.typeChangeContext(f)
	case "PG017":
		return s.setNotNullContext(f)
	case "PG014", "PG015":
		return s.dropIndexContext(f)
	case "PG021":
		return s.validationContext(f)
	case "K8S003":
		return s.claimRemovalContext(f)
	case "K8S004":
		return s.storageDecreaseContext(f)
	default:
		return nil
	}
}

// typeChangeContext explains what an ALTER COLUMN TYPE rewrite actually costs. PG006 and PG007
// both rewrite the whole table; they differ in whether data is lost, not in the work done.
func (s *Set) typeChangeContext(f domain.Finding) *domain.FindingContext {
	t, ok := s.table(f.Subject.Relation)
	if !ok {
		return nil
	}

	return &domain.FindingContext{
		RowEstimate:           t.RowEstimate,
		SizeBytes:             t.TotalSizeBytes,
		EstimatedLockDuration: estimate(t.TotalSizeBytes, rewriteBytesPerSecond),
		ContextNote: fmt.Sprintf(
			"Rewrites the whole of %s: about %s rows, %s on disk including indexes.",
			qualify(t.Schema, t.Name), formatRows(t.RowEstimate), formatBytes(t.TotalSizeBytes)),
	}
}

// setNotNullContext is the highest-value check here: it turns "this takes a lock" into "this
// will fail", before it fails, using a statistic the database already keeps.
func (s *Set) setNotNullContext(f domain.Finding) *domain.FindingContext {
	t, tableOK := s.table(f.Subject.Relation)

	col, colOK := s.column(f.Subject.Relation, f.Subject.Object)
	if !colOK {
		if !tableOK {
			return nil
		}
		return &domain.FindingContext{
			RowEstimate:           t.RowEstimate,
			SizeBytes:             t.SizeBytes,
			EstimatedLockDuration: estimate(t.SizeBytes, scanBytesPerSecond),
			ContextNote: fmt.Sprintf(
				"Scans about %s rows of %s under lock. No statistics exist for column %s, so whether it contains nulls is unknown.",
				formatRows(t.RowEstimate), qualify(t.Schema, t.Name), f.Subject.Object),
		}
	}

	c := &domain.FindingContext{}
	if tableOK {
		c.RowEstimate = t.RowEstimate
		c.SizeBytes = t.SizeBytes
		c.EstimatedLockDuration = estimate(t.SizeBytes, scanBytesPerSecond)
	}

	if col.NullFraction > 0 {
		// Stated as a certainty because it is one: SET NOT NULL validates every existing row,
		// and a single null aborts it. The estimate is in how many, never in whether.
		note := fmt.Sprintf(
			"THIS MIGRATION WILL FAIL. Column %s currently contains nulls (about %s of rows), and SET NOT NULL rejects the whole statement if any row violates it. Backfill the column first.",
			qualified(f.Subject.Relation, col.Name), formatPercent(col.NullFraction))
		if tableOK && t.RowEstimate > 0 {
			note += fmt.Sprintf(" That is roughly %s rows to fix.",
				formatRows(int64(col.NullFraction*float64(t.RowEstimate))))
		}
		c.ContextNote = note
		return c
	}

	c.ContextNote = fmt.Sprintf(
		"Column %s has no nulls in the snapshot, so the constraint should validate — though any null written between the snapshot and the migration will still abort it.",
		qualified(f.Subject.Relation, col.Name))
	return c
}

// dropIndexContext reports what the index costs and whether anything reads it.
func (s *Set) dropIndexContext(f domain.Finding) *domain.FindingContext {
	idx, ok := s.index(f.Subject.Object, f.Subject.Relation)
	if !ok {
		return nil
	}

	c := &domain.FindingContext{SizeBytes: idx.SizeBytes}

	if idx.Scans == 0 {
		// Worth saying plainly: this is the one place in the whole engine where production
		// context makes a change look genuinely cheap rather than merely explained.
		note := fmt.Sprintf(
			"The planner has not used index %s once since statistics were last reset, and it occupies %s. Dropping an unused index is cheap; rebuilding it is the cost, and that cost is known.",
			idx.Name, formatBytes(idx.SizeBytes))
		if idx.StatsResetAt != nil {
			note += fmt.Sprintf(" Statistics have been running since %s.", idx.StatsResetAt.UTC().Format("2006-01-02"))
		} else {
			note += " The statistics reset time is unknown, so a zero count may only mean the counters are young."
		}
		c.ContextNote = note
		return c
	}

	c.ContextNote = fmt.Sprintf(
		"Index %s occupies %s and the planner has used it %s times since statistics were last reset, so dropping it will change query plans.",
		idx.Name, formatBytes(idx.SizeBytes), formatRows(idx.Scans))
	return c
}

// validationContext explains what validating a constraint has to read.
func (s *Set) validationContext(f domain.Finding) *domain.FindingContext {
	t, ok := s.table(f.Subject.Relation)
	if !ok {
		return nil
	}

	return &domain.FindingContext{
		RowEstimate:           t.RowEstimate,
		SizeBytes:             t.SizeBytes,
		EstimatedLockDuration: estimate(t.SizeBytes, scanBytesPerSecond),
		ContextNote: fmt.Sprintf(
			"Validating this constraint scans about %s rows of %s (%s) while holding a lock.",
			formatRows(t.RowEstimate), qualify(t.Schema, t.Name), formatBytes(t.SizeBytes)),
	}
}

// claimRemovalContext replaces the analyzer's guess at a reclaim policy with the cluster's
// answer.
//
// A Retain policy makes this materially less severe, and the finding is left IRREVERSIBLE
// anyway. That is the no-downgrade rule doing its job: the fact is recorded, the grade is not
// moved, and a human decides. Automating that decision would mean trusting a snapshot to
// authorise data loss.
func (s *Set) claimRemovalContext(f domain.Finding) *domain.FindingContext {
	if s.Kubernetes == nil {
		return nil
	}

	claim, ok := s.claim(f.Subject.Relation)
	if !ok {
		return nil
	}

	policy, policyOK := s.reclaimPolicy(claim.StorageClass)

	switch {
	case policyOK && policy == "Retain":
		return &domain.FindingContext{
			ContextNote: fmt.Sprintf(
				"The cluster reports StorageClass %q with reclaimPolicy Retain, so deleting this claim releases the volume rather than erasing it and the data can be recovered by binding a new claim to it. The finding stands: recovery is a manual operation, and no tool should grade it as reversible on your behalf.",
				claim.StorageClass),
			SizeBytes: 0,
		}

	case policyOK:
		return &domain.FindingContext{
			ContextNote: fmt.Sprintf(
				"Confirmed against the cluster: StorageClass %q has reclaimPolicy %s, so deleting this claim destroys the volume. The claim is currently %s with a capacity of %s.",
				claim.StorageClass, policy, strings.ToLower(claim.Phase), or(claim.Capacity, "an unreported size")),
		}

	default:
		return &domain.FindingContext{
			ContextNote: fmt.Sprintf(
				"The cluster has a claim %s in phase %s, but its StorageClass %q is not in the snapshot, so the reclaim policy is still unknown.",
				claim.Name, strings.ToLower(claim.Phase), claim.StorageClass),
		}
	}
}

// storageDecreaseContext reports what the volume actually is, rather than what the previous
// manifest requested.
func (s *Set) storageDecreaseContext(f domain.Finding) *domain.FindingContext {
	claim, ok := s.claim(f.Subject.Relation)
	if !ok {
		return nil
	}
	if claim.Capacity == "" {
		return &domain.FindingContext{
			ContextNote: fmt.Sprintf("The cluster reports claim %s in phase %s with no bound capacity, so there is nothing to shrink yet.",
				claim.Name, strings.ToLower(claim.Phase)),
		}
	}

	return &domain.FindingContext{
		ContextNote: fmt.Sprintf(
			"The bound volume behind %s is currently %s, which is what the request has to be measured against — not the previous manifest.",
			claim.Name, claim.Capacity),
	}
}

// ----------------------------------------------------------------------------------------
// Lookups
//
// Every one of these refuses an ambiguous match. A migration that writes an unqualified name
// means "whatever the search path resolves to", which this cannot know; guessing between two
// schemas would attach one table's size to another table's migration.
// ----------------------------------------------------------------------------------------

func (s *Set) table(relation string) (Table, bool) {
	if s.Postgres == nil || relation == "" {
		return Table{}, false
	}

	schema, name := splitQualified(relation)

	var found Table
	matches := 0
	for _, t := range s.Postgres.Tables {
		if !sameObject(schema, name, t.Schema, t.Name) {
			continue
		}
		found, matches = t, matches+1
	}

	return found, matches == 1
}

func (s *Set) column(relation, column string) (Column, bool) {
	if s.Postgres == nil || relation == "" || column == "" {
		return Column{}, false
	}

	schema, name := splitQualified(relation)

	var found Column
	matches := 0
	for _, c := range s.Postgres.Columns {
		if !sameObject(schema, name, c.Schema, c.Table) || !strings.EqualFold(c.Name, column) {
			continue
		}
		found, matches = c, matches+1
	}

	return found, matches == 1
}

// index looks an index up by its own name. The table is used only to disambiguate, because an
// index name is unique per schema rather than globally.
func (s *Set) index(name, relation string) (Index, bool) {
	if s.Postgres == nil || name == "" {
		return Index{}, false
	}

	schema, bare := splitQualified(name)
	_, table := splitQualified(relation)

	var found Index
	matches := 0
	for _, i := range s.Postgres.Indexes {
		if !sameObject(schema, bare, i.Schema, i.Name) {
			continue
		}
		if table != "" && !strings.EqualFold(table, i.Table) {
			continue
		}
		found, matches = i, matches+1
	}

	return found, matches == 1
}

func (s *Set) claim(name string) (Claim, bool) {
	if s.Kubernetes == nil || name == "" {
		return Claim{}, false
	}

	namespace, bare := splitNamespaced(name)

	var found Claim
	matches := 0
	for _, c := range s.Kubernetes.Claims {
		if !strings.EqualFold(c.Name, bare) {
			continue
		}
		if namespace != "" && !strings.EqualFold(c.Namespace, namespace) {
			continue
		}
		found, matches = c, matches+1
	}

	return found, matches == 1
}

func (s *Set) reclaimPolicy(className string) (string, bool) {
	if s.Kubernetes == nil || className == "" {
		return "", false
	}
	for _, sc := range s.Kubernetes.StorageClasses {
		if sc.Name == className {
			return sc.ReclaimPolicy, sc.ReclaimPolicy != ""
		}
	}
	return "", false
}

// sameObject matches a possibly-unqualified reference against a schema-qualified object.
//
// An unqualified reference matches on name alone, which is why the callers count matches and
// refuse anything but exactly one.
func sameObject(refSchema, refName, objSchema, objName string) bool {
	if !strings.EqualFold(refName, objName) {
		return false
	}
	if refSchema == "" {
		return true
	}
	return strings.EqualFold(refSchema, objSchema)
}

// splitNamespaced separates "namespace/name", the form Kubernetes findings use.
func splitNamespaced(s string) (namespace, name string) {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func qualified(relation, object string) string {
	if relation == "" {
		return object
	}
	return relation + "." + object
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
