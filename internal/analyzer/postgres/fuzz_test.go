// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// seedCorpus covers the shapes most likely to break a parser wrapper: valid SQL, truncated SQL,
// unbalanced delimiters, dollar quoting, deep nesting, and non-UTF-8 bytes.
var seedCorpus = []string{
	"",
	" ",
	";",
	";;;;",
	"DROP TABLE users;",
	"DROP TABLE users",
	"DROP TABLE",
	"DROP",
	"ALTER TABLE orders ALTER COLUMN quantity TYPE integer;",
	"CREATE TABLE t (id bigint PRIMARY KEY);",
	"CREATE INDEX CONCURRENTLY i ON t (c);",
	"DELETE FROM sessions WHERE a < now();",
	"ALTER TABLE t ADD COLUMN c uuid DEFAULT gen_random_uuid();",
	"DROP TABLE payments CASCADE;",
	"GRANT SELECT ON orders TO ro;",

	// Dollar quoting: the classic reason a regex cannot do this job.
	"CREATE FUNCTION f() RETURNS text AS $$ SELECT 'DROP TABLE users' $$ LANGUAGE sql;",
	"SELECT $tag$ DROP TABLE x; $tag$;",
	"SELECT $$ unterminated",

	// Comments, including an unterminated block comment.
	"-- DROP TABLE users;\nSELECT 1;",
	"/* DROP TABLE users; */ SELECT 1;",
	"/* unterminated",

	// Unbalanced delimiters and hostile punctuation.
	"(((((",
	")))))",
	"'''",
	`"""`,
	"SELECT * FROM (;",
	"ALTER TABLE orders FLARBLE COLUMN quantity;",

	// Deep nesting: the recursive-descent path, and the one that faults on musl.
	"SELECT " + strings.Repeat("(", 100) + "1" + strings.Repeat(")", 100) + ";",

	// Bytes that are not text.
	"\x00\x01\x02",
	"\xff\xfe\xfd",
	"DROP TABLE \x00users;",

	// Multi-byte identifiers, which truncation must not split.
	"DROP TABLE \"таблица\";",
	"DROP TABLE " + strings.Repeat("Ω", 300) + ";",
}

// FuzzAnalyze asserts the two properties CLAUDE.md §13 requires of the SQL analyzer: it must
// never panic, and it must never call malformed input REVERSIBLE.
//
// The second is the one that matters. A crash is loud and gets fixed; a confident "safe" verdict
// on SQL nobody could parse is the failure that ends the product.
func FuzzAnalyze(f *testing.F) {
	for _, seed := range seedCorpus {
		f.Add(seed)
	}

	subject := postgres.New()
	sqlParser := parser.NewPgQuery()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, sql string) {
		files := []domain.ChangedFile{{
			Path:    "migrations/0001_fuzz.up.sql",
			Status:  domain.StatusAdded,
			Current: []byte(sql),
		}}

		// A panic here fails the test by escaping; the analyzer is library code and owns no
		// recover of its own.
		findings, err := subject.Analyze(ctx, files)
		if err != nil {
			t.Fatalf("Analyze returned an error rather than an UNKNOWN finding: %v", err)
		}

		// If the parser rejected the input, nothing in the file was understood, so every
		// finding must be UNKNOWN. Anything else is the engine vouching for SQL it could not
		// read.
		_, parseErr := sqlParser.Parse(ctx, sql)

		for _, finding := range findings {
			assertWellFormed(t, finding)

			if parseErr != nil {
				if finding.Reversibility != domain.ReversibilityUnknown {
					t.Errorf("unparseable input produced %s/%s; malformed SQL must never be classified",
						finding.RuleID, finding.Reversibility)
				}
				if finding.RuleID != "PG027" {
					t.Errorf("unparseable input produced rule %s, want PG027", finding.RuleID)
				}
			}
		}
	})
}

// assertWellFormed holds the invariants every finding must satisfy, whatever the input.
func assertWellFormed(t *testing.T, f domain.Finding) {
	t.Helper()

	if f.RuleID == "" {
		t.Error("finding has no rule ID")
	}
	if !f.Reversibility.Valid() {
		t.Errorf("%s has invalid reversibility %q", f.RuleID, f.Reversibility)
	}
	if !f.LockHazard.Valid() {
		t.Errorf("%s has invalid lock hazard %q", f.RuleID, f.LockHazard)
	}
	if strings.TrimSpace(f.Rationale) == "" {
		t.Errorf("%s has no rationale", f.RuleID)
	}
	if f.Line < 0 {
		t.Errorf("%s has a negative line number %d", f.RuleID, f.Line)
	}

	// An undo step attached to a change that cannot be undone is a lie the certificate must
	// never tell.
	switch f.Reversibility {
	case domain.ReversibilityIrreversible, domain.ReversibilityUnknown:
		if f.UndoStep != "" {
			t.Errorf("%s is %s yet offers undo step %q", f.RuleID, f.Reversibility, f.UndoStep)
		}
	}

	// The statement is rendered into PR comments and hashed into certificates; the bound is
	// what stops a generated migration from burying the verdict.
	if n := len([]rune(f.Statement)); n > domain.MaxStatementLength {
		t.Errorf("%s statement is %d runes, over the %d bound", f.RuleID, n, domain.MaxStatementLength)
	}
}

// FuzzValidateDownMigrations exercises the pairing and symmetry logic, which walks filenames and
// two parse trees at once — a combination with more edge cases than it looks.
func FuzzValidateDownMigrations(f *testing.F) {
	f.Add("CREATE TABLE t (id bigint);", "DROP TABLE t;")
	f.Add("DROP TABLE t;", "")
	f.Add("", "")
	f.Add("CREATE INDEX i ON t (c);", "not valid sql")
	f.Add(";;;", ";;;")
	f.Add("\x00", "\xff")

	sqlParser := parser.NewPgQuery()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, up, down string) {
		files := []domain.ChangedFile{
			{Path: "migrations/0001_x.up.sql", Status: domain.StatusAdded, Current: []byte(up)},
			{Path: "migrations/0001_x.down.sql", Status: domain.StatusAdded, Current: []byte(down)},
		}

		statuses, err := postgres.ValidateDownMigrations(ctx, sqlParser, files)
		if err != nil {
			t.Fatalf("ValidateDownMigrations: %v", err)
		}

		if len(statuses) != 1 {
			t.Fatalf("got %d statuses, want 1", len(statuses))
		}

		status := statuses[0]
		if status.Migration != "0001_x" {
			t.Errorf("migration = %q, want 0001_x", status.Migration)
		}

		// The levels are ordered: symmetry cannot pass unless the file parsed, and it cannot
		// have parsed unless it existed. A status that claims otherwise is incoherent.
		if status.Parses && !status.Exists {
			t.Error("a down migration parses without existing")
		}
		if status.Symmetric && !status.Parses {
			t.Error("a down migration is symmetric without parsing")
		}
		if !status.Symmetric && len(status.SymmetryNotes) == 0 {
			t.Error("a failed level recorded no note explaining why")
		}
	})
}
