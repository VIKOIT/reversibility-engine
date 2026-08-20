// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package parser_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
)

// FuzzParse targets the cgo boundary directly.
//
// This is the only place in the repository where Go hands a buffer to C. A parser that faults
// there takes the process with it — the engine's recover boundary cannot catch a segfault — so
// the seam is worth fuzzing on its own rather than only through the analyzer above it.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"", " ", ";", "SELECT 1;",
		"DROP TABLE users;",
		"ALTER TABLE t ADD COLUMN c text, DROP COLUMN d;",
		"CREATE FUNCTION f() RETURNS text AS $$ SELECT 1 $$ LANGUAGE sql;",
		"SELECT $$unterminated",
		"/* unterminated",
		"'unterminated",
		strings.Repeat(";", 1000),
		"SELECT " + strings.Repeat("(", 200) + "1" + strings.Repeat(")", 200) + ";",
		"SELECT " + strings.Repeat("1+", 500) + "1;",
		strings.Repeat("SELECT 1;\n", 500),
		"\x00\x01\x02",
		"\xff\xfe\xfd",
		"SELECT '\x00';",
		"DROP TABLE " + strings.Repeat("Ω", 500) + ";",
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	subject := parser.NewPgQuery()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, sql string) {
		statements, err := subject.Parse(ctx, sql)

		// Failure is fine — most random bytes are not SQL. What is not fine is returning
		// statements alongside an error, which would let a caller act on a half-read file.
		if err != nil {
			if statements != nil {
				t.Errorf("Parse returned %d statements alongside an error", len(statements))
			}
			return
		}

		for i, s := range statements {
			if s.Kind == "" {
				t.Errorf("statement %d has no kind", i)
			}

			// Line numbers point users at their own file; a nonsensical one sends them
			// somewhere that does not exist.
			if s.Line < 1 {
				t.Errorf("statement %d reports line %d, want at least 1", i, s.Line)
			}
			if s.Line > countLines(sql) {
				t.Errorf("statement %d reports line %d, past the end of a %d-line input",
					i, s.Line, countLines(sql))
			}

			// The captured text is sliced out of the input, so a bad offset would produce
			// invalid UTF-8 that breaks JSON encoding downstream.
			if !utf8.ValidString(s.SQL) && utf8.ValidString(sql) {
				t.Errorf("statement %d captured invalid UTF-8 from valid input", i)
			}
		}
	})
}

func countLines(s string) int { return strings.Count(s, "\n") + 1 }
