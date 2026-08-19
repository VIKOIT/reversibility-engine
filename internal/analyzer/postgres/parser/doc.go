// Package parser isolates the PostgreSQL SQL parser behind a narrow SQLParser interface.
//
// The concrete implementation wraps github.com/pganalyze/pg_query_go/v5, which is cgo and
// carries a musl/Alpine constraint (see ADR/0001-parser-choice.md). Confining it here means the
// cgo blast radius is one package, and a future pure-Go parser is a swap rather than a rewrite.
//
// There is no regex fallback. If the parser is unavailable that is an error, and an error
// grades F.
package parser
