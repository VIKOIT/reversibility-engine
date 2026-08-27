// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

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

// utf8BOM is the byte-order mark that Windows editors and PowerShell redirection prepend to
// UTF-8 files.
//
// PostgreSQL's grammar has no production for it, so three invisible bytes make an otherwise
// ordinary migration unparseable. Fail-closed still grades that F, so nothing unsafe merges —
// but the reviewer is told "this file could not be parsed" when the truth is "this file drops a
// column". A verdict that misdescribes the change is a worse artifact than one that names the
// rule, and on Windows-authored migrations it would be the common case rather than the rare one.
const utf8BOM = "\uFEFF"

// Parse implements SQLParser.
//
// A failure to parse is returned as an error, never as an empty statement list. The caller
// classifies that as PG027/UNKNOWN, which grades F — there is no regex fallback and no
// best-effort mode.
func (p *PgQuery) Parse(ctx context.Context, sql string) ([]Statement, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	// Removing the BOM is a decoding step, not a parsing concession: it drops a marker that
	// encodes no SQL and leaves every token untouched. It happens before the complexity guard
	// and before any offset is computed, so statement locations and line numbers are measured
	// against the same string the grammar sees.
	//
	// Only the UTF-8 BOM is handled. A UTF-16 file — what PowerShell 5.1's ">" produces — is
	// left to fail, because recovering it means transcoding the whole input, and guessing at an
	// encoding is exactly the kind of inference this engine does not make.
	sql = strings.TrimPrefix(sql, utf8BOM)

	// Structure is checked before the bytes reach cgo. The PostgreSQL grammar is recursive
	// descent on a C stack, and a long enough chain of operators overflows it — a hard process
	// crash that no recover() can catch. This is the only defence that works, so it runs first.
	if err := guardComplexity(sql); err != nil {
		return nil, err
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

	case *pg.Node_SelectStmt:
		// docs/SPECIFICATION.md §2: classification is by effect, never by statement type.
		//
		// A SelectStmt is not a read. It can carry a data-modifying CTE, which deletes or
		// updates rows, and it can call a function with side effects. Both are classified by
		// what they do; everything else stays UNRECOGNIZED, and no permissive default is
		// added, because the generalisation that would justify one is the wrong one.
		return []Statement{convertSelect(n.SelectStmt, base)}

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
		// Replace is the whole of D1. Without reading it, CREATE OR REPLACE VIEW was
		// indistinguishable from CREATE VIEW, graded REVERSIBLE, and had DROP VIEW printed as
		// its undo — a step that destroys the view the statement had just overwritten.
		if n.ViewStmt.GetReplace() {
			base.Kind = KindReplaceView
		} else {
			base.Kind = KindCreateView
		}
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
		// Same distinction as CREATE OR REPLACE VIEW, for the same reason: the previous body is
		// overwritten and this changeset does not record it.
		if n.CreateFunctionStmt.GetReplace() {
			base.Kind = KindReplaceFunction
		} else {
			base.Kind = KindCreateFunction
		}
		base.Object = lastName(n.CreateFunctionStmt.GetFuncname())
		return []Statement{base}

	case *pg.Node_CreateTrigStmt:
		base.Kind = KindCreateTrigger
		base.Object = n.CreateTrigStmt.GetTrigname()
		base.Relation = n.CreateTrigStmt.GetRelation().GetRelname()
		return []Statement{base}

	case *pg.Node_GrantStmt:
		// One node covers both directions; is_grant is what separates them.
		if n.GrantStmt.GetIsGrant() {
			base.Kind = KindGrant
		} else {
			base.Kind = KindRevoke
		}
		base.Object = grantTargetName(n.GrantStmt)
		return []Statement{base}

	case *pg.Node_CommentStmt:
		base.Kind = KindComment
		base.Object = commentTargetName(n.CommentStmt)
		return []Statement{base}

	case *pg.Node_VariableSetStmt:
		base.Kind = KindSetVariable
		base.Object = n.VariableSetStmt.GetName()
		return []Statement{base}

	case *pg.Node_LockStmt:
		base.Kind = KindLockTable
		if rels := n.LockStmt.GetRelations(); len(rels) > 0 {
			base.Relation = rels[0].GetRangeVar().GetRelname()
		}
		return []Statement{base}

	case *pg.Node_VacuumStmt:
		// One node covers ANALYZE, VACUUM and VACUUM FULL, and they are three different lock
		// hazards. is_vacuumcmd separates ANALYZE from the rest; the FULL option separates a
		// rewrite under ACCESS EXCLUSIVE from an ordinary vacuum that blocks nothing.
		switch {
		case !n.VacuumStmt.GetIsVacuumcmd():
			base.Kind = KindAnalyze
		case hasVacuumOption(n.VacuumStmt, "full"):
			base.Kind = KindVacuumFull
		default:
			base.Kind = KindVacuum
		}
		if rels := n.VacuumStmt.GetRels(); len(rels) > 0 {
			base.Relation = rels[0].GetVacuumRelation().GetRelation().GetRelname()
		}
		return []Statement{base}

	case *pg.Node_CreateTableAsStmt:
		// CREATE MATERIALIZED VIEW and CREATE TABLE ... AS share this node; objtype separates
		// them. skipData is WITH NO DATA, which is the difference between scanning the sources
		// and writing a catalog entry.
		base.Relation = n.CreateTableAsStmt.GetInto().GetRel().GetRelname()

		switch {
		case n.CreateTableAsStmt.GetObjtype() != pg.ObjectType_OBJECT_MATVIEW:
			// CREATE TABLE ... AS SELECT is not classified: it creates a table and copies rows
			// in one statement, and no row in the table covers it. UNRECOGNIZED, so it fails
			// closed rather than borrowing CREATE TABLE's verdict.
			base.Kind = KindUnrecognized
		case n.CreateTableAsStmt.GetInto().GetSkipData():
			base.Kind = KindCreateMatViewNoData
		default:
			base.Kind = KindCreateMatView
		}
		return []Statement{base}

	case *pg.Node_RefreshMatViewStmt:
		if n.RefreshMatViewStmt.GetConcurrent() {
			base.Kind = KindRefreshMatViewConcurr
		} else {
			base.Kind = KindRefreshMatView
		}
		base.Relation = n.RefreshMatViewStmt.GetRelation().GetRelname()
		return []Statement{base}

	case *pg.Node_AlterEnumStmt:
		// Only ADD VALUE is classified. Renaming a value is a different statement with a
		// different answer, and it stays UNRECOGNIZED rather than borrowing this verdict.
		if n.AlterEnumStmt.GetOldVal() == "" {
			base.Kind = KindAddEnumValue
			base.Object = lastName(n.AlterEnumStmt.GetTypeName())
			base.NewName = n.AlterEnumStmt.GetNewVal()
		}
		return []Statement{base}

	case *pg.Node_CreatePolicyStmt:
		base.Kind = KindCreatePolicy
		base.Object = n.CreatePolicyStmt.GetPolicyName()
		base.Relation = n.CreatePolicyStmt.GetTable().GetRelname()
		return []Statement{base}

	case *pg.Node_InsertStmt:
		// ON CONFLICT DO UPDATE overwrites rows that already exist, so it is an UPDATE wearing
		// an INSERT's syntax and is classified by that effect. DO NOTHING writes nothing on
		// conflict and stays an ordinary insert.
		base.Relation = n.InsertStmt.GetRelation().GetRelname()
		if oc := n.InsertStmt.GetOnConflictClause(); oc != nil &&
			oc.GetAction() == pg.OnConflictAction_ONCONFLICT_UPDATE {
			base.Kind = KindUpsert
		} else {
			base.Kind = KindInsert
		}
		return []Statement{base}

	default:
		// Parsed cleanly, but the engine has no vocabulary for it. That is UNKNOWN, and
		// UNKNOWN is not safe.
		return []Statement{base}
	}
}

// convertDrop maps the many things DROP can remove onto distinct kinds, because the rule table
// grades DROP TABLE and DROP VIEW very differently.
// sequenceResetFuncs are functions that move a sequence's position. They are the PG010 hazard
// wearing a function call: the previous position is recorded nowhere and cannot be restored.
var sequenceResetFuncs = map[string]bool{"setval": true}

// convertSelect classifies a SELECT by what it does rather than by what it is.
//
// Three outcomes, in precedence order. A data-modifying CTE is the DML it contains, because the
// rows it removes are as gone as any other DELETE's. A call to a sequence-moving function is
// KindAlterSequenceRestart, because the effect is identical to ALTER SEQUENCE ... RESTART. And
// anything else is UNRECOGNIZED, which grades UNKNOWN — the fail-closed default, deliberately
// left in place.
//
// Precedence matters: a CTE that deletes rows outranks a setval in the same statement, because
// the destroyed rows are the larger loss and a finding names one thing.
func convertSelect(s *pg.SelectStmt, base Statement) Statement {
	if dml := dataModifyingCTE(s.GetWithClause(), base); dml != nil {
		return *dml
	}

	if name, ok := callsSideEffectingFunc(s); ok {
		base.Kind = KindAlterSequenceRestart
		base.Object = name
		return base
	}

	return base
}

// dataModifyingCTE reports the DML hidden in a WITH clause, if there is one.
//
// PostgreSQL allows DELETE, UPDATE and INSERT inside a CTE, and the parse tree reports the whole
// thing as a SelectStmt. Reading only the outer node is how a DELETE becomes invisible.
func dataModifyingCTE(with *pg.WithClause, base Statement) *Statement {
	if with == nil {
		return nil
	}

	for _, cte := range with.GetCtes() {
		query := cte.GetCommonTableExpr().GetCtequery()
		if query == nil {
			continue
		}

		switch n := query.Node.(type) {
		case *pg.Node_DeleteStmt:
			s := base
			s.Kind = KindDelete
			s.Relation = n.DeleteStmt.GetRelation().GetRelname()
			s.HasWhere = n.DeleteStmt.GetWhereClause() != nil
			s.InCTE = true
			return &s

		case *pg.Node_UpdateStmt:
			s := base
			s.Kind = KindUpdate
			s.Relation = n.UpdateStmt.GetRelation().GetRelname()
			s.HasWhere = n.UpdateStmt.GetWhereClause() != nil
			s.InCTE = true
			return &s

		case *pg.Node_InsertStmt:
			// Reported so the effect is named, and left UNRECOGNIZED so it fails closed:
			// INSERT has no row in the table yet, and borrowing DELETE's verdict for it would
			// be inventing a classification rather than reading one.
			s := base
			s.Relation = n.InsertStmt.GetRelation().GetRelname()
			s.InCTE = true
			return &s
		}
	}

	return nil
}

// callsSideEffectingFunc reports a call to a function whose effect the table classifies.
//
// It walks the target list only. A setval buried in a WHERE clause or a subquery is not found,
// and that is the honest limit of this check rather than a gap to paper over: what is not
// recognised stays UNRECOGNIZED and grades UNKNOWN.
func callsSideEffectingFunc(s *pg.SelectStmt) (string, bool) {
	for _, target := range s.GetTargetList() {
		call := target.GetResTarget().GetVal().GetFuncCall()
		if call == nil {
			continue
		}
		if name := lastName(call.GetFuncname()); sequenceResetFuncs[strings.ToLower(name)] {
			return sequenceArgument(call), true
		}
	}
	return "", false
}

// sequenceArgument names the sequence a setval moves, from its first argument.
func sequenceArgument(call *pg.FuncCall) string {
	for _, arg := range call.GetArgs() {
		if s := arg.GetAConst().GetSval(); s != nil {
			return s.GetSval()
		}
	}
	return ""
}

// hasVacuumOption reports whether a VACUUM carries the named option, e.g. "full".
//
// Options are a name list rather than flags, so this is a lookup and not a bitmask. An option
// this does not recognise leaves the statement an ordinary VACUUM, which is the conservative
// answer for lock hazard: FULL is the only option that escalates it.
func hasVacuumOption(v *pg.VacuumStmt, name string) bool {
	for _, opt := range v.GetOptions() {
		if strings.EqualFold(opt.GetDefElem().GetDefname(), name) {
			return true
		}
	}
	return false
}

// grantTargetName names what a GRANT or REVOKE applies to, for the finding text.
//
// Best effort by design: the privilege target can be a list, a whole schema, or every table in
// one, and the verdict does not depend on which. An empty result renders as a generic phrase
// rather than as a wrong one.
func grantTargetName(g *pg.GrantStmt) string {
	for _, obj := range g.GetObjects() {
		if r := obj.GetRangeVar(); r != nil && r.GetRelname() != "" {
			return r.GetRelname()
		}
		if name := lastName([]*pg.Node{obj}); name != "" {
			return name
		}
	}
	return ""
}

// commentTargetName names the object a COMMENT ON applies to.
func commentTargetName(c *pg.CommentStmt) string {
	if obj := c.GetObject(); obj != nil {
		if r := obj.GetRangeVar(); r != nil && r.GetRelname() != "" {
			return r.GetRelname()
		}
		if name := lastName([]*pg.Node{obj}); name != "" {
			return name
		}
	}
	return ""
}

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
	case pg.ObjectType_OBJECT_VIEW:
		base.Kind = KindDropView
	case pg.ObjectType_OBJECT_MATVIEW:
		// Separate from OBJECT_VIEW: a materialized view holds rows, and grading it as a plain
		// view told the reader it held none. See D2.
		base.Kind = KindDropMatView
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
	case pg.ObjectType_OBJECT_POLICY:
		base.Kind = KindDropPolicy
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

				// USING INDEX promotes an index that already exists, so there is no scan and no
				// build — it is the second half of the safe two-step pattern, and grading it as
				// a plain ADD CONSTRAINT charged it for work it does not do.
				if con.GetIndexname() != "" {
					s.Kind = KindAddConstraintUsingIndex
					s.Index = con.GetIndexname()
				}
			}

		case pg.AlterTableType_AT_ValidateConstraint:
			s.Kind = KindValidateConstraint

		case pg.AlterTableType_AT_EnableRowSecurity:
			s.Kind = KindEnableRLS

		case pg.AlterTableType_AT_DisableRowSecurity:
			s.Kind = KindDisableRLS

		case pg.AlterTableType_AT_SetRelOptions, pg.AlterTableType_AT_ResetRelOptions:
			s.Kind = KindSetRelOptions

		case pg.AlterTableType_AT_SetLogged, pg.AlterTableType_AT_SetUnLogged:
			s.Kind = KindSetLogged

		case pg.AlterTableType_AT_AttachPartition:
			s.Kind = KindAttachPartition

		case pg.AlterTableType_AT_DetachPartition:
			s.Kind = KindDetachPartition
			s.Concurrent = cmd.GetDef().GetPartitionCmd().GetConcurrent()

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
	case pg.ObjectType_OBJECT_INDEX:
		base.Kind = KindRenameIndex
		base.Object = base.Relation
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
