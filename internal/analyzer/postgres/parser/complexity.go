// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package parser

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTooComplex means input was refused before it reached the parser because its structure could
// overflow the parser's stack.
//
// The caller treats this like any other parse failure: PG027, UNKNOWN, grade F. A migration
// nobody could safely read is not a migration to bless.
var ErrTooComplex = errors.New("parse: input is too deeply structured to parse safely")

// Structural limits applied before any input reaches cgo.
//
// These exist because the PostgreSQL grammar is recursive descent running on a C stack, and a
// long chain of binary operators — "SELECT 1+1+1+..." — builds a parse tree deep enough to
// overflow it. That is a hard process crash inside C, which the engine's recover boundary
// CANNOT catch: the whole server dies, taking every in-flight analysis with it. On a webhook
// server that is a remote denial of service triggered by opening a pull request.
//
// Measured on this parser, a chain of 1,000 operators parses fine and 5,000 crashes the process.
// Parenthesis nesting is bounded by the grammar itself and is safe well past that, but is capped
// here too so the guard does not depend on that remaining true.
//
// The limits sit two orders of magnitude below the observed failure and far above anything a
// human-written migration contains. A file that exceeds them is refused, not truncated.
const (
	maxNestingDepth     = 100
	maxOperatorsPerStmt = 500
)

// operatorChars are the characters PostgreSQL may combine into an operator. Each one can extend
// an expression chain by another level of parse-tree depth.
const operatorChars = "+-*/<>=~!@#%^&|?"

// wordOperators are operators spelled as keywords. They must be counted alongside the symbolic
// ones: "NOT NOT NOT ..." nests a BoolExpr per repetition exactly as "1+1+1" nests an A_Expr,
// and a chain of five thousand crashes the parser just the same. Counting only punctuation
// would leave that door open — which it did, until a fuzz seed walked through it.
var wordOperators = map[string]bool{
	"NOT": true, "AND": true, "OR": true,
	"IS": true, "LIKE": true, "ILIKE": true, "SIMILAR": true,
	"BETWEEN": true, "OVERLAPS": true, "IN": true,
}

// guardComplexity refuses input whose structure could overflow the parser's stack.
//
// It scans rather than parses, because parsing is the operation that crashes. String literals,
// dollar-quoted bodies, quoted identifiers, and comments are skipped, so a migration that merely
// contains arithmetic inside a string is not punished for it.
func guardComplexity(sql string) error {
	var (
		depth     int
		maxDepth  int
		operators int
		statement = 1
	)

	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i = skipLineComment(sql, i)

		case strings.HasPrefix(sql[i:], "/*"):
			i = skipBlockComment(sql, i)

		case sql[i] == '\'':
			i = skipQuoted(sql, i, '\'')

		case sql[i] == '"':
			i = skipQuoted(sql, i, '"')

		case sql[i] == '$':
			if end, ok := skipDollarQuoted(sql, i); ok {
				i = end
				continue
			}
			i++

		case sql[i] == '(':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
			if maxDepth > maxNestingDepth {
				return fmt.Errorf("%w: statement %d nests more than %d levels deep",
					ErrTooComplex, statement, maxNestingDepth)
			}
			i++

		case sql[i] == ')':
			if depth > 0 {
				depth--
			}
			i++

		case sql[i] == ';':
			// Limits are per statement: a file of many simple statements is not suspicious,
			// while one statement with a thousand chained operators is.
			statement++
			operators = 0
			depth = 0
			maxDepth = 0
			i++

		case strings.IndexByte(operatorChars, sql[i]) >= 0:
			// A run of operator characters is one operator, not several: "<=" and "!~*" are
			// single tokens and add one level of depth between them.
			for i < len(sql) && strings.IndexByte(operatorChars, sql[i]) >= 0 {
				i++
			}
			operators++
			if operators > maxOperatorsPerStmt {
				return fmt.Errorf("%w: statement %d chains more than %d operators",
					ErrTooComplex, statement, maxOperatorsPerStmt)
			}

		case isIdentifierStart(sql[i]):
			word, next := readWord(sql, i)
			i = next

			if wordOperators[strings.ToUpper(word)] {
				operators++
				if operators > maxOperatorsPerStmt {
					return fmt.Errorf("%w: statement %d chains more than %d operators",
						ErrTooComplex, statement, maxOperatorsPerStmt)
				}
			}

		default:
			i++
		}
	}

	return nil
}

func isIdentifierStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// readWord consumes an identifier and returns it with the offset just past it, so a keyword is
// only matched on a word boundary — "NOTES" must not count as "NOT".
func readWord(sql string, i int) (string, int) {
	start := i
	for i < len(sql) {
		c := sql[i]
		if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	return sql[start:i], i
}

func skipLineComment(sql string, i int) int {
	if end := strings.IndexByte(sql[i:], '\n'); end >= 0 {
		return i + end + 1
	}
	return len(sql)
}

// skipBlockComment handles PostgreSQL's nested block comments.
func skipBlockComment(sql string, i int) int {
	depth := 0
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(sql)
}

// skipQuoted skips a quoted string or identifier, honouring the doubled-quote escape.
func skipQuoted(sql string, i int, quote byte) int {
	i++ // opening quote
	for i < len(sql) {
		if sql[i] != quote {
			i++
			continue
		}
		// A doubled quote is an escaped quote, not the end of the literal.
		if i+1 < len(sql) && sql[i+1] == quote {
			i += 2
			continue
		}
		return i + 1
	}
	return len(sql)
}

// skipDollarQuoted skips a $tag$ ... $tag$ body, which is where function definitions live and
// where SQL-shaped text most often appears without being SQL.
func skipDollarQuoted(sql string, i int) (int, bool) {
	close := strings.IndexByte(sql[i+1:], '$')
	if close < 0 {
		return 0, false
	}

	tag := sql[i : i+close+2]

	// A tag must be an identifier: $$ or $name$, never $1$ (a positional parameter).
	for _, r := range tag[1 : len(tag)-1] {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return 0, false
		}
	}

	end := strings.Index(sql[i+len(tag):], tag)
	if end < 0 {
		// Unterminated: treat the rest of the input as body, which the parser will reject.
		return len(sql), true
	}
	return i + len(tag) + end + len(tag), true
}
