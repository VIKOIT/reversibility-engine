// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
)

// schema tracks column types declared earlier in the same changeset.
//
// It exists because PG006 and PG007 are unanswerable from the ALTER alone: the statement
// "ALTER TABLE orders ALTER COLUMN quantity TYPE integer" says nothing about whether that
// widens or narrows. The prior type has to come from a CREATE TABLE or ADD COLUMN earlier in
// the changeset. When it does not, the verdict is UNKNOWN — never a guess.
//
// See CLAUDE.md §16.3: a schema baseline supplied from a live database would be a different
// design, and is out of MVP scope.
type schema struct {
	// tables maps a lower-cased table name to its known column types.
	tables map[string]map[string]parser.Type
}

func newSchema() *schema {
	return &schema{tables: map[string]map[string]parser.Type{}}
}

// apply records the effect of a statement on the tracked schema. It is called after the
// statement has been classified, so that an ALTER is judged against the type it replaces.
func (s *schema) apply(st parser.Statement) {
	switch st.Kind {
	case parser.KindCreateTable:
		for _, c := range st.Columns {
			if c.Type != nil {
				s.set(st.Relation, c.Name, *c.Type)
			}
		}

	case parser.KindAddColumn:
		if st.ColumnType != nil {
			s.set(st.Relation, st.Object, *st.ColumnType)
		}

	case parser.KindAlterColumnType:
		if st.ColumnType != nil {
			s.set(st.Relation, st.Object, *st.ColumnType)
		}

	case parser.KindDropColumn:
		s.remove(st.Relation, st.Object)

	case parser.KindDropTable:
		delete(s.tables, key(st.Object))
	}
}

func (s *schema) set(table, column string, t parser.Type) {
	if table == "" || column == "" {
		return
	}

	cols, ok := s.tables[key(table)]
	if !ok {
		cols = map[string]parser.Type{}
		s.tables[key(table)] = cols
	}
	cols[key(column)] = t
}

func (s *schema) remove(table, column string) {
	if cols, ok := s.tables[key(table)]; ok {
		delete(cols, key(column))
	}
}

// columnType returns the type a column was last known to have.
func (s *schema) columnType(table, column string) (parser.Type, bool) {
	cols, ok := s.tables[key(table)]
	if !ok {
		return parser.Type{}, false
	}
	t, ok := cols[key(column)]
	return t, ok
}

// PostgreSQL folds unquoted identifiers to lower case, so the tracker must too, or
// "ALTER TABLE Orders" would fail to find a column declared by "CREATE TABLE orders".
func key(s string) string { return strings.ToLower(s) }

// widthChange is the direction of a type conversion.
type widthChange int

const (
	// widthUnknown means the conversion is outside the cases CLAUDE.md §9 enumerates. It
	// grades UNKNOWN. Extending this set would be inventing a classification rule.
	widthUnknown widthChange = iota
	widthNarrowing
	widthWidening
	widthSame
)

// integerRanks orders the integer types by the range they can hold.
var integerRanks = map[string]int{"int2": 1, "int4": 2, "int8": 3}

// compareTypes reports whether converting from one type to another loses range.
//
// It implements exactly the four cases named in the PG006 row — integer narrowing,
// text to varchar(n), reduced numeric precision, and timestamptz to date — plus their
// inverses for PG007. Everything else is widthUnknown on purpose.
func compareTypes(from, to parser.Type) widthChange {
	f, t := strings.ToLower(from.Name), strings.ToLower(to.Name)

	if fr, ok := integerRanks[f]; ok {
		tr, ok := integerRanks[t]
		if !ok {
			return widthUnknown
		}
		switch {
		case tr < fr:
			return widthNarrowing
		case tr > fr:
			return widthWidening
		default:
			return widthSame
		}
	}

	if isCharacter(f) && isCharacter(t) {
		return compareCharacter(from, to)
	}

	if f == "numeric" && t == "numeric" {
		return compareNumeric(from, to)
	}

	// The one temporal pair the table names. timestamptz to date discards the time of day and
	// the zone; there is no way back.
	if f == "timestamptz" && t == "date" {
		return widthNarrowing
	}
	if f == "date" && t == "timestamptz" {
		return widthWidening
	}

	// Identical name and identical modifiers is a genuine no-op. Identical name with different
	// modifiers is not: bpchar(10) to bpchar(5) truncates, and this function has no authority
	// to say so because the table does not cover bpchar. Calling it "same" would be a lie that
	// a future caller could act on.
	if f == t && sameMods(from, to) {
		return widthSame
	}
	return widthUnknown
}

func sameMods(a, b parser.Type) bool {
	if len(a.Mods) != len(b.Mods) {
		return false
	}
	for i := range a.Mods {
		if a.Mods[i] != b.Mods[i] {
			return false
		}
	}
	return true
}

func isCharacter(name string) bool { return name == "text" || name == "varchar" }

// compareCharacter treats text as unbounded, which is what makes text to varchar(n) a
// truncation and the reverse a widening.
func compareCharacter(from, to parser.Type) widthChange {
	fromLen, fromBounded := charLimit(from)
	toLen, toBounded := charLimit(to)

	switch {
	case !fromBounded && !toBounded:
		return widthSame
	case !fromBounded && toBounded:
		return widthNarrowing
	case fromBounded && !toBounded:
		return widthWidening
	case toLen < fromLen:
		return widthNarrowing
	case toLen > fromLen:
		return widthWidening
	default:
		return widthSame
	}
}

// charLimit reports a character type's length bound. varchar with no modifier is unbounded,
// exactly like text.
func charLimit(t parser.Type) (limit int32, bounded bool) {
	if strings.ToLower(t.Name) == "text" || len(t.Mods) == 0 {
		return 0, false
	}
	return t.Mods[0], true
}

// compareNumeric implements "numeric precision reduced". Bare numeric is unbounded, so
// constraining it at all is a narrowing.
func compareNumeric(from, to parser.Type) widthChange {
	fromP, fromS, fromBounded := numericMods(from)
	toP, toS, toBounded := numericMods(to)

	switch {
	case !fromBounded && !toBounded:
		return widthSame
	case !fromBounded && toBounded:
		return widthNarrowing
	case fromBounded && !toBounded:
		return widthWidening
	}

	// Losing integral digits or decimal places both discard values that were representable
	// before, so either counts as narrowing.
	if toP < fromP || toS < fromS {
		return widthNarrowing
	}
	if toP > fromP || toS > fromS {
		return widthWidening
	}
	return widthSame
}

func numericMods(t parser.Type) (precision, scale int32, bounded bool) {
	switch len(t.Mods) {
	case 0:
		return 0, 0, false
	case 1:
		return t.Mods[0], 0, true
	default:
		return t.Mods[0], t.Mods[1], true
	}
}
