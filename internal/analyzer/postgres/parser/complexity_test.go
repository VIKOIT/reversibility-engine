package parser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// THE STACK-OVERFLOW GUARD.
//
// A chain of roughly five thousand binary operators overflows the C parser's stack. That is a
// hard process crash inside cgo — not a Go panic — so the engine's recover boundary cannot catch
// it and the whole server dies. On the webhook server it is a remote denial of service triggered
// by opening a pull request.
//
// Each input below crashed the process before this guard existed. They must now be refused,
// which the caller reports as PG027/UNKNOWN and grade F.
func TestGuardRefusesStackOverflowingInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
	}{
		{"chained addition", "SELECT " + strings.Repeat("1+", 5000) + "1;"},
		{"chained addition, larger", "SELECT " + strings.Repeat("1+", 20000) + "1;"},
		{"chained NOT", "SELECT " + strings.Repeat("NOT ", 5000) + "true;"},
		{"chained comparison", "SELECT " + strings.Repeat("1<", 5000) + "1;"},
		{"mixed operators", "SELECT " + strings.Repeat("1+2*3-", 2000) + "1;"},
		{"deep parens", "SELECT " + strings.Repeat("(", 5000) + "1" + strings.Repeat(")", 5000) + ";"},
		{"deep parens inside a real statement", "ALTER TABLE t ADD CONSTRAINT c CHECK " + strings.Repeat("(", 500) + "1" + strings.Repeat(")", 500) + ";"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The guard runs first, so this never reaches cgo. If it ever does, this test
			// takes the whole test binary down with it — which is itself the alarm.
			got, err := NewPgQuery().Parse(context.Background(), tt.sql)

			if err == nil {
				t.Fatalf("input was accepted and produced %d statements; it must be refused", len(got))
			}
			if !errors.Is(err, ErrTooComplex) {
				t.Errorf("error = %v, want ErrTooComplex", err)
			}
			if got != nil {
				t.Error("statements were returned alongside the refusal")
			}
		})
	}
}

// The guard must not refuse anything a human would actually write. A false positive here grades
// a legitimate migration F, which is how a gate loses its audience.
func TestGuardAcceptsRealisticMigrations(t *testing.T) {
	t.Parallel()

	tests := []string{
		"DROP TABLE users;",
		"ALTER TABLE orders ADD CONSTRAINT c CHECK (total >= 0 AND discount <= total);",
		"CREATE INDEX CONCURRENTLY i ON orders (status) WHERE status <> 'archived';",
		"UPDATE orders SET total = total * 1.2 WHERE created_at < now() - interval '1 year';",
		"CREATE VIEW v AS SELECT a, b, c FROM t JOIN u ON t.id = u.t_id WHERE t.x > 1 AND u.y < 2;",

		// A generously sized but ordinary statement.
		"INSERT INTO t (a) VALUES " + strings.Repeat("(1),", 500) + "(1);",

		// Many statements, each simple: the limits are per statement, so this must pass.
		strings.Repeat("ALTER TABLE t ADD COLUMN c integer DEFAULT 1 + 2;\n", 200),

		// Arithmetic inside a string is text, not structure.
		"INSERT INTO t (note) VALUES ('" + strings.Repeat("1+", 5000) + "1');",

		// The same inside a dollar-quoted function body.
		"CREATE FUNCTION f() RETURNS text AS $$ SELECT '" + strings.Repeat("+", 5000) + "' $$ LANGUAGE sql;",

		// And inside a comment.
		"-- " + strings.Repeat("1+", 5000) + "\nSELECT 1;",
		"/* " + strings.Repeat("(", 5000) + " */ SELECT 1;",
	}

	for i, sql := range tests {
		if err := guardComplexity(sql); err != nil {
			t.Errorf("case %d was refused: %v\n  input begins: %.80s", i, err, sql)
		}
	}
}

func TestGuardSkipsQuotedRegions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
	}{
		{"single quotes", "SELECT '" + strings.Repeat("+", 2000) + "';"},
		{"escaped quote inside a literal", "SELECT 'it''s " + strings.Repeat("+", 2000) + "';"},
		{"quoted identifier", `SELECT "` + strings.Repeat("+", 2000) + `" FROM t;`},
		{"dollar quoted", "SELECT $$" + strings.Repeat("+", 2000) + "$$;"},
		{"tagged dollar quote", "SELECT $body$" + strings.Repeat("(", 2000) + "$body$;"},
		{"line comment", "-- " + strings.Repeat("(", 2000) + "\nSELECT 1;"},
		{"block comment", "/*" + strings.Repeat("(", 2000) + "*/ SELECT 1;"},
		{"nested block comment", "/* a /* b " + strings.Repeat("+", 2000) + " */ c */ SELECT 1;"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := guardComplexity(tt.sql); err != nil {
				t.Errorf("guard counted structure inside a %s: %v", tt.name, err)
			}
		})
	}
}

// A multi-character operator is one token, not several. Counting "<=" as two would halve the
// effective limit for no reason.
func TestGuardCountsOperatorRunsAsOne(t *testing.T) {
	t.Parallel()

	// 400 comparisons, each written with a two-character operator.
	sql := "SELECT " + strings.Repeat("1<=", 400) + "1;"
	if err := guardComplexity(sql); err != nil {
		t.Errorf("400 two-character operators were refused: %v", err)
	}
}

// Unterminated quoting must not make the scanner run past the end of the input or loop forever.
func TestGuardTerminatesOnMalformedInput(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{
		"SELECT 'unterminated",
		`SELECT "unterminated`,
		"SELECT $$unterminated",
		"SELECT $tag$unterminated",
		"/* unterminated",
		"/* nested /* unterminated",
		"-- unterminated",
		"$",
		"$$",
		"$1$",
		"",
		"\x00\xff",
	} {
		// A hang would fail the test through the package timeout; a panic would fail it here.
		_ = guardComplexity(sql)
	}
}

// The limits are per statement, so a long file of simple statements is not penalised for its
// length while one hostile statement is still caught.
func TestGuardLimitsArePerStatement(t *testing.T) {
	t.Parallel()

	safe := strings.Repeat("SELECT "+strings.Repeat("1+", 400)+"1;\n", 20)
	if err := guardComplexity(safe); err != nil {
		t.Errorf("twenty statements of 400 operators each were refused: %v", err)
	}

	hostile := strings.Repeat("SELECT 1;\n", 20) + "SELECT " + strings.Repeat("1+", 5000) + "1;"
	if err := guardComplexity(hostile); err == nil {
		t.Error("a hostile statement hidden after simple ones was not caught")
	}
}
