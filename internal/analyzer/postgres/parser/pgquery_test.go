package parser_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
)

// parseOne is the common case: one statement in, one statement out.
func parseOne(t *testing.T, sql string) parser.Statement {
	t.Helper()

	got, err := parser.NewPgQuery().Parse(context.Background(), sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(got) != 1 {
		t.Fatalf("Parse(%q) returned %d statements, want 1", sql, len(got))
	}
	return got[0]
}

func TestParseKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sql  string
		want parser.Kind
	}{
		{"DROP TABLE users;", parser.KindDropTable},
		{"ALTER TABLE orders DROP COLUMN note;", parser.KindDropColumn},
		{"TRUNCATE TABLE audit_log;", parser.KindTruncate},
		{"DROP SCHEMA reporting;", parser.KindDropSchema},
		{"DROP DATABASE analytics;", parser.KindDropDatabase},
		{"ALTER TABLE orders ALTER COLUMN q TYPE integer;", parser.KindAlterColumnType},
		{"DELETE FROM sessions;", parser.KindDelete},
		{"UPDATE orders SET status = 'x';", parser.KindUpdate},
		{"ALTER SEQUENCE s RESTART WITH 1;", parser.KindAlterSequenceRestart},
		{"DROP TYPE order_status;", parser.KindDropType},
		{"DROP SEQUENCE s;", parser.KindDropSequence},
		{"DROP EXTENSION pg_trgm;", parser.KindDropExtension},
		{"ALTER TABLE orders RENAME TO purchase_orders;", parser.KindRenameTable},
		{"ALTER TABLE orders RENAME COLUMN a TO b;", parser.KindRenameColumn},
		{"ALTER TABLE orders DROP CONSTRAINT c;", parser.KindDropConstraint},
		{"DROP INDEX i;", parser.KindDropIndex},
		{"DROP VIEW v;", parser.KindDropView},
		{"DROP FUNCTION f(numeric);", parser.KindDropFunction},
		{"DROP TRIGGER t ON orders;", parser.KindDropTrigger},
		{"ALTER TABLE orders ALTER COLUMN c SET NOT NULL;", parser.KindSetNotNull},
		{"ALTER TABLE orders ALTER COLUMN c DROP NOT NULL;", parser.KindDropNotNull},
		{"ALTER TABLE orders ALTER COLUMN c SET DEFAULT 'x';", parser.KindSetDefault},
		{"ALTER TABLE orders ALTER COLUMN c DROP DEFAULT;", parser.KindDropDefault},
		{"ALTER TABLE orders ADD COLUMN c text;", parser.KindAddColumn},
		{"ALTER TABLE orders ADD CONSTRAINT c CHECK (total >= 0);", parser.KindAddConstraint},
		{"CREATE INDEX i ON orders (c);", parser.KindCreateIndex},
		{"CREATE TABLE t (id bigint);", parser.KindCreateTable},
		{"CREATE VIEW v AS SELECT 1;", parser.KindCreateView},
		{"CREATE TYPE s AS ENUM ('a');", parser.KindCreateType},
		{"CREATE SCHEMA reporting;", parser.KindCreateSchema},
		{"CREATE SEQUENCE s;", parser.KindCreateSequence},
		{"CREATE EXTENSION pg_trgm;", parser.KindCreateExtension},

		// Parses cleanly, but the engine has no vocabulary for it. That is UNRECOGNIZED, and
		// the caller must grade it UNKNOWN rather than assume it is harmless.
		{"GRANT SELECT ON orders TO reporting_ro;", parser.KindUnrecognized},
		{"VACUUM FULL orders;", parser.KindUnrecognized},
		{"ALTER SEQUENCE s OWNED BY orders.id;", parser.KindUnrecognized},
		{"SELECT 1;", parser.KindUnrecognized},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			t.Parallel()
			if got := parseOne(t, tt.sql).Kind; got != tt.want {
				t.Errorf("Parse(%q).Kind = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}

// The flags below are the difference between rules that share a verdict and rules that do not.
// Getting one wrong silently changes a grade.
func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		sql   string
		check func(*testing.T, parser.Statement)
	}{
		{"cascade on drop", "DROP TABLE payments CASCADE;", func(t *testing.T, s parser.Statement) {
			if !s.Cascade {
				t.Error("Cascade = false, want true")
			}
		}},
		{"no cascade by default", "DROP TABLE payments;", func(t *testing.T, s parser.Statement) {
			if s.Cascade {
				t.Error("Cascade = true, want false")
			}
		}},
		{"cascade on truncate", "TRUNCATE sessions CASCADE;", func(t *testing.T, s parser.Statement) {
			if !s.Cascade {
				t.Error("Cascade = false, want true")
			}
		}},
		{"concurrent drop index", "DROP INDEX CONCURRENTLY i;", func(t *testing.T, s parser.Statement) {
			if !s.Concurrent {
				t.Error("Concurrent = false, want true")
			}
		}},
		{"non-concurrent drop index", "DROP INDEX i;", func(t *testing.T, s parser.Statement) {
			if s.Concurrent {
				t.Error("Concurrent = true, want false")
			}
		}},
		{"concurrent create index", "CREATE INDEX CONCURRENTLY i ON t (c);", func(t *testing.T, s parser.Statement) {
			if !s.Concurrent {
				t.Error("Concurrent = false, want true")
			}
		}},
		{"delete without where", "DELETE FROM sessions;", func(t *testing.T, s parser.Statement) {
			if s.HasWhere {
				t.Error("HasWhere = true, want false")
			}
		}},
		{"delete with where", "DELETE FROM sessions WHERE a < now();", func(t *testing.T, s parser.Statement) {
			if !s.HasWhere {
				t.Error("HasWhere = false, want true")
			}
		}},
		{"update with where", "UPDATE t SET a = 1 WHERE b = 2;", func(t *testing.T, s parser.Statement) {
			if !s.HasWhere {
				t.Error("HasWhere = false, want true")
			}
		}},
		{"not valid constraint", "ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT VALID;", func(t *testing.T, s parser.Statement) {
			if !s.NotValid {
				t.Error("NotValid = false, want true")
			}
			if s.ConstraintKind != parser.ConstraintCheck {
				t.Errorf("ConstraintKind = %q, want CHECK", s.ConstraintKind)
			}
		}},
		{"validated foreign key", "ALTER TABLE t ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES u (id);", func(t *testing.T, s parser.Statement) {
			if s.NotValid {
				t.Error("NotValid = true, want false")
			}
			if s.ConstraintKind != parser.ConstraintForeignKey {
				t.Errorf("ConstraintKind = %q, want FOREIGN_KEY", s.ConstraintKind)
			}
		}},
		{"primary key is neither FK nor check", "ALTER TABLE t ADD CONSTRAINT c PRIMARY KEY (id);", func(t *testing.T, s parser.Statement) {
			if s.ConstraintKind != parser.ConstraintOther {
				t.Errorf("ConstraintKind = %q, want OTHER", s.ConstraintKind)
			}
		}},
		{"not null without default", "ALTER TABLE t ADD COLUMN c uuid NOT NULL;", func(t *testing.T, s parser.Statement) {
			if !s.NotNull || s.HasDefault {
				t.Errorf("NotNull=%v HasDefault=%v, want true/false", s.NotNull, s.HasDefault)
			}
		}},
		{"constant default is not volatile", "ALTER TABLE t ADD COLUMN c text DEFAULT 'USD';", func(t *testing.T, s parser.Statement) {
			if !s.HasDefault || s.VolatileDefault {
				t.Errorf("HasDefault=%v VolatileDefault=%v, want true/false", s.HasDefault, s.VolatileDefault)
			}
		}},
		{"function default is volatile", "ALTER TABLE t ADD COLUMN c uuid DEFAULT gen_random_uuid();", func(t *testing.T, s parser.Statement) {
			if !s.VolatileDefault {
				t.Error("VolatileDefault = false, want true")
			}
		}},
		{"cast of a literal is not volatile", "ALTER TABLE t ADD COLUMN c timestamptz DEFAULT '2020-01-01'::timestamptz;", func(t *testing.T, s parser.Statement) {
			if s.VolatileDefault {
				t.Error("VolatileDefault = true for a cast literal, want false")
			}
		}},
		{"cast of a function is volatile", "ALTER TABLE t ADD COLUMN c date DEFAULT now()::date;", func(t *testing.T, s parser.Statement) {
			if !s.VolatileDefault {
				t.Error("VolatileDefault = false for a cast function call, want true")
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.check(t, parseOne(t, tt.sql))
		})
	}
}

func TestParseTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sql  string
		want string
	}{
		// The parser resolves user spellings to catalog names, which is why no spelling table
		// is needed anywhere in this repository.
		{"ALTER TABLE t ALTER COLUMN c TYPE bigint;", "int8"},
		{"ALTER TABLE t ALTER COLUMN c TYPE integer;", "int4"},
		{"ALTER TABLE t ALTER COLUMN c TYPE smallint;", "int2"},
		{"ALTER TABLE t ALTER COLUMN c TYPE text;", "text"},
		{"ALTER TABLE t ALTER COLUMN c TYPE varchar(10);", "varchar(10)"},
		{"ALTER TABLE t ALTER COLUMN c TYPE numeric(12,2);", "numeric(12,2)"},
		{"ALTER TABLE t ALTER COLUMN c TYPE timestamptz;", "timestamptz"},
		{"ALTER TABLE t ALTER COLUMN c TYPE date;", "date"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			s := parseOne(t, tt.sql)
			if s.ColumnType == nil {
				t.Fatalf("ColumnType is nil for %q", tt.sql)
			}
			if got := s.ColumnType.String(); got != tt.want {
				t.Errorf("ColumnType = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRecordsCreateTableColumns(t *testing.T) {
	t.Parallel()

	s := parseOne(t, "CREATE TABLE orders (id bigint PRIMARY KEY, quantity integer NOT NULL, note text);")

	if len(s.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(s.Columns))
	}

	want := map[string]string{"id": "int8", "quantity": "int4", "note": "text"}
	for _, c := range s.Columns {
		if c.Type == nil {
			t.Errorf("column %s has no type", c.Name)
			continue
		}
		if got := c.Type.String(); got != want[c.Name] {
			t.Errorf("column %s type = %q, want %q", c.Name, got, want[c.Name])
		}
	}
}

// A statement must be attributed to the line it starts on. The parser reports an offset that
// sits on the preceding newline, so this is the regression guard for that correction.
func TestParseLineNumbers(t *testing.T) {
	t.Parallel()

	sql := "DROP TABLE a;\nDROP TABLE b;\n\n\nDROP TABLE c;\n   DROP TABLE d;"

	got, err := parser.NewPgQuery().Parse(context.Background(), sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d statements, want 4", len(got))
	}

	for i, want := range []int{1, 2, 5, 6} {
		if got[i].Line != want {
			t.Errorf("statement %d (%q) reported line %d, want %d", i, got[i].SQL, got[i].Line, want)
		}
	}
}

// The captured text is the statement without its terminator — that is the extent the parser
// reports — and leading indentation is stripped so that formatting cannot change a certificate.
func TestParseStatementText(t *testing.T) {
	t.Parallel()

	got, err := parser.NewPgQuery().Parse(context.Background(), "DROP TABLE a;\n  TRUNCATE b;\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for i, want := range []string{"DROP TABLE a", "TRUNCATE b"} {
		if got[i].SQL != want {
			t.Errorf("statement %d SQL = %q, want %q", i, got[i].SQL, want)
		}
	}
}

// A multi-command ALTER TABLE must not collapse into one verdict, or the destructive half of
// "ADD COLUMN a, DROP COLUMN b" would be hidden behind the harmless half.
func TestParseFlattensMultiCommandAlter(t *testing.T) {
	t.Parallel()

	got, err := parser.NewPgQuery().Parse(context.Background(),
		"ALTER TABLE orders ADD COLUMN a text, DROP COLUMN b;")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	if got[0].Kind != parser.KindAddColumn {
		t.Errorf("first command = %q, want ADD_COLUMN", got[0].Kind)
	}
	if got[1].Kind != parser.KindDropColumn {
		t.Errorf("second command = %q, want DROP_COLUMN", got[1].Kind)
	}
	for i, s := range got {
		if s.Line != 1 {
			t.Errorf("command %d reported line %d, want 1", i, s.Line)
		}
		if s.Relation != "orders" {
			t.Errorf("command %d relation = %q, want orders", i, s.Relation)
		}
	}
}

// Malformed input must be an error, never an empty statement list that a caller could mistake
// for "this file contains nothing risky".
func TestParseRejectsMalformedSQL(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{
		"ALTER TABLE orders FLARBLE COLUMN quantity;",
		"DROP TABLE;",
		"SELECT * FROM (;",
		")))",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			got, err := parser.NewPgQuery().Parse(context.Background(), sql)
			if err == nil {
				t.Errorf("Parse(%q) returned nil error and %d statements", sql, len(got))
			}
			if got != nil {
				t.Errorf("Parse(%q) returned statements alongside an error", sql)
			}
		})
	}
}

// Empty input is not malformed. It simply has no statements, and the caller decides what that
// means rather than the parser inventing an error.
func TestParseEmptyInput(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{"", "   \n\t\n", "-- just a comment\n"} {
		got, err := parser.NewPgQuery().Parse(context.Background(), sql)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", sql, err)
		}
		if len(got) != 0 {
			t.Errorf("Parse(%q) returned %d statements, want 0", sql, len(got))
		}
	}
}

func TestParseRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := parser.NewPgQuery().Parse(ctx, "DROP TABLE t;"); err == nil {
		t.Fatal("Parse with a cancelled context returned nil error")
	}
}

// Dollar-quoted bodies are the classic reason a regex cannot do this job: the words inside look
// exactly like destructive DDL and are not.
func TestParseDoesNotSeeIntoStringLiterals(t *testing.T) {
	t.Parallel()

	got, err := parser.NewPgQuery().Parse(context.Background(),
		"CREATE FUNCTION f() RETURNS text AS $$ SELECT 'DROP TABLE users' $$ LANGUAGE sql;")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, s := range got {
		if s.Kind == parser.KindDropTable {
			t.Errorf("a DROP TABLE inside a string literal was classified as a real drop: %q", s.SQL)
		}
	}
}

func TestObjectRefsForSymmetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sql     string
		creates string
		drops   string
	}{
		{"CREATE TABLE t (id bigint);", "TABLE t", ""},
		{"DROP TABLE t;", "", "TABLE t"},
		{"CREATE INDEX i ON t (c);", "INDEX i", ""},
		{"DROP INDEX i;", "", "INDEX i"},
		{"CREATE VIEW v AS SELECT 1;", "VIEW v", ""},
		{"DROP VIEW v;", "", "VIEW v"},
		{"ALTER TABLE t ADD COLUMN c text;", "COLUMN t.c", ""},
		{"ALTER TABLE t DROP COLUMN c;", "", "COLUMN t.c"},
		{"TRUNCATE t;", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			t.Parallel()

			s := parseOne(t, tt.sql)

			created, hasCreate := s.Creates()
			if tt.creates == "" {
				if hasCreate {
					t.Errorf("Creates() = %q, want none", created)
				}
			} else if !hasCreate || created.String() != tt.creates {
				t.Errorf("Creates() = %q/%v, want %q", created, hasCreate, tt.creates)
			}

			dropped, hasDrop := s.Drops()
			if tt.drops == "" {
				if hasDrop {
					t.Errorf("Drops() = %q, want none", dropped)
				}
			} else if !hasDrop || dropped.String() != tt.drops {
				t.Errorf("Drops() = %q/%v, want %q", dropped, hasDrop, tt.drops)
			}
		})
	}
}

func TestTypeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   parser.Type
		want string
	}{
		{parser.Type{Name: "int8"}, "int8"},
		{parser.Type{Name: "varchar", Mods: []int32{10}}, "varchar(10)"},
		{parser.Type{Name: "numeric", Mods: []int32{12, 2}}, "numeric(12,2)"},
	}

	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Type%+v.String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A migration that is one enormous statement must not blow the statement bound or the line
// index. This is the shape that breaks naive slicing.
func TestParseHandlesLargeInput(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("DROP TABLE t;\n")
	}

	got, err := parser.NewPgQuery().Parse(context.Background(), b.String())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 500 {
		t.Fatalf("got %d statements, want 500", len(got))
	}
	if got[499].Line != 500 {
		t.Errorf("last statement reported line %d, want 500", got[499].Line)
	}
}
