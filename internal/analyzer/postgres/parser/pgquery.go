package parser

import (
	"context"
	"fmt"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v5"
)

// PgQuery is the SQLParser backed by github.com/pganalyze/pg_query_go/v5, which wraps the real
// PostgreSQL server parser.
//
// This is the only file in the repository that imports pg_query. Everything cgo brings with it
// — the C toolchain requirement, the glibc base image, the cross-compilation friction — is
// confined here on purpose. See ADR/0001.
type PgQuery struct{}

// NewPgQuery returns a parser backed by the PostgreSQL grammar.
func NewPgQuery() *PgQuery { return &PgQuery{} }

// Parse implements SQLParser.
//
// A failure to parse is returned as an error, never as an empty statement list. The caller
// classifies that as PG027/UNKNOWN, which grades F — there is no regex fallback and no
// best-effort mode.
func (p *PgQuery) Parse(ctx context.Context, sql string) ([]Statement, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	result, err := pg.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("parse: parser returned no result")
	}

	// Line offsets are computed once for the whole file rather than per statement, so a file
	// with hundreds of migrations does not become quadratic.
	lines := newLineIndex(sql)

	var out []Statement
	for _, raw := range result.Stmts {
		if raw == nil || raw.Stmt == nil {
			continue
		}

		// StmtLocation points at the character immediately after the previous statement's
		// terminator, which is usually the newline separating them. Attributing the statement
		// to that offset would report every statement on the line above it.
		start := skipLeadingSpace(sql, int(raw.StmtLocation))

		line := lines.lineAt(start)
		text := statementText(sql, start, int(raw.StmtLocation)+int(raw.StmtLen)-start)

		out = append(out, convert(raw.Stmt, text, line)...)
	}
	return out, nil
}

// convert lifts one parse-tree node into one or more engine-neutral statements. An
// AlterTableStmt yields one Statement per command.
func convert(node *pg.Node, text string, line int) []Statement {
	base := Statement{Kind: KindUnrecognized, SQL: text, Line: line}

	switch n := node.Node.(type) {
	case *pg.Node_DropStmt:
		return []Statement{convertDrop(n.DropStmt, base)}

	case *pg.Node_DropdbStmt:
		base.Kind = KindDropDatabase
		base.Object = n.DropdbStmt.GetDbname()
		return []Statement{base}

	case *pg.Node_TruncateStmt:
		base.Kind = KindTruncate
		base.Cascade = n.TruncateStmt.GetBehavior() == pg.DropBehavior_DROP_CASCADE
		if rels := n.TruncateStmt.GetRelations(); len(rels) > 0 {
			base.Relation = rels[0].GetRangeVar().GetRelname()
		}
		return []Statement{base}

	case *pg.Node_AlterTableStmt:
		return convertAlterTable(n.AlterTableStmt, base)

	case *pg.Node_RenameStmt:
		return []Statement{convertRename(n.RenameStmt, base)}

	case *pg.Node_DeleteStmt:
		base.Kind = KindDelete
		base.Relation = n.DeleteStmt.GetRelation().GetRelname()
		base.HasWhere = n.DeleteStmt.GetWhereClause() != nil
		return []Statement{base}

	case *pg.Node_UpdateStmt:
		base.Kind = KindUpdate
		base.Relation = n.UpdateStmt.GetRelation().GetRelname()
		base.HasWhere = n.UpdateStmt.GetWhereClause() != nil
		return []Statement{base}

	case *pg.Node_AlterSeqStmt:
		// Only RESTART is classified; any other ALTER SEQUENCE option is unlisted and must
		// stay UNRECOGNIZED rather than borrow RESTART's verdict.
		for _, opt := range n.AlterSeqStmt.GetOptions() {
			if strings.EqualFold(opt.GetDefElem().GetDefname(), "restart") {
				base.Kind = KindAlterSequenceRestart
				base.Relation = n.AlterSeqStmt.GetSequence().GetRelname()
				return []Statement{base}
			}
		}
		return []Statement{base}

	case *pg.Node_IndexStmt:
		base.Kind = KindCreateIndex
		base.Object = n.IndexStmt.GetIdxname()
		base.Relation = n.IndexStmt.GetRelation().GetRelname()
		base.Concurrent = n.IndexStmt.GetConcurrent()
		return []Statement{base}

	case *pg.Node_CreateStmt:
		base.Kind = KindCreateTable
		base.Relation = n.CreateStmt.GetRelation().GetRelname()
		base.Columns = columnsOf(n.CreateStmt.GetTableElts())
		return []Statement{base}

	case *pg.Node_ViewStmt:
		base.Kind = KindCreateView
		base.Relation = n.ViewStmt.GetView().GetRelname()
		return []Statement{base}

	case *pg.Node_CreateEnumStmt:
		base.Kind = KindCreateType
		base.Object = lastName(n.CreateEnumStmt.GetTypeName())
		return []Statement{base}

	case *pg.Node_CompositeTypeStmt:
		base.Kind = KindCreateType
		base.Object = n.CompositeTypeStmt.GetTypevar().GetRelname()
		return []Statement{base}

	case *pg.Node_CreateSchemaStmt:
		base.Kind = KindCreateSchema
		base.Object = n.CreateSchemaStmt.GetSchemaname()
		return []Statement{base}

	case *pg.Node_CreateSeqStmt:
		base.Kind = KindCreateSequence
		base.Relation = n.CreateSeqStmt.GetSequence().GetRelname()
		return []Statement{base}

	case *pg.Node_CreateExtensionStmt:
		base.Kind = KindCreateExtension
		base.Object = n.CreateExtensionStmt.GetExtname()
		return []Statement{base}

	case *pg.Node_CreateFunctionStmt:
		base.Kind = KindCreateFunction
		base.Object = lastName(n.CreateFunctionStmt.GetFuncname())
		return []Statement{base}

	case *pg.Node_CreateTrigStmt:
		base.Kind = KindCreateTrigger
		base.Object = n.CreateTrigStmt.GetTrigname()
		base.Relation = n.CreateTrigStmt.GetRelation().GetRelname()
		return []Statement{base}

	default:
		// Parsed cleanly, but the engine has no vocabulary for it. That is UNKNOWN, and
		// UNKNOWN is not safe.
		return []Statement{base}
	}
}

// convertDrop maps the many things DROP can remove onto distinct kinds, because the rule table
// grades DROP TABLE and DROP VIEW very differently.
func convertDrop(d *pg.DropStmt, base Statement) Statement {
	base.Cascade = d.GetBehavior() == pg.DropBehavior_DROP_CASCADE
	base.Concurrent = d.GetConcurrent()
	base.Object = dropTargetName(d)

	switch d.GetRemoveType() {
	case pg.ObjectType_OBJECT_TABLE:
		base.Kind = KindDropTable
	case pg.ObjectType_OBJECT_SCHEMA:
		base.Kind = KindDropSchema
	case pg.ObjectType_OBJECT_INDEX:
		base.Kind = KindDropIndex
	case pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW:
		base.Kind = KindDropView
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		base.Kind = KindDropFunction
	case pg.ObjectType_OBJECT_TRIGGER:
		base.Kind = KindDropTrigger
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		base.Kind = KindDropType
	case pg.ObjectType_OBJECT_SEQUENCE:
		base.Kind = KindDropSequence
	case pg.ObjectType_OBJECT_EXTENSION:
		base.Kind = KindDropExtension
	default:
		base.Kind = KindUnrecognized
	}
	return base
}

// convertAlterTable flattens one ALTER TABLE into one Statement per command.
func convertAlterTable(a *pg.AlterTableStmt, base Statement) []Statement {
	base.Relation = a.GetRelation().GetRelname()

	cmds := a.GetCmds()
	if len(cmds) == 0 {
		return []Statement{base}
	}

	out := make([]Statement, 0, len(cmds))
	for _, c := range cmds {
		cmd := c.GetAlterTableCmd()
		if cmd == nil {
			out = append(out, base)
			continue
		}

		s := base
		s.Cascade = cmd.GetBehavior() == pg.DropBehavior_DROP_CASCADE
		s.Object = cmd.GetName()

		switch cmd.GetSubtype() {
		case pg.AlterTableType_AT_DropColumn:
			s.Kind = KindDropColumn

		case pg.AlterTableType_AT_DropConstraint:
			s.Kind = KindDropConstraint

		case pg.AlterTableType_AT_SetNotNull:
			s.Kind = KindSetNotNull

		case pg.AlterTableType_AT_DropNotNull:
			s.Kind = KindDropNotNull

		case pg.AlterTableType_AT_ColumnDefault:
			// The parser distinguishes SET DEFAULT from DROP DEFAULT only by whether a
			// definition is attached.
			if cmd.GetDef() != nil {
				s.Kind = KindSetDefault
			} else {
				s.Kind = KindDropDefault
			}

		case pg.AlterTableType_AT_AlterColumnType:
			s.Kind = KindAlterColumnType
			if def := cmd.GetDef().GetColumnDef(); def != nil {
				s.ColumnType = typeOf(def.GetTypeName())
			}

		case pg.AlterTableType_AT_AddColumn:
			s.Kind = KindAddColumn
			if def := cmd.GetDef().GetColumnDef(); def != nil {
				s.Object = def.GetColname()
				s.ColumnType = typeOf(def.GetTypeName())
				s.NotNull, s.HasDefault, s.VolatileDefault = columnConstraints(def.GetConstraints())
			}

		case pg.AlterTableType_AT_AddConstraint:
			s.Kind = KindAddConstraint
			if con := cmd.GetDef().GetConstraint(); con != nil {
				s.Object = con.GetConname()
				s.NotValid = con.GetSkipValidation()
				s.ConstraintKind = constraintKindOf(con.GetContype())
			}

		default:
			s.Kind = KindUnrecognized
		}

		out = append(out, s)
	}
	return out
}

func convertRename(r *pg.RenameStmt, base Statement) Statement {
	base.NewName = r.GetNewname()
	base.Relation = r.GetRelation().GetRelname()

	switch r.GetRenameType() {
	case pg.ObjectType_OBJECT_TABLE:
		base.Kind = KindRenameTable
		base.Object = base.Relation
	case pg.ObjectType_OBJECT_COLUMN:
		base.Kind = KindRenameColumn
		base.Object = r.GetSubname()
	default:
		base.Kind = KindUnrecognized
	}
	return base
}

// columnConstraints reports whether an added column is NOT NULL, carries a DEFAULT, and whether
// that default has to be evaluated per row.
func columnConstraints(constraints []*pg.Node) (notNull, hasDefault, volatileDefault bool) {
	for _, c := range constraints {
		con := c.GetConstraint()
		if con == nil {
			continue
		}

		switch con.GetContype() {
		case pg.ConstrType_CONSTR_NOTNULL:
			notNull = true
		case pg.ConstrType_CONSTR_DEFAULT:
			hasDefault = true
			volatileDefault = isVolatile(con.GetRawExpr())
		}
	}
	return notNull, hasDefault, volatileDefault
}

// isVolatile reports whether a DEFAULT expression must be evaluated for every existing row.
//
// A literal is stored in the catalog and costs nothing; a function call such as
// gen_random_uuid() has to produce a distinct value per row, which rewrites the table. Anything
// that is not plainly a constant is treated as volatile, because guessing cheap and being wrong
// is the expensive direction.
func isVolatile(expr *pg.Node) bool {
	if expr == nil {
		return false
	}

	switch e := expr.Node.(type) {
	case *pg.Node_AConst:
		return false
	case *pg.Node_TypeCast:
		// 'now'::text is still a constant; now()::text is not.
		return isVolatile(e.TypeCast.GetArg())
	default:
		return true
	}
}

func constraintKindOf(t pg.ConstrType) ConstraintKind {
	switch t {
	case pg.ConstrType_CONSTR_FOREIGN:
		return ConstraintForeignKey
	case pg.ConstrType_CONSTR_CHECK:
		return ConstraintCheck
	default:
		return ConstraintOther
	}
}

// columnsOf lifts the column declarations of a CREATE TABLE, so that a later ALTER COLUMN TYPE
// in the same changeset can be resolved as widening or narrowing.
func columnsOf(elts []*pg.Node) []Column {
	var out []Column
	for _, e := range elts {
		def := e.GetColumnDef()
		if def == nil {
			continue
		}
		out = append(out, Column{Name: def.GetColname(), Type: typeOf(def.GetTypeName())})
	}
	return out
}

// typeOf normalizes a parsed type name. The parser has already resolved "bigint" to "int8", so
// no spelling table is needed here.
func typeOf(tn *pg.TypeName) *Type {
	if tn == nil {
		return nil
	}

	name := lastName(tn.GetNames())
	if name == "" {
		return nil
	}

	t := &Type{Name: name}
	for _, m := range tn.GetTypmods() {
		if c := m.GetAConst(); c != nil {
			if iv := c.GetIval(); iv != nil {
				t.Mods = append(t.Mods, iv.GetIval())
			}
		}
	}
	return t
}

// lastName returns the final element of a dotted name, dropping any pg_catalog qualifier.
func lastName(names []*pg.Node) string {
	if len(names) == 0 {
		return ""
	}
	return names[len(names)-1].GetString_().GetSval()
}

// dropTargetName extracts the object name from a DROP, which the parser represents either as a
// bare string or as a list of name parts depending on the object type.
func dropTargetName(d *pg.DropStmt) string {
	objs := d.GetObjects()
	if len(objs) == 0 {
		return ""
	}

	first := objs[0]
	if s := first.GetString_(); s != nil {
		return s.GetSval()
	}
	if l := first.GetList(); l != nil {
		return lastName(l.GetItems())
	}
	if ow := first.GetObjectWithArgs(); ow != nil {
		return lastName(ow.GetObjname())
	}
	return ""
}

// skipLeadingSpace advances an offset past whitespace, so a statement is attributed to the line
// it actually starts on.
func skipLeadingSpace(s string, offset int) int {
	if offset < 0 {
		return 0
	}
	for offset < len(s) {
		switch s[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		default:
			return offset
		}
	}
	return offset
}

// statementText slices the original source for a statement and normalizes it, so that a
// finding quotes what was written rather than a reconstruction of it.
func statementText(sql string, offset, length int) string {
	if offset < 0 || offset > len(sql) {
		return ""
	}

	end := len(sql)
	if length > 0 && offset+length <= len(sql) {
		end = offset + length
	}
	return strings.TrimSpace(sql[offset:end])
}

// lineIndex maps a byte offset to a 1-based line number.
type lineIndex struct {
	// starts[i] is the byte offset at which line i+1 begins.
	starts []int
}

func newLineIndex(s string) *lineIndex {
	idx := &lineIndex{starts: []int{0}}
	for i, r := range s {
		if r == '\n' {
			idx.starts = append(idx.starts, i+1)
		}
	}
	return idx
}

func (l *lineIndex) lineAt(offset int) int {
	// Linear from the end is fine: statements are visited in source order, and migration files
	// are small enough that a binary search would be premature.
	for i := len(l.starts) - 1; i >= 0; i-- {
		if offset >= l.starts[i] {
			return i + 1
		}
	}
	return 1
}
