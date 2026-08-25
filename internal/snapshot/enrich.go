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
// It may make a finding WORSE and may never make one better. Concretely it may do exactly two
// things: raise a classification to WILL_FAIL when production state proves the statement cannot
// apply, and attach a lock duration band that caps the grade. Both directions are guarded — a
// classification whose severity would drop is discarded rather than applied, so an edit that
// weakens a finding from inside enrichment has no effect rather than a subtle one.
//
// "Worse" and "better" are the trap in this vocabulary, so: lowering a grade means A -> B -> C
// -> F, and that is permitted. Raising one means C -> B, and that never happens here. A small
// table does not turn a C into a B; the absence of evidence of a problem is not evidence of
// safety.
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
		before := out[i]

		s.enrich(&out[i])

		// The one-way ratchet, asserted here rather than only in a test.
		if out[i].Reversibility.Severity() < before.Reversibility.Severity() {
			out[i].Reversibility = before.Reversibility
			out[i].Rationale = before.Rationale
		}

		// The lock hazard is the analyzer's alone. Context describes how long a lock is held,
		// never which lock is taken.
		out[i].LockHazard = before.LockHazard
	}

	return out
}

// enrich applies the rules that gain context, in place.
//
// The rules named here are exactly those the session specified. A rule not named gets nothing,
// deliberately: inventing enrichment for a rule nobody specified would be inventing an
// interpretation of that rule.
func (s *Set) enrich(f *domain.Finding) {
	switch f.RuleID {
	case "PG006", "PG007":
		f.Context = s.typeChangeContext(*f)
	case "PG017":
		s.setNotNull(f)
	case "PG014", "PG015":
		f.Context = s.dropIndexContext(*f)
	case "PG021":
		f.Context = s.validationContext(*f)
	case "K8S003":
		f.Context = s.claimRemovalContext(*f)
	case "K8S004":
		f.Context = s.storageDecreaseContext(*f)
	}
}

// typeChangeContext explains what an ALTER COLUMN TYPE rewrite actually costs. PG006 and PG007
// both rewrite the whole table; they differ in whether data is lost, not in the work done.
func (s *Set) typeChangeContext(f domain.Finding) *domain.FindingContext {
	t, ok := s.table(f.Subject.Relation)
	if !ok {
		return nil
	}

	size, sized := s.tableSize(t)
	if !sized {
		// Neither a recorded size nor a row width to derive one from. Guessing would be worse
		// than saying nothing, so the context is treated as absent for this finding.
		return nil
	}

	note := fmt.Sprintf("Rewrites the whole of %s: about %s rows, %s of heap",
		qualify(t.Schema, t.Name), formatRows(t.RowEstimate), formatBytes(size))
	if t.TotalSizeBytes > size {
		note += fmt.Sprintf(" and %s including indexes", formatBytes(t.TotalSizeBytes))
	}
	note += "."

	return &domain.FindingContext{
		RowEstimate:           t.RowEstimate,
		SizeBytes:             size,
		EstimatedLockDuration: estimateFor(size, f.LockHazard),
		LockDurationBand:      bandFor(size, f.LockHazard),
		ContextNote:           note,
	}
}

// setNotNull is the highest-value check in the engine: it turns "this takes a lock" into "this
// will not run", before it runs, from a statistic the database already keeps.
//
// A null in the column is not a risk to weigh. SET NOT NULL validates every existing row and a
// single violation aborts the statement and rolls back the transaction, so the classification
// becomes WILL_FAIL and the grade becomes F. Table size stops mattering at that point: the
// statement never gets far enough to hold a lock for any length of time, and printing a duration
// beside "this will not run" would be noise dressed as precision.
func (s *Set) setNotNull(f *domain.Finding) {
	t, tableOK := s.table(f.Subject.Relation)
	col, colOK := s.column(f.Subject.Relation, f.Subject.Object)

	if colOK && col.NullFraction > 0 {
		f.Reversibility = domain.ReversibilityWillFail
		f.Rationale = fmt.Sprintf(
			"NULL values exist in %s; SET NOT NULL will fail and roll back the transaction.",
			qualified(f.Subject.Relation, col.Name))

		note := fmt.Sprintf(
			"Confirmed against production: about %s of rows in %s are null.",
			formatPercent(col.NullFraction), qualified(f.Subject.Relation, col.Name))
		if tableOK && t.RowEstimate > 0 {
			note += fmt.Sprintf(" That is roughly %s rows to backfill before this migration can apply.",
				formatRows(int64(col.NullFraction*float64(t.RowEstimate))))
		}

		f.Context = &domain.FindingContext{
			RowEstimate: t.RowEstimate,
			ContextNote: note,
		}
		return
	}

	if !tableOK {
		return
	}

	size, sized := s.tableSize(t)
	if !sized {
		return
	}

	c := &domain.FindingContext{
		RowEstimate:           t.RowEstimate,
		SizeBytes:             size,
		EstimatedLockDuration: estimateFor(size, f.LockHazard),
		LockDurationBand:      bandFor(size, f.LockHazard),
	}

	if colOK {
		c.ContextNote = fmt.Sprintf(
			"Column %s has no nulls in the snapshot, so the constraint should validate — though any null written between the snapshot and the migration will still abort it. The scan covers about %s rows.",
			qualified(f.Subject.Relation, col.Name), formatRows(t.RowEstimate))
	} else {
		c.ContextNote = fmt.Sprintf(
			"Scans about %s rows of %s under lock. No statistics exist for column %s, so whether it contains nulls is unknown.",
			formatRows(t.RowEstimate), qualify(t.Schema, t.Name), f.Subject.Object)
	}

	f.Context = c
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

	size, sized := s.tableSize(t)
	if !sized {
		return nil
	}

	return &domain.FindingContext{
		RowEstimate:           t.RowEstimate,
		SizeBytes:             size,
		EstimatedLockDuration: estimateFor(size, f.LockHazard),
		LockDurationBand:      bandFor(size, f.LockHazard),
		ContextNote: fmt.Sprintf(
			"Validating this constraint scans about %s rows of %s (%s) while holding a lock.",
			formatRows(t.RowEstimate), qualify(t.Schema, t.Name), formatBytes(size)),
	}
}

// tableSize establishes how many bytes the work has to move.
//
// pg_relation_size is used when the snapshot recorded one. When it did not, the size is derived
// from the row count and the summed per-column average width — avg_width is PER COLUMN, so the
// widths of every column the snapshot knows about are added together to make a row width. One
// column's width is not a row width, and using it as one would understate the table by however
// many columns it has.
//
// When neither is available the caller is told so, and treats the context as absent for that
// finding rather than guessing. An understated size can only ever produce a milder band, which
// the caps make harmless — a milder band imposes a weaker ceiling, and a weaker ceiling can only
// leave the grade where it already was.
func (s *Set) tableSize(t Table) (int64, bool) {
	if t.SizeBytes > 0 {
		return t.SizeBytes, true
	}

	if t.RowEstimate <= 0 {
		return 0, false
	}

	width := s.rowWidth(t.Schema, t.Name)
	if width <= 0 {
		return 0, false
	}

	return t.RowEstimate * width, true
}

// rowWidth sums pg_stats.avg_width across every column of a table the snapshot carries.
//
// pg_stats only lists columns that have been analyzed, so this can understate a wide table. That
// is the safe direction: it can only shrink an estimate, and a smaller estimate can only impose
// a weaker ceiling on the grade.
func (s *Set) rowWidth(schema, name string) int64 {
	if s.Postgres == nil {
		return 0
	}

	var width int64
	for _, c := range s.Postgres.Columns {
		if strings.EqualFold(c.Schema, schema) && strings.EqualFold(c.Table, name) {
			width += int64(c.AverageWidth)
		}
	}
	return width
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
