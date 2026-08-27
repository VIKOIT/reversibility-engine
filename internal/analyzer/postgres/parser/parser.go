// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package parser

import (
	"context"
	"fmt"
	"strings"
)

// SQLParser turns SQL text into engine-neutral statements.
//
// The interface deliberately exposes no parser types. Everything the classification rules need
// is lifted into Statement here, so that swapping the implementation — for a pure-Go parser, or
// for a fake in tests — touches this package and nothing else. See ADR/0001.
type SQLParser interface {
	// Parse returns one Statement per operation in sql, in source order.
	//
	// It returns an error if the text cannot be parsed at all. Callers must treat that as
	// UNKNOWN, never as "no statements, therefore nothing risky".
	Parse(ctx context.Context, sql string) ([]Statement, error)
}

// Kind is the operation a statement performs, named in engine terms rather than parser terms.
//
// Kinds exist for operations the classification tables do not list — CREATE SEQUENCE, for
// example — because down-migration symmetry has to recognise them even though classification
// grades them UNKNOWN.
type Kind string

// The complete set of recognised operations. Anything the parser cannot place lands on
// KindUnrecognized and grades UNKNOWN.
const (
	KindUnrecognized Kind = "UNRECOGNIZED"

	KindDropTable    Kind = "DROP_TABLE"
	KindDropColumn   Kind = "DROP_COLUMN"
	KindTruncate     Kind = "TRUNCATE"
	KindDropSchema   Kind = "DROP_SCHEMA"
	KindDropDatabase Kind = "DROP_DATABASE"

	KindAlterColumnType Kind = "ALTER_COLUMN_TYPE"

	KindDelete Kind = "DELETE"
	KindUpdate Kind = "UPDATE"

	KindAlterSequenceRestart Kind = "ALTER_SEQUENCE_RESTART"

	KindDropType      Kind = "DROP_TYPE"
	KindDropSequence  Kind = "DROP_SEQUENCE"
	KindDropExtension Kind = "DROP_EXTENSION"

	KindRenameTable  Kind = "RENAME_TABLE"
	KindRenameColumn Kind = "RENAME_COLUMN"

	KindDropConstraint Kind = "DROP_CONSTRAINT"
	KindDropIndex      Kind = "DROP_INDEX"

	KindDropView     Kind = "DROP_VIEW"
	KindDropFunction Kind = "DROP_FUNCTION"
	KindDropTrigger  Kind = "DROP_TRIGGER"

	// KindDropMatView is deliberately separate from KindDropView. A materialized view holds
	// rows of its own — that is the entire distinction — so folding the two together produced a
	// verdict that told the reader the object "holds no data of its own", which was false, and
	// an undo naming the wrong object type.
	KindDropMatView Kind = "DROP_MATERIALIZED_VIEW"

	KindSetNotNull  Kind = "SET_NOT_NULL"
	KindDropNotNull Kind = "DROP_NOT_NULL"
	KindSetDefault  Kind = "SET_DEFAULT"
	KindDropDefault Kind = "DROP_DEFAULT"

	KindAddColumn     Kind = "ADD_COLUMN"
	KindAddConstraint Kind = "ADD_CONSTRAINT"

	KindCreateIndex Kind = "CREATE_INDEX"
	KindCreateTable Kind = "CREATE_TABLE"
	KindCreateView  Kind = "CREATE_VIEW"

	// KindReplaceView is CREATE OR REPLACE VIEW, and it is not KindCreateView.
	//
	// Under fail-closed the engine assumes the view already existed, because that is the only
	// reason the statement is written this way. The previous definition is then overwritten and
	// recorded nowhere, which makes the change COSTLY rather than reversible — and makes a bare
	// DROP VIEW the one undo step that must never be printed for it.
	KindReplaceView Kind = "REPLACE_VIEW"

	// KindAddConstraintUsingIndex is ADD CONSTRAINT ... UNIQUE/PRIMARY KEY USING INDEX: the
	// second half of the safe two-step pattern, promoting an index built CONCURRENTLY.
	KindAddConstraintUsingIndex Kind = "ADD_CONSTRAINT_USING_INDEX"

	// KindValidateConstraint is the second half of the other safe two-step pattern, validating
	// a constraint added NOT VALID (PG022).
	KindValidateConstraint Kind = "VALIDATE_CONSTRAINT"

	// KindGrant and KindRevoke cover privilege changes, which touch no object and no row.
	KindGrant  Kind = "GRANT"
	KindRevoke Kind = "REVOKE"

	// KindComment is COMMENT ON, which changes documentation and nothing else.
	KindComment Kind = "COMMENT"

	// Session and maintenance statements. These are what migration tooling emits around the
	// change rather than what a developer writes, and until they were classified a large class
	// of repository could not reach grade A at all: the tool's own transaction wrapper failed
	// the gate.
	KindSetVariable Kind = "SET_VARIABLE"
	KindLockTable   Kind = "LOCK_TABLE"
	KindAnalyze     Kind = "ANALYZE"
	KindVacuum      Kind = "VACUUM"
	KindVacuumFull  Kind = "VACUUM_FULL"

	// KindReplaceFunction is CREATE OR REPLACE FUNCTION, and it is not KindCreateFunction, for
	// the same reason KindReplaceView is not KindCreateView: the previous body is overwritten
	// and recorded nowhere.
	KindReplaceFunction Kind = "REPLACE_FUNCTION"

	// Materialized views. WITH DATA runs the query and WITH NO DATA does not, which is the
	// difference between a scan of the sources and a catalog entry.
	KindCreateMatView         Kind = "CREATE_MATERIALIZED_VIEW"
	KindCreateMatViewNoData   Kind = "CREATE_MATERIALIZED_VIEW_NO_DATA"
	KindRefreshMatView        Kind = "REFRESH_MATERIALIZED_VIEW"
	KindRefreshMatViewConcurr Kind = "REFRESH_MATERIALIZED_VIEW_CONCURRENTLY"

	// KindAddEnumValue is ALTER TYPE ... ADD VALUE. PostgreSQL cannot remove an enum value,
	// which makes this the one genuinely irreversible statement in the additive family.
	KindAddEnumValue Kind = "ADD_ENUM_VALUE"

	// Row-level security. Enabling adds a restriction; disabling removes a protection, which is
	// the third clause of the discriminator rather than an ordinary update.
	KindCreatePolicy Kind = "CREATE_POLICY"
	KindDropPolicy   Kind = "DROP_POLICY"
	KindEnableRLS    Kind = "ENABLE_ROW_LEVEL_SECURITY"
	KindDisableRLS   Kind = "DISABLE_ROW_LEVEL_SECURITY"

	KindRenameIndex     Kind = "RENAME_INDEX"
	KindSetRelOptions   Kind = "SET_REL_OPTIONS"
	KindSetLogged       Kind = "SET_LOGGED"
	KindAttachPartition Kind = "ATTACH_PARTITION"
	KindDetachPartition Kind = "DETACH_PARTITION"

	// KindInsert is an additive write. KindUpsert is not: ON CONFLICT DO UPDATE overwrites
	// existing rows, so it is an UPDATE wearing an INSERT's syntax.
	KindInsert          Kind = "INSERT"
	KindUpsert          Kind = "UPSERT"
	KindCreateType      Kind = "CREATE_TYPE"
	KindCreateSchema    Kind = "CREATE_SCHEMA"
	KindCreateSequence  Kind = "CREATE_SEQUENCE"
	KindCreateExtension Kind = "CREATE_EXTENSION"
	KindCreateFunction  Kind = "CREATE_FUNCTION"
	KindCreateTrigger   Kind = "CREATE_TRIGGER"
)

// ConstraintKind distinguishes the constraint types the rules care about.
type ConstraintKind string

// Constraint types. KindConstraintOther covers PRIMARY KEY, UNIQUE, EXCLUDE and anything else
// the tables do not name — all of which grade UNKNOWN.
const (
	ConstraintNone       ConstraintKind = ""
	ConstraintForeignKey ConstraintKind = "FOREIGN_KEY"
	ConstraintCheck      ConstraintKind = "CHECK"
	ConstraintOther      ConstraintKind = "OTHER"
)

// ObjectType names the kind of database object a statement creates or drops. It exists for
// down-migration symmetry, which pairs a CREATE with the DROP that reverses it.
type ObjectType string

// Object types recognised for symmetry checking.
const (
	ObjectTable     ObjectType = "TABLE"
	ObjectView      ObjectType = "VIEW"
	ObjectType_     ObjectType = "TYPE"
	ObjectIndex     ObjectType = "INDEX"
	ObjectSchema    ObjectType = "SCHEMA"
	ObjectSequence  ObjectType = "SEQUENCE"
	ObjectExtension ObjectType = "EXTENSION"
	ObjectFunction  ObjectType = "FUNCTION"
	ObjectTrigger   ObjectType = "TRIGGER"
	ObjectColumn    ObjectType = "COLUMN"
	ObjectConstrain ObjectType = "CONSTRAINT"
)

// ObjectRef identifies one database object for symmetry comparison.
type ObjectRef struct {
	Type ObjectType
	Name string
}

func (o ObjectRef) String() string { return string(o.Type) + " " + o.Name }

// Type is a column type, normalized the way PostgreSQL normalizes it: "bigint" arrives here as
// "int8", because that is what the parser resolves it to and comparing user spellings would be
// a regex by another name.
type Type struct {
	// Name is the base type without any schema qualifier, e.g. "int4", "varchar", "numeric".
	Name string

	// Mods are the type modifiers: varchar(10) has Mods [10], numeric(12,2) has [12, 2].
	Mods []int32
}

// String renders the type the way it would be written in SQL.
func (t Type) String() string {
	if len(t.Mods) == 0 {
		return t.Name
	}

	parts := make([]string, 0, len(t.Mods))
	for _, m := range t.Mods {
		parts = append(parts, fmt.Sprint(m))
	}
	return t.Name + "(" + strings.Join(parts, ",") + ")"
}

// Column is a column declared by CREATE TABLE or ADD COLUMN, recorded so that a later
// ALTER COLUMN TYPE in the same changeset can be resolved as widening or narrowing.
type Column struct {
	Name string
	Type *Type
}

// Statement is one operation, described in terms the classification rules can consume directly.
//
// A multi-command ALTER TABLE is flattened into one Statement per command: each command is a
// distinct change with its own verdict, and collapsing them would hide the destructive half of
// "ALTER TABLE t ADD COLUMN a int, DROP COLUMN b".
type Statement struct {
	Kind Kind

	// SQL is the normalized source of the whole statement.
	SQL string

	// Line is the 1-based line on which the statement starts.
	Line int

	// Relation is the table the statement acts on, where there is one.
	Relation string

	// Object is the subject of the operation: the column, constraint, index, or type name.
	Object string

	// NewName is the target of a rename.
	NewName string

	// Cascade reports that the statement carries CASCADE, whose blast radius is unbounded.
	Cascade bool

	// Concurrent reports CONCURRENTLY, which is the difference between an exclusive lock and
	// no lock at all.
	Concurrent bool

	// HasWhere separates a bounded DELETE/UPDATE from one that rewrites every row.
	HasWhere bool

	// NotValid reports that a constraint was added with NOT VALID, skipping the table scan.
	NotValid bool

	// Index is the index a constraint is promoted from, for ADD CONSTRAINT ... USING INDEX.
	Index string

	// InCTE reports that this statement's effect was carried inside a WITH clause on a SELECT.
	//
	// It changes no verdict — a DELETE in a CTE removes exactly the rows a bare DELETE would —
	// and it changes the rationale, because a reviewer scanning for destructive statements will
	// not have seen a DELETE on that line.
	InCTE bool

	// ColumnType is the type being added or converted to.
	ColumnType *Type

	// NotNull and HasDefault describe an added column.
	NotNull    bool
	HasDefault bool

	// VolatileDefault reports a DEFAULT that must be evaluated per row — the difference
	// between a catalog update and a full table rewrite.
	VolatileDefault bool

	ConstraintKind ConstraintKind

	// Columns are the columns declared by CREATE TABLE, recorded for type tracking.
	Columns []Column
}

// Creates returns the object this statement brings into existence, if any.
func (s Statement) Creates() (ObjectRef, bool) {
	switch s.Kind {
	case KindCreateTable:
		return ObjectRef{ObjectTable, s.Relation}, true
	case KindCreateView:
		return ObjectRef{ObjectView, s.Relation}, true
	case KindCreateType:
		return ObjectRef{ObjectType_, s.Object}, true
	case KindCreateIndex:
		return ObjectRef{ObjectIndex, s.Object}, true
	case KindCreateSchema:
		return ObjectRef{ObjectSchema, s.Object}, true
	case KindCreateSequence:
		return ObjectRef{ObjectSequence, s.Relation}, true
	case KindCreateExtension:
		return ObjectRef{ObjectExtension, s.Object}, true
	case KindCreateFunction:
		return ObjectRef{ObjectFunction, s.Object}, true
	case KindCreateTrigger:
		return ObjectRef{ObjectTrigger, s.Object}, true
	case KindAddColumn:
		return ObjectRef{ObjectColumn, s.Relation + "." + s.Object}, true
	case KindAddConstraint:
		return ObjectRef{ObjectConstrain, s.Relation + "." + s.Object}, true
	default:
		return ObjectRef{}, false
	}
}

// Drops returns the object this statement removes, if any.
func (s Statement) Drops() (ObjectRef, bool) {
	switch s.Kind {
	case KindDropTable:
		return ObjectRef{ObjectTable, s.Object}, true
	case KindDropView:
		return ObjectRef{ObjectView, s.Object}, true
	case KindDropType:
		return ObjectRef{ObjectType_, s.Object}, true
	case KindDropIndex:
		return ObjectRef{ObjectIndex, s.Object}, true
	case KindDropSchema:
		return ObjectRef{ObjectSchema, s.Object}, true
	case KindDropSequence:
		return ObjectRef{ObjectSequence, s.Object}, true
	case KindDropExtension:
		return ObjectRef{ObjectExtension, s.Object}, true
	case KindDropFunction:
		return ObjectRef{ObjectFunction, s.Object}, true
	case KindDropTrigger:
		return ObjectRef{ObjectTrigger, s.Object}, true
	case KindDropColumn:
		return ObjectRef{ObjectColumn, s.Relation + "." + s.Object}, true
	case KindDropConstraint:
		return ObjectRef{ObjectConstrain, s.Relation + "." + s.Object}, true
	default:
		return ObjectRef{}, false
	}
}
