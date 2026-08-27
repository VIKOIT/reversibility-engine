// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"fmt"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// This file is the executable form of the authoritative PostgreSQL table in docs/RULES.md §1.
//
// Every branch corresponds to exactly one row. Nothing here may be softened, extended, or
// inferred: a statement that matches no row is PG027/UNKNOWN, which grades F. If a rule seems
// wrong, the table is what changes, not this file.

// undoUnavailable marks the part of an undo command the engine cannot reconstruct from the
// migration alone — the body of a dropped view, the definition of a dropped constraint.
//
// It is written as a SQL comment so the emitted undo step stays a syntactically valid statement
// that an operator can paste, complete, and run under pressure.
const undoUnavailable = "/* original definition unavailable: recover from the down migration or schema history */"

// classification is one row of the table, resolved for a specific statement.
type classification struct {
	ruleID        string
	reversibility domain.Reversibility
	lock          domain.LockHazard
	rationale     string
	undo          domain.UndoStep
}

// classify maps a parsed statement onto exactly one rule.
//
// Precedence, per docs/SPECIFICATION.md §16.2: one finding per statement, and CASCADE (PG005)
// overrides any rule it overlays, because the set of objects CASCADE will destroy is not knowable
// from the migration text.
func classify(s parser.Statement, sch *schema) classification {
	if s.Cascade {
		return classification{
			ruleID:        "PG005",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     "CASCADE silently drops every dependent object, and which objects those are cannot be known from the migration, so the blast radius is unbounded and no undo can be written.",
		}
	}

	switch s.Kind {
	case parser.KindDropTable:
		return classification{
			ruleID:        "PG001",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("Dropping table %s destroys every row it holds; recreating the table restores the shape but not the data.", or(s.Object, "unknown")),
		}

	case parser.KindDropColumn:
		return classification{
			ruleID:        "PG002",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("Dropping column %s discards its values; re-adding the column restores the schema but leaves every row null.", qualified(s.Relation, s.Object)),
		}

	case parser.KindTruncate:
		return classification{
			ruleID:        "PG003",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("TRUNCATE removes every row of %s without logging them individually, so no down migration can put them back.", or(s.Relation, "the table")),
		}

	case parser.KindDropSchema, parser.KindDropDatabase:
		return classification{
			ruleID:        "PG004",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("Dropping %s destroys every object inside it along with their data; only a backup restore can undo this.", or(s.Object, "the schema")),
		}

	case parser.KindAlterColumnType:
		return classifyAlterColumnType(s, sch)

	case parser.KindDelete, parser.KindUpdate:
		return classifyDML(s)

	case parser.KindAlterSequenceRestart:
		// A setval() call reaches here too, and gets PG010's verdict because it has PG010's
		// effect. docs/SPECIFICATION.md §2: classification is by effect, never by statement
		// type — and a SELECT that moves a sequence is not a read.
		note := ""
		if s.InCTE || s.Object != "" && s.Relation == "" {
			note = " This is a setval() call: a SELECT node carrying a non-SELECT effect, " +
				"classified by that effect rather than by the node."
		}

		return classification{
			ruleID:        "PG010",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("The prior position of sequence %s is recorded nowhere, so it cannot be restored, and subsequent inserts will collide with existing keys.%s",
				or(s.Relation, or(s.Object, "the sequence")), note),
		}

	case parser.KindDropType, parser.KindDropSequence, parser.KindDropExtension:
		return classification{
			ruleID:        "PG011",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("Dropping %s cannot be fully undone: recreating a sequence loses its position, and recreating a type or extension cannot restore the dependents dropped with it.", or(s.Object, "the object")),
		}

	case parser.KindRenameTable, parser.KindRenameColumn:
		return classifyRename(s)

	case parser.KindDropConstraint:
		// The mandated rationale from docs/RULES.md §1.
		return classification{
			ruleID:        "PG013",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Rows violating constraint %s may be inserted before the rollback happens, making it impossible to re-add the constraint afterwards.", or(s.Object, "the constraint")),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s;", s.Relation, s.Object, undoUnavailable)),
		}

	case parser.KindDropIndex:
		return classifyDropIndex(s)

	case parser.KindDropView, parser.KindDropFunction, parser.KindDropTrigger:
		return classifyDropDerived(s)

	case parser.KindSetNotNull:
		return classification{
			ruleID:        "PG017",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockFullScan,
			rationale:     fmt.Sprintf("Adding NOT NULL to %s forces a scan of every row under lock, and the constraint cannot be re-imposed after rollback if nulls have since been written.", qualified(s.Relation, s.Object)),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", s.Relation, s.Object)),
		}

	case parser.KindAddColumn:
		return classifyAddColumn(s)

	case parser.KindAddConstraint:
		return classifyAddConstraint(s)

	case parser.KindCreateIndex:
		return classifyCreateIndex(s)

	case parser.KindCreateTable:
		return classification{
			ruleID:        "PG025",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Creating table %s takes nothing away; dropping it returns the schema to its prior state exactly.", or(s.Relation, "the table")),
			undo:          domain.UndoStep(fmt.Sprintf("DROP TABLE %s;", s.Relation)),
		}

	case parser.KindCreateView:
		return classification{
			ruleID:        "PG025",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Creating view %s takes nothing away; dropping it returns the schema to its prior state exactly.", or(s.Relation, "the view")),
			undo:          domain.UndoStep(fmt.Sprintf("DROP VIEW %s;", s.Relation)),
		}

	case parser.KindCreateType:
		return classification{
			ruleID:        "PG025",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Creating type %s takes nothing away; dropping it returns the schema to its prior state exactly.", or(s.Object, "the type")),
			undo:          domain.UndoStep(fmt.Sprintf("DROP TYPE %s;", s.Object)),
		}

	// PG028. The engine assumes the view already existed, because that is the only reason the
	// statement is written with OR REPLACE. The previous definition is then overwritten and is
	// recorded nowhere in the changeset.
	//
	// The undo is the point of this rule. A bare DROP VIEW would destroy an object that existed
	// before the migration, so what is emitted instead is an instruction naming what has to be
	// recovered and from where. An undo plan that destroys a pre-existing object is worse than
	// no undo plan.
	case parser.KindReplaceView:
		view := or(s.Relation, "the view")
		return classification{
			ruleID:        "PG028",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf(
				"CREATE OR REPLACE VIEW overwrites the previous definition of %s, which this changeset does not record. "+
					"Rolling back means restoring that definition from schema history, not dropping the view.", view),
			undo: domain.UndoStep(fmt.Sprintf(
				"-- Restore the previous definition of view %s from schema history.\n"+
					"-- It is not in this changeset, so it cannot be written here. Do NOT run DROP VIEW %s:\n"+
					"-- the view existed before this migration and dropping it would destroy it.", view, view)),
		}

	// PG029. Separate from PG016 because a materialized view holds rows of its own, which is the
	// entire distinction, and grading it as a plain view asserted the opposite.
	case parser.KindDropMatView:
		view := or(s.Object, "the materialized view")
		return classification{
			ruleID:        "PG029",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockExclusive,
			rationale: fmt.Sprintf(
				"Dropping materialized view %s destroys the rows it holds. They are derived, so a REFRESH can rebuild them — "+
					"but the rebuild may be expensive, and it will not reproduce the old contents if the sources have changed since.", view),
			undo: domain.UndoStep(fmt.Sprintf(
				"-- Recreate materialized view %s from its definition in schema history, then:\n"+
					"REFRESH MATERIALIZED VIEW %s;", view, view)),
		}

	// PG030. The second half of the safe two-step pattern: build the index CONCURRENTLY, then
	// promote it. There is no scan and no build, so charging it for either would grade the safe
	// sequence worse than the unsafe one-step form and teach people to stop using it.
	case parser.KindAddConstraintUsingIndex:
		return classification{
			ruleID:        "PG030",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf(
				"Constraint %s is promoted from index %s, which already exists, so no table scan and no index build happen here. "+
					"Dropping the constraint returns the schema to its prior state.",
				or(s.Object, "the constraint"), or(s.Index, "an existing index")),
			undo: domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", s.Relation, s.Object)),
		}

	// PG031. The second half of the other safe two-step pattern, completing PG022's NOT VALID.
	// It scans, and it takes nothing away.
	case parser.KindValidateConstraint:
		return classification{
			ruleID:        "PG031",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockFullScan,
			rationale: fmt.Sprintf(
				"Validating constraint %s scans every existing row without blocking writes, and removes nothing. "+
					"It completes the NOT VALID pattern rather than adding risk to it.", or(s.Object, "the constraint")),
			undo: domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", s.Relation, s.Object)),
		}

	// PG032. Privileges are not data and no object is touched. The reverse statement is exact,
	// which is what makes this REVERSIBLE rather than COSTLY — but the engine does not verify
	// that the reverse is actually present in the down migration, and the rationale says so.
	case parser.KindGrant, parser.KindRevoke:
		return classification{
			ruleID:        "PG032",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf(
				"A privilege change on %s alters no object and no row, and the opposite statement restores it exactly. "+
					"The engine does not verify that the opposite statement is present.", or(s.Object, "the target")),
			undo: domain.UndoStep(reverseGrant(s)),
		}

	// PG033. Overwriting a comment loses the previous text, which the changeset does not record —
	// but a comment is not an object and not data, so the overwrite principle that governs
	// PG028 deliberately stops short of it.
	// --- Tier P: what migration tooling emits around the change ---------------------------
	//
	// These graded F until they were classified, which meant a repository whose migration tool
	// wraps every change in SET lock_timeout could not reach grade A at all. The tool's own
	// boilerplate failed the gate, and no change to the migration would have fixed it.

	case parser.KindSetVariable:
		return classification{
			ruleID:        "PG034",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("Setting %s affects this session only and persists nothing; the connection ends and the setting is gone.",
				or(s.Object, "a runtime parameter")),
			undo: domain.UndoStep("-- Nothing to undo: the setting did not outlive the session."),
		}

	case parser.KindLockTable:
		return classification{
			ruleID:        "PG035",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockExclusive,
			rationale: fmt.Sprintf("LOCK TABLE takes the lock it names on %s for the rest of the transaction and changes nothing. The hazard is the lock itself, held for as long as the transaction runs.",
				or(s.Relation, "the table")),
			undo: domain.UndoStep("-- Nothing to undo: the lock was released when the transaction ended."),
		}

	case parser.KindAnalyze:
		return classification{
			ruleID:        "PG036",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("ANALYZE refreshes planner statistics for %s. No row, column, or constraint changes.",
				or(s.Relation, "the database")),
			undo: domain.UndoStep("-- Nothing to undo: statistics are derived and are refreshed again on the next ANALYZE."),
		}

	case parser.KindVacuum:
		return classification{
			ruleID:        "PG037",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("VACUUM reclaims dead tuples in %s without blocking reads or writes. It changes no live row.",
				or(s.Relation, "the database")),
			undo: domain.UndoStep("-- Nothing to undo: vacuuming removes only tuples no transaction can still see."),
		}

	case parser.KindVacuumFull:
		return classification{
			ruleID:        "PG038",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockExclusive,
			rationale: fmt.Sprintf("VACUUM FULL rewrites %s under an ACCESS EXCLUSIVE lock, blocking every reader and writer for the duration. Nothing is lost; the outage is the whole risk.",
				or(s.Relation, "the table")),
			undo: domain.UndoStep("-- Nothing to undo: the rewrite preserves every live row."),
		}

	// --- Tier H: common in hand-written migrations ----------------------------------------

	case parser.KindCreateExtension:
		return classification{
			ruleID:        "PG039",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Creating extension %s adds objects and takes none away. Note the deliberate asymmetry with PG011: dropping an extension can cascade to everything that depends on it, creating one cannot.",
				or(s.Object, "the extension")),
			undo: domain.UndoStep(fmt.Sprintf("DROP EXTENSION %s;", or(s.Object, "the_extension"))),
		}

	case parser.KindCreateSchema:
		return classification{
			ruleID:        "PG040",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("Creating schema %s takes nothing away; dropping it returns the database to its prior state exactly.",
				or(s.Object, "the schema")),
			undo: domain.UndoStep(fmt.Sprintf("DROP SCHEMA %s;", or(s.Object, "the_schema"))),
		}

	case parser.KindCreateSequence:
		return classification{
			ruleID:        "PG041",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("Creating sequence %s takes nothing away. Note that moving an existing sequence is PG010 and is not reversible; creating one is a different act.",
				or(s.Relation, "the sequence")),
			undo: domain.UndoStep(fmt.Sprintf("DROP SEQUENCE %s;", or(s.Relation, "the_sequence"))),
		}

	case parser.KindCreateFunction:
		return classification{
			ruleID:        "PG042",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("Creating function %s adds a definition where there was none; dropping it restores the prior state.",
				or(s.Object, "the function")),
			undo: domain.UndoStep(fmt.Sprintf("DROP FUNCTION %s;", or(s.Object, "the_function"))),
		}

	// PG043 is PG028's sibling and shares its reasoning exactly: OR REPLACE is written because
	// the object already exists, the previous body is overwritten, and this changeset does not
	// record it. The undo must never be a bare DROP.
	case parser.KindReplaceFunction:
		fn := or(s.Object, "the function")
		return classification{
			ruleID:        "PG043",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("CREATE OR REPLACE FUNCTION overwrites the previous body of %s, which this changeset does not record. Rolling back means restoring that body from schema history, not dropping the function.", fn),
			undo: domain.UndoStep(fmt.Sprintf(
				"-- Restore the previous body of function %s from schema history.\n"+
					"-- It is not in this changeset, so it cannot be written here. Do NOT run DROP FUNCTION %s:\n"+
					"-- the function existed before this migration and dropping it would destroy it.", fn, fn)),
		}

	case parser.KindCreateTrigger:
		return classification{
			ruleID:        "PG044",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Creating trigger %s on %s adds behaviour and removes none. It takes a SHARE ROW EXCLUSIVE lock on the table, which blocks writes but not reads.",
				or(s.Object, "the trigger"), or(s.Relation, "the table")),
			undo: domain.UndoStep(fmt.Sprintf("DROP TRIGGER %s ON %s;", or(s.Object, "the_trigger"), or(s.Relation, "the_table"))),
		}

	case parser.KindCreateMatView:
		return classification{
			ruleID:        "PG045",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockFullScan,
			rationale: fmt.Sprintf("Creating materialized view %s WITH DATA runs its query and holds a read lock on the source tables for the duration. It takes nothing away.",
				or(s.Relation, "the materialized view")),
			undo: domain.UndoStep(fmt.Sprintf("DROP MATERIALIZED VIEW %s;", or(s.Relation, "the_view"))),
		}

	case parser.KindCreateMatViewNoData:
		return classification{
			ruleID:        "PG046",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale: fmt.Sprintf("Creating materialized view %s WITH NO DATA writes a definition and reads nothing, so the sources are never scanned.",
				or(s.Relation, "the materialized view")),
			undo: domain.UndoStep(fmt.Sprintf("DROP MATERIALIZED VIEW %s;", or(s.Relation, "the_view"))),
		}

	case parser.KindRefreshMatView, parser.KindRefreshMatViewConcurr:
		lock := domain.LockExclusive
		note := "blocking every reader of the view for the duration"
		if s.Kind == parser.KindRefreshMatViewConcurr {
			lock = domain.LockFullScan
			note = "without blocking readers, at the cost of a slower rebuild"
		}
		return classification{
			ruleID:        "PG047",
			reversibility: domain.ReversibilityReversible,
			lock:          lock,
			rationale: fmt.Sprintf("Refreshing materialized view %s replaces derived rows with the sources' current contents, %s. No original data is lost, because none of it was original.",
				or(s.Relation, "the materialized view"), note),
			undo: domain.UndoStep(fmt.Sprintf("-- Nothing to restore: the contents of %s are derived. Refresh again if needed.", or(s.Relation, "the view"))),
		}

	// PG048. PostgreSQL cannot remove a value from an enum. This reached F by falling through
	// the fail-closed default before it was classified, which was the right answer with no
	// reasoning behind it, and it is the clearest case in the table for coverage over a soft
	// default.
	case parser.KindAddEnumValue:
		return classification{
			ruleID:        "PG048",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("PostgreSQL provides no way to remove a value from an enum, so adding %q to %s cannot be undone. Rolling back requires recreating the type and rewriting every column that uses it.",
				s.NewName, or(s.Object, "the type")),
		}

	// --- Tier M ---------------------------------------------------------------------------

	case parser.KindCreatePolicy:
		return classification{
			ruleID:        "PG049",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Creating policy %s on %s adds a restriction where there was none; dropping it restores the prior state.",
				or(s.Object, "the policy"), or(s.Relation, "the table")),
			undo: domain.UndoStep(fmt.Sprintf("DROP POLICY %s ON %s;", or(s.Object, "the_policy"), or(s.Relation, "the_table"))),
		}

	case parser.KindDropPolicy:
		return classification{
			ruleID:        "PG050",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Dropping policy %s removes a row-level restriction whose definition this changeset does not record. Recreating it requires recovering that definition from schema history.",
				or(s.Object, "the policy")),
		}

	case parser.KindEnableRLS:
		return classification{
			ruleID:        "PG051",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Enabling row-level security on %s adds a restriction. Disabling it again restores the prior state exactly.",
				or(s.Relation, "the table")),
			undo: domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DISABLE ROW LEVEL SECURITY;", or(s.Relation, "the_table"))),
		}

	// PG052 is graded on the THIRD CLAUSE of the discriminator, the same clause TF004 fires on:
	// a change is IRREVERSIBLE if it destroys data, destroys an identity, or destroys a recovery
	// capability a future rollback would depend on.
	//
	// Disabling RLS destroys no data and the setting is one line to restore, so neither of the
	// first two clauses applies and REVERSIBLE looks defensible. What it destroys is the
	// protection every subsequent query relied on: for as long as it is off every row is visible
	// to every role, and a rollback cannot un-read what was read. One principle, two analyzers.
	case parser.KindDisableRLS:
		return classification{
			ruleID:        "PG052",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Disabling row-level security on %s removes the protection every query against it relied on. The setting is one line to restore; the exposure while it was off is not. This is graded with removing a recovery capability, as TF004 is, rather than with an ordinary reversible change.",
				or(s.Relation, "the table")),
		}

	case parser.KindRenameIndex:
		return classification{
			ruleID:        "PG053",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Renaming index %s to %s breaks anything that names it: a planner hint, a maintenance script, a constraint promotion. The break lasts for as long as the new name is in effect.",
				or(s.Object, "the index"), or(s.NewName, "a new name")),
			undo: domain.UndoStep(fmt.Sprintf("ALTER INDEX %s RENAME TO %s;", or(s.NewName, "the_new_name"), or(s.Object, "the_old_name"))),
		}

	case parser.KindSetRelOptions:
		return classification{
			ruleID:        "PG054",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Changing storage parameters on %s overwrites their previous values, which this changeset does not record.",
				or(s.Relation, "the table")),
		}

	case parser.KindSetLogged:
		return classification{
			ruleID:        "PG055",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockTableRewrite,
			rationale: fmt.Sprintf("Switching %s between LOGGED and UNLOGGED rewrites the whole table, and the reverse rewrites it again. While a table is UNLOGGED its contents do not survive a crash.",
				or(s.Relation, "the table")),
		}

	case parser.KindAttachPartition:
		return classification{
			ruleID:        "PG056",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockFullScan,
			rationale: fmt.Sprintf("Attaching a partition to %s scans it to prove every row satisfies the partition bound, unless a matching constraint already exists. Detaching returns it to a standalone table with its rows intact.",
				or(s.Relation, "the table")),
			undo: domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s;", or(s.Relation, "the_table"), or(s.Object, "the_partition"))),
		}

	case parser.KindDetachPartition:
		lock := domain.LockShort
		if s.Concurrent {
			lock = domain.LockNone
		}
		return classification{
			ruleID:        "PG057",
			reversibility: domain.ReversibilityCostly,
			lock:          lock,
			rationale: fmt.Sprintf("Detaching a partition from %s leaves its rows intact as a standalone table, so nothing is destroyed. Re-attaching requires the partition bound, which this changeset does not record.",
				or(s.Relation, "the table")),
		}

	case parser.KindInsert:
		return classification{
			ruleID:        "PG058",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("Inserting into %s adds rows and overwrites none, so nothing is destroyed. Undoing it means deleting exactly the rows this statement added, and their keys are not derivable from the statement alone.",
				or(s.Relation, "the table")),
		}

	// PG059 carries PG009's verdict because ON CONFLICT DO UPDATE is an UPDATE: rows that
	// already existed are overwritten and their prior values are gone. Classification is by
	// effect, never by statement type (docs/SPECIFICATION.md §2), and this is an InsertStmt
	// whose effect is an update.
	case parser.KindUpsert:
		return classification{
			ruleID:        "PG059",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf("ON CONFLICT DO UPDATE overwrites rows of %s that already existed, and their prior values are gone. How many rows that is cannot be known from the statement alone: it depends on what is already in the table. This is an INSERT node carrying an UPDATE's effect and is classified by the effect.",
				or(s.Relation, "the table")),
		}

	case parser.KindComment:
		return classification{
			ruleID:        "PG033",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale: fmt.Sprintf(
				"A comment on %s is documentation: no object, no row, and no constraint changes. "+
					"Any previous comment is overwritten and is not recorded here.", or(s.Object, "the target")),
			undo: domain.UndoStep(fmt.Sprintf(
				"-- Restore the previous comment on %s, or remove it with COMMENT ON ... IS NULL.", or(s.Object, "the target"))),
		}

	case parser.KindDropNotNull:
		return classification{
			ruleID:        "PG026",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Relaxing NOT NULL on %s is a catalog-only change; note that re-imposing it costs a full table scan.", qualified(s.Relation, s.Object)),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", s.Relation, s.Object)),
		}

	case parser.KindSetDefault:
		return classification{
			ruleID:        "PG026",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Setting a default on %s is a catalog-only change that leaves existing rows untouched.", qualified(s.Relation, s.Object)),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", s.Relation, s.Object)),
		}

	case parser.KindDropDefault:
		return classification{
			ruleID:        "PG026",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Dropping the default on %s is a catalog-only change that leaves existing rows untouched.", qualified(s.Relation, s.Object)),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", s.Relation, s.Object, undoUnavailable)),
		}

	default:
		return unknown("this statement matches no rule in the PostgreSQL classification table, and an unclassified change is treated as unsafe")
	}
}

// unknown is the fail-closed verdict. Every path that cannot reach a definite answer ends here,
// and PG027 grades F.
func unknown(why string) classification {
	return classification{
		ruleID:        "PG027",
		reversibility: domain.ReversibilityUnknown,
		lock:          domain.LockExclusive,
		rationale:     why,
	}
}

func classifyAlterColumnType(s parser.Statement, sch *schema) classification {
	if s.ColumnType == nil {
		return unknown("the target type of this column conversion could not be resolved, so whether it truncates data is unknown")
	}

	from, ok := sch.columnType(s.Relation, s.Object)
	if !ok {
		return unknown(fmt.Sprintf("the prior type of %s was not declared anywhere in this changeset, so whether this conversion widens or narrows cannot be determined", qualified(s.Relation, s.Object)))
	}

	switch compareTypes(from, *s.ColumnType) {
	case widthNarrowing:
		return classification{
			ruleID:        "PG006",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockTableRewrite,
			rationale:     fmt.Sprintf("Converting %s from %s to %s narrows the type, and any value that does not fit is truncated or rejected with no record of what it was.", qualified(s.Relation, s.Object), from, *s.ColumnType),
		}

	case widthWidening:
		return classification{
			ruleID:        "PG007",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockTableRewrite,
			rationale:     fmt.Sprintf("Widening %s from %s to %s loses no values, but reverting fails once a value too large for %s has been written.", qualified(s.Relation, s.Object), from, *s.ColumnType, from),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", s.Relation, s.Object, from)),
		}

	default:
		return unknown(fmt.Sprintf("the conversion of %s from %s to %s is not one the classification table covers, so whether it loses data is unknown", qualified(s.Relation, s.Object), from, *s.ColumnType))
	}
}

func classifyDML(s parser.Statement) classification {
	verb := "DELETE"
	if s.Kind == parser.KindUpdate {
		verb = "UPDATE"
	}

	if !s.HasWhere {
		return classification{
			ruleID:        "PG008",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockExclusive,
			rationale: fmt.Sprintf("An unqualified %s rewrites every row in %s, and the prior values are not recorded anywhere.%s",
				verb, or(s.Relation, "the table"), cteNote(s)),
		}
	}

	return classification{
		ruleID:        "PG009",
		reversibility: domain.ReversibilityIrreversible,
		lock:          domain.LockShort,
		rationale: fmt.Sprintf("The WHERE clause bounds how many rows of %s are touched, but the prior values of those rows are still gone.%s",
			or(s.Relation, "the table"), cteNote(s)),
	}
}

// cteNote flags a DML whose effect was carried inside a SELECT.
//
// docs/SPECIFICATION.md §2: classification is by effect, never by statement type. The verdict is
// the same as for a bare DELETE, because the rows are equally gone — what differs is that a
// reviewer scanning the diff for destructive statements sees a line beginning with WITH, and
// nothing on it says DELETE. The note is the only place they will be told.
//
// It also exists to be read by whoever next proposes classifying SELECT as harmless.
func cteNote(s parser.Statement) string {
	if !s.InCTE {
		return ""
	}
	return " This is a data-modifying CTE: the statement is a SELECT node carrying a non-SELECT effect, " +
		"and it is classified by that effect rather than by the node."
}

// The mandated rationale from docs/RULES.md §1: a rename breaks the previous application version.
func classifyRename(s parser.Statement) classification {
	if s.Kind == parser.KindRenameTable {
		return classification{
			ruleID:        "PG012",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Renaming table %s to %s breaks the previous application version, so rolling the code back fails for as long as the schema stays renamed.", s.Object, s.NewName),
			undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", s.NewName, s.Object)),
		}
	}

	return classification{
		ruleID:        "PG012",
		reversibility: domain.ReversibilityCostly,
		lock:          domain.LockShort,
		rationale:     fmt.Sprintf("Renaming column %s to %s breaks the previous application version, so rolling the code back fails for as long as the schema stays renamed.", qualified(s.Relation, s.Object), s.NewName),
		undo:          domain.UndoStep(fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", s.Relation, s.NewName, s.Object)),
	}
}

func classifyDropIndex(s parser.Statement) classification {
	if s.Concurrent {
		return classification{
			ruleID:        "PG015",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Index %s can be rebuilt, but the rebuild is slow and queries relying on it degrade until it completes; dropping it concurrently at least takes no blocking lock.", or(s.Object, "the index")),
			undo:          domain.UndoStep(fmt.Sprintf("CREATE INDEX CONCURRENTLY %s ON %s %s;", s.Object, undoUnavailable, undoUnavailable)),
		}
	}

	return classification{
		ruleID:        "PG014",
		reversibility: domain.ReversibilityCostly,
		lock:          domain.LockExclusive,
		rationale:     fmt.Sprintf("Index %s can be rebuilt, but the non-concurrent drop takes an exclusive lock and the rebuild is slow.", or(s.Object, "the index")),
		undo:          domain.UndoStep(fmt.Sprintf("CREATE INDEX %s ON %s %s;", s.Object, undoUnavailable, undoUnavailable)),
	}
}

func classifyDropDerived(s parser.Statement) classification {
	var noun, recreate string
	switch s.Kind {
	case parser.KindDropView:
		noun, recreate = "View", "CREATE VIEW"
	case parser.KindDropFunction:
		noun, recreate = "Function", "CREATE FUNCTION"
	default:
		noun, recreate = "Trigger", "CREATE TRIGGER"
	}

	return classification{
		ruleID:        "PG016",
		reversibility: domain.ReversibilityCostly,
		lock:          domain.LockShort,
		rationale:     fmt.Sprintf("%s %s holds no data of its own, so restoring it only requires replaying its definition — but that definition has to be recovered from somewhere.", noun, or(s.Object, "the object")),
		undo:          domain.UndoStep(fmt.Sprintf("%s %s %s;", recreate, s.Object, undoUnavailable)),
	}
}

func classifyAddColumn(s parser.Statement) classification {
	undo := domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", s.Relation, s.Object))
	col := qualified(s.Relation, s.Object)

	switch {
	case s.NotNull && !s.HasDefault:
		return classification{
			ruleID:        "PG018",
			reversibility: domain.ReversibilityCostly,
			lock:          domain.LockExclusive,
			rationale:     fmt.Sprintf("Adding %s as NOT NULL with no DEFAULT fails outright on a non-empty table, which aborts the migration mid-deploy and leaves the release wedged.", col),
			undo:          undo,
		}

	case s.HasDefault && s.VolatileDefault:
		return classification{
			ruleID:        "PG019",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockTableRewrite,
			rationale:     fmt.Sprintf("Dropping %s undoes this completely, but a volatile default must be evaluated for every existing row, which rewrites the whole table.", col),
			undo:          undo,
		}

	default:
		return classification{
			ruleID:        "PG020",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Adding %s is the safest schema change available: a constant default is stored in the catalog rather than written to every row.", col),
			undo:          undo,
		}
	}
}

func classifyAddConstraint(s parser.Statement) classification {
	undo := domain.UndoStep(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", s.Relation, s.Object))

	if s.NotValid {
		return classification{
			ruleID:        "PG022",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("Constraint %s is added NOT VALID, so existing rows are not scanned and the change is both cheap and trivially reversible.", or(s.Object, "the constraint")),
			undo:          undo,
		}
	}

	// PG021 names foreign keys and check constraints only. A primary key or unique constraint
	// is not in the table, and inventing a verdict for it is exactly what §14 forbids.
	switch s.ConstraintKind {
	case parser.ConstraintForeignKey, parser.ConstraintCheck:
		return classification{
			ruleID:        "PG021",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockFullScan,
			rationale:     fmt.Sprintf("Constraint %s is reversible by dropping it, but without NOT VALID every existing row is validated under lock.", or(s.Object, "the constraint")),
			undo:          undo,
		}

	default:
		return unknown(fmt.Sprintf("constraint %s is neither a foreign key nor a check, and the classification table covers no other constraint type", or(s.Object, "of this kind")))
	}
}

func classifyCreateIndex(s parser.Statement) classification {
	if s.Concurrent {
		return classification{
			ruleID:        "PG024",
			reversibility: domain.ReversibilityReversible,
			lock:          domain.LockNone,
			rationale:     fmt.Sprintf("Index %s is built without blocking writes and is removed again by a single concurrent drop.", or(s.Object, "the index")),
			undo:          domain.UndoStep(fmt.Sprintf("DROP INDEX CONCURRENTLY %s;", s.Object)),
		}
	}

	return classification{
		ruleID:        "PG023",
		reversibility: domain.ReversibilityReversible,
		lock:          domain.LockExclusive,
		rationale:     fmt.Sprintf("Index %s is fully reversible by dropping it, but the non-concurrent build blocks writes to %s until it finishes.", or(s.Object, "the index"), or(s.Relation, "the table")),
		undo:          domain.UndoStep(fmt.Sprintf("DROP INDEX %s;", s.Object)),
	}
}

// qualified renders table.column, degrading gracefully when the parser could not supply either.
func qualified(table, column string) string {
	switch {
	case table != "" && column != "":
		return table + "." + column
	case column != "":
		return column
	case table != "":
		return table
	default:
		return "the column"
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// reverseGrant names the statement that undoes a privilege change.
//
// It states the shape rather than reconstructing the exact SQL: the privilege list, the grantee
// list, and WITH GRANT OPTION all affect what the reverse must say, and a plausible-looking
// statement that is subtly wrong is worse here than an instruction that is plainly incomplete.
func reverseGrant(s parser.Statement) string {
	target := or(s.Object, "the target")
	if s.Kind == parser.KindGrant {
		return fmt.Sprintf("-- REVOKE the privileges this statement granted on %s, from the same grantees.", target)
	}
	return fmt.Sprintf("-- GRANT back the privileges this statement revoked on %s, to the same grantees.", target)
}
