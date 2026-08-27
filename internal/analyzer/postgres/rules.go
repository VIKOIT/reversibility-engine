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
		return classification{
			ruleID:        "PG010",
			reversibility: domain.ReversibilityIrreversible,
			lock:          domain.LockShort,
			rationale:     fmt.Sprintf("The prior position of sequence %s is recorded nowhere, so it cannot be restored, and subsequent inserts will collide with existing keys.", or(s.Relation, "the sequence")),
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
			rationale:     fmt.Sprintf("An unqualified %s rewrites every row in %s, and the prior values are not recorded anywhere.", verb, or(s.Relation, "the table")),
		}
	}

	return classification{
		ruleID:        "PG009",
		reversibility: domain.ReversibilityIrreversible,
		lock:          domain.LockShort,
		rationale:     fmt.Sprintf("The WHERE clause bounds how many rows of %s are touched, but the prior values of those rows are still gone.", or(s.Relation, "the table")),
	}
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
