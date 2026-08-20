// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// compareTypes implements exactly the conversions CLAUDE.md §9 enumerates. Everything else must
// come back widthUnknown, because a conversion the table does not describe is one the engine
// has no authority to grade.
func TestCompareTypes(t *testing.T) {
	t.Parallel()

	typ := func(name string, mods ...int32) parser.Type {
		return parser.Type{Name: name, Mods: mods}
	}

	tests := []struct {
		name string
		from parser.Type
		to   parser.Type
		want widthChange
	}{
		{"bigint to int narrows", typ("int8"), typ("int4"), widthNarrowing},
		{"int to smallint narrows", typ("int4"), typ("int2"), widthNarrowing},
		{"int to bigint widens", typ("int4"), typ("int8"), widthWidening},
		{"smallint to bigint widens", typ("int2"), typ("int8"), widthWidening},
		{"same integer type", typ("int4"), typ("int4"), widthSame},
		{"integer to text is not in the table", typ("int4"), typ("text"), widthUnknown},

		{"text to varchar(n) narrows", typ("text"), typ("varchar", 50), widthNarrowing},
		{"varchar(n) to text widens", typ("varchar", 50), typ("text"), widthWidening},
		{"varchar shrinks", typ("varchar", 50), typ("varchar", 10), widthNarrowing},
		{"varchar grows", typ("varchar", 10), typ("varchar", 50), widthWidening},
		{"varchar unchanged", typ("varchar", 10), typ("varchar", 10), widthSame},
		{"unbounded varchar behaves like text", typ("varchar"), typ("varchar", 10), widthNarrowing},
		{"text to text", typ("text"), typ("text"), widthSame},

		{"numeric precision reduced", typ("numeric", 12, 2), typ("numeric", 8, 2), widthNarrowing},
		{"numeric scale reduced", typ("numeric", 12, 4), typ("numeric", 12, 2), widthNarrowing},
		{"numeric precision increased", typ("numeric", 8, 2), typ("numeric", 12, 2), widthWidening},
		{"bare numeric constrained narrows", typ("numeric"), typ("numeric", 12, 2), widthNarrowing},
		{"constrained numeric unbounded widens", typ("numeric", 12, 2), typ("numeric"), widthWidening},
		{"numeric unchanged", typ("numeric", 12, 2), typ("numeric", 12, 2), widthSame},

		{"timestamptz to date narrows", typ("timestamptz"), typ("date"), widthNarrowing},
		{"date to timestamptz widens", typ("date"), typ("timestamptz"), widthWidening},

		// Deliberately not classified. Adding any of these would be inventing a rule.
		{"timestamptz to timestamp is not in the table", typ("timestamptz"), typ("timestamp"), widthUnknown},
		{"float to numeric is not in the table", typ("float8"), typ("numeric"), widthUnknown},
		{"uuid to text is not in the table", typ("uuid"), typ("text"), widthUnknown},
		{"bpchar is not covered", typ("bpchar", 10), typ("bpchar", 5), widthUnknown},

		{"case is folded", typ("INT8"), typ("Int4"), widthNarrowing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := compareTypes(tt.from, tt.to); got != tt.want {
				t.Errorf("compareTypes(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestSchemaTracking(t *testing.T) {
	t.Parallel()

	s := newSchema()

	s.apply(parser.Statement{
		Kind:     parser.KindCreateTable,
		Relation: "orders",
		Columns: []parser.Column{
			{Name: "id", Type: &parser.Type{Name: "int8"}},
			{Name: "quantity", Type: &parser.Type{Name: "int4"}},
		},
	})

	got, ok := s.columnType("orders", "quantity")
	if !ok || got.Name != "int4" {
		t.Fatalf("columnType after CREATE TABLE = %v/%v, want int4", got, ok)
	}

	// PostgreSQL folds unquoted identifiers, so lookups must too.
	if _, ok := s.columnType("ORDERS", "QUANTITY"); !ok {
		t.Error("identifier case is not folded on lookup")
	}

	s.apply(parser.Statement{
		Kind:       parser.KindAlterColumnType,
		Relation:   "orders",
		Object:     "quantity",
		ColumnType: &parser.Type{Name: "int8"},
	})
	if got, _ := s.columnType("orders", "quantity"); got.Name != "int8" {
		t.Errorf("type after ALTER = %q, want int8", got.Name)
	}

	s.apply(parser.Statement{Kind: parser.KindDropColumn, Relation: "orders", Object: "quantity"})
	if _, ok := s.columnType("orders", "quantity"); ok {
		t.Error("column is still tracked after being dropped")
	}

	s.apply(parser.Statement{Kind: parser.KindDropTable, Object: "orders"})
	if _, ok := s.columnType("orders", "id"); ok {
		t.Error("table is still tracked after being dropped")
	}
}

// An ALTER COLUMN TYPE whose prior type is unknown must be PG027, never a guess in either
// direction. This is the fail-closed path for CLAUDE.md §16.3.
func TestAlterColumnTypeWithoutPriorTypeIsUnknown(t *testing.T) {
	t.Parallel()

	got := classify(parser.Statement{
		Kind:       parser.KindAlterColumnType,
		Relation:   "orders",
		Object:     "quantity",
		ColumnType: &parser.Type{Name: "int4"},
	}, newSchema())

	if got.ruleID != "PG027" {
		t.Errorf("ruleID = %q, want PG027", got.ruleID)
	}
	if got.reversibility != domain.ReversibilityUnknown {
		t.Errorf("reversibility = %q, want UNKNOWN", got.reversibility)
	}
	if got.undo != "" {
		t.Errorf("an unknown verdict was given an undo step %q", got.undo)
	}
}

// CASCADE overrides the rule it overlays, per CLAUDE.md §16.2.
func TestCascadeOverridesBaseRule(t *testing.T) {
	t.Parallel()

	for _, kind := range []parser.Kind{
		parser.KindDropTable,
		parser.KindTruncate,
		parser.KindDropConstraint,
		parser.KindDropView,
	} {
		got := classify(parser.Statement{Kind: kind, Cascade: true, Object: "x"}, newSchema())

		if got.ruleID != "PG005" {
			t.Errorf("%s with CASCADE classified as %s, want PG005", kind, got.ruleID)
		}
		if got.reversibility != domain.ReversibilityIrreversible {
			t.Errorf("%s with CASCADE is %s, want IRREVERSIBLE", kind, got.reversibility)
		}
	}
}

// No IRREVERSIBLE or UNKNOWN verdict may carry an undo step. Offering one would be a lie, and
// the undo plan is assembled directly from these fields.
func TestIrreversibleVerdictsCarryNoUndo(t *testing.T) {
	t.Parallel()

	kinds := []parser.Kind{
		parser.KindDropTable, parser.KindDropColumn, parser.KindTruncate,
		parser.KindDropSchema, parser.KindDropDatabase, parser.KindDelete,
		parser.KindUpdate, parser.KindAlterSequenceRestart, parser.KindDropType,
		parser.KindDropSequence, parser.KindDropExtension, parser.KindUnrecognized,
	}

	sch := newSchema()
	for _, kind := range kinds {
		got := classify(parser.Statement{Kind: kind, Object: "x", Relation: "t"}, sch)

		switch got.reversibility {
		case domain.ReversibilityIrreversible, domain.ReversibilityUnknown:
			if got.undo != "" {
				t.Errorf("%s is %s yet carries undo step %q", kind, got.reversibility, got.undo)
			}
		}
	}
}

// Every classification must produce a valid, non-empty verdict. A rule that returned the zero
// value would read as REVERSIBLE-adjacent to a careless consumer.
func TestEveryClassificationIsWellFormed(t *testing.T) {
	t.Parallel()

	kinds := []parser.Kind{
		parser.KindUnrecognized, parser.KindDropTable, parser.KindDropColumn,
		parser.KindTruncate, parser.KindDropSchema, parser.KindDropDatabase,
		parser.KindAlterColumnType, parser.KindDelete, parser.KindUpdate,
		parser.KindAlterSequenceRestart, parser.KindDropType, parser.KindDropSequence,
		parser.KindDropExtension, parser.KindRenameTable, parser.KindRenameColumn,
		parser.KindDropConstraint, parser.KindDropIndex, parser.KindDropView,
		parser.KindDropFunction, parser.KindDropTrigger, parser.KindSetNotNull,
		parser.KindDropNotNull, parser.KindSetDefault, parser.KindDropDefault,
		parser.KindAddColumn, parser.KindAddConstraint, parser.KindCreateIndex,
		parser.KindCreateTable, parser.KindCreateView, parser.KindCreateType,
		parser.KindCreateSchema, parser.KindCreateSequence, parser.KindCreateExtension,
	}

	sch := newSchema()
	for _, kind := range kinds {
		got := classify(parser.Statement{Kind: kind, Object: "x", Relation: "t"}, sch)

		if got.ruleID == "" {
			t.Errorf("%s produced no rule ID", kind)
		}
		if !got.reversibility.Valid() {
			t.Errorf("%s produced invalid reversibility %q", kind, got.reversibility)
		}
		if !got.lock.Valid() {
			t.Errorf("%s produced invalid lock hazard %q", kind, got.lock)
		}
		if len(got.rationale) < 20 {
			t.Errorf("%s produced a rationale too short to explain anything: %q", kind, got.rationale)
		}
	}
}

func TestClassifyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path          string
		wantID        string
		wantDown      bool
		wantMigration bool
	}{
		{"migrations/0001_init.up.sql", "0001_init", false, true},
		{"migrations/0001_init.down.sql", "0001_init", true, true},
		{"migrations/0001/up.sql", "0001", false, true},
		{"migrations/0001/down.sql", "0001", true, true},
		{"migrations/0001/UP.SQL", "0001", false, true},
		{"db/schema.sql", "schema", false, true},
		{"README.md", "", false, false},
		{"k8s/deploy.yaml", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			id, isDown, isMigration := classifyPath(tt.path)
			if id != tt.wantID || isDown != tt.wantDown || isMigration != tt.wantMigration {
				t.Errorf("classifyPath(%q) = (%q, %v, %v), want (%q, %v, %v)",
					tt.path, id, isDown, isMigration, tt.wantID, tt.wantDown, tt.wantMigration)
			}
		})
	}
}

// Migrations in different directories must not be paired just because they share a number.
func TestPairMigrationsKeysOnDirectory(t *testing.T) {
	t.Parallel()

	pairs := pairMigrations([]domain.ChangedFile{
		{Path: "a/0001/up.sql"},
		{Path: "a/0001/down.sql"},
		{Path: "b/0001/up.sql"},
		{Path: "migrations/0002_x.up.sql"},
	})

	if len(pairs) != 3 {
		t.Fatalf("got %d pairs, want 3", len(pairs))
	}

	// Sorted by directory then identity, so the certificate never depends on map order.
	if pairs[0].up == nil || pairs[0].down == nil {
		t.Errorf("a/0001 did not pair: up=%v down=%v", pairs[0].up, pairs[0].down)
	}
	if pairs[1].up == nil || pairs[1].down != nil {
		t.Errorf("b/0001 should have an up and no down")
	}
	if pairs[2].id != "0002_x" {
		t.Errorf("third pair id = %q, want 0002_x", pairs[2].id)
	}
}

func TestPairMigrationsIsDeterministic(t *testing.T) {
	t.Parallel()

	files := []domain.ChangedFile{
		{Path: "migrations/0003_c.up.sql"},
		{Path: "migrations/0001_a.up.sql"},
		{Path: "migrations/0002_b.up.sql"},
	}

	want := []string{"0001_a", "0002_b", "0003_c"}
	for run := 0; run < 20; run++ {
		got := pairMigrations(files)
		for i := range want {
			if got[i].id != want[i] {
				t.Fatalf("run %d position %d: id = %q, want %q", run, i, got[i].id, want[i])
			}
		}
	}
}

func TestDedupe(t *testing.T) {
	t.Parallel()

	got := dedupe([]string{"a", "a", "b", "b", "b", "c"})
	want := []string{"a", "b", "c"}

	if len(got) != len(want) {
		t.Fatalf("dedupe = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupe = %v, want %v", got, want)
		}
	}

	if got := dedupe(nil); len(got) != 0 {
		t.Errorf("dedupe(nil) = %v, want empty", got)
	}
}
