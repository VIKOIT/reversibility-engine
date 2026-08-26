# ADR 0001 — SQL parser choice: pg_query_go, and the cgo tax we accept

- **Status:** Accepted
- **Date:** 2026-08-19
- **Deciders:** Project owner
- **Applies to:** `internal/analyzer/postgres/parser`

## Context

The Postgres analyzer must decide, for every statement in a migration, which of the 27 rules in
docs/SPECIFICATION.md §9 applies. That decision is the product. A wrong "REVERSIBLE" verdict on a
`DROP TABLE` is not a bug report, it is a customer with a deleted table and a support ticket
that ends the relationship.

The distinctions the rule table demands are structural, not lexical:

- PG008 vs PG009 — `DELETE` with or without a `WHERE` clause.
- PG006 vs PG007 — whether an `ALTER COLUMN TYPE` narrows or widens, which requires resolving
  both the old and new type including precision and length modifiers.
- PG019 vs PG020 — whether a column `DEFAULT` expression is volatile or constant.
- PG015 vs PG014, PG024 vs PG023 — the presence of `CONCURRENTLY`.
- PG022 — the presence of `NOT VALID` on a constraint being added.

A regex sees text. It cannot tell a `DROP TABLE` from the same words inside a string literal, a
dollar-quoted function body, a comment, or a `CREATE VIEW` definition. It cannot resolve type
modifiers to compare widths. Every one of those confusions produces a *confidently wrong*
answer, which is the single failure mode this product cannot survive.

Options considered:

1. **`github.com/pganalyze/pg_query_go/v5`** — Go bindings over the actual PostgreSQL server
   parser, vendored as C source. Produces the same parse tree Postgres itself would.
2. **A pure-Go SQL parser** (e.g. `vitess` sqlparser, `xwb1989/sqlparser`, CockroachDB's
   parser). No cgo.
3. **Regex / hand-rolled tokenizer.**

## Decision

**Use `github.com/pganalyze/pg_query_go/v5`, isolated behind an internal `SQLParser` interface
in `internal/analyzer/postgres/parser`.**

Option 3 is rejected outright and is on the Do-Not list in docs/SPECIFICATION.md §14.

Option 2 was rejected because the available pure-Go parsers target MySQL or CockroachDB
dialects. Their divergence from real PostgreSQL grammar — `ALTER TABLE ... SET NOT NULL`,
`CREATE INDEX CONCURRENTLY`, `ADD CONSTRAINT ... NOT VALID`, dollar quoting, extension DDL — is
exactly the surface our rule table depends on. Every gap becomes a PG027/UNKNOWN, and enough
UNKNOWNs make the tool grade everything F and get switched off. A parser that is wrong about
Postgres is worse than a slow build.

Option 1 is, by construction, correct about Postgres: it *is* the Postgres parser.

## Consequences

### The cost: cgo

Accepting `pg_query_go` means accepting cgo, permanently, in the core analysis path:

- **`CGO_ENABLED=1` is mandatory.** A `CGO_ENABLED=0` build does not degrade gracefully — it
  fails to build, which is the correct behaviour. This is set explicitly in the `Makefile` and
  in `.github/workflows/ci.yml` so nobody can turn it off by accident.
- **A C toolchain is required to build.** Contributors need gcc/clang; CI needs it too.
- **Cross-compilation is no longer free.** `GOOS=linux go build` from a macOS or Windows
  workstation needs a cross toolchain or a container. Release builds must happen in a Linux
  container.
- **Build times increase.** The vendored PostgreSQL parser is a large C compilation unit; expect
  a slow first build, cached thereafter.
- **`go test -race` still works,** but the race detector cannot see into C.

### The stack-overflow constraint (found in S7, applies everywhere)

**The musl note below understated the problem. Deep input crashes the parser on glibc and on
Windows too, not only on musl.**

Fuzzing in S7 found that a long chain of operators — `SELECT 1+1+1+…` with a few thousand terms,
or `NOT NOT NOT …` — overflows the C parser's stack. Measured on this parser, a chain of 1,000
parses fine and 5,000 kills the process. Parenthesis nesting is bounded by the grammar and is
safe much further; chained operators are not bounded at all in the dangerous band.

This is a **hard process crash inside C**. The engine's `recover()` boundary cannot catch it:
Go never regains control. On `revsrv` that made a ~10 KB pull request into a remote denial of
service — open a PR, kill the server, take every in-flight analysis with it.

**Mitigation:** `internal/analyzer/postgres/parser/complexity.go` refuses structurally extreme
input *before* it reaches cgo, bounded at 100 nesting levels and 500 operators per statement —
two orders of magnitude below the observed failure and far above anything handwritten. Refused
input is reported as PG027/UNKNOWN, so it grades F: fail-closed, visible, and reviewable by a
human. String literals, dollar-quoted bodies, and comments are skipped, so arithmetic inside a
string is not punished for looking like structure.

**This guard is load-bearing. Do not remove it, and do not raise the limits without re-measuring
the crash threshold on the target platform.** The word operators (`NOT`, `AND`, `OR`, …) matter
as much as the symbolic ones: counting only punctuation left `NOT NOT NOT …` able to crash the
process, which is exactly how the second half of this bug was found.

### The musl / Alpine constraint

`pg_query_go` is built and tested against glibc. On Alpine and other musl distributions:

- The build needs `apk add build-base` at minimum.
- Even when it builds, musl's default thread stack is 128 KiB against glibc's 8 MiB. The
  PostgreSQL grammar is a recursive-descent parser, so deeply nested SQL — long boolean chains,
  nested subqueries — can overflow the stack on musl where it parses cleanly on glibc. A stack
  overflow in C is a process crash, not a Go panic, so the engine's `recover()` boundary
  **cannot** catch it.

**Therefore: ship `revsrv` on a glibc base image** — `debian-slim` or `gcr.io/distroless/base`
— not `alpine`. If an Alpine build ever becomes a hard requirement, it must come with an
explicit larger thread stack for the goroutine that calls the parser, plus a deep-nesting
fixture in CI to prove it.

### The mitigation: the `SQLParser` seam

The concrete parser is reachable only through an interface in
`internal/analyzer/postgres/parser`. Nothing else in the codebase imports `pg_query_go`.

This buys three things:

1. The classification rules are testable against a fake parser, with no cgo in the unit-test
   path.
2. If a credible pure-Go PostgreSQL parser appears, adopting it is one implementation of one
   interface, not a rewrite of the analyzer.
3. The cgo blast radius — build tags, toolchain requirements, base-image constraints — stays in
   a single package where it is documented.

### Failure behaviour

If the parser is unavailable, errors, or cannot produce a tree, the analyzer returns an error.
Per docs/SPECIFICATION.md §11 an analyzer error grades **F**. There is no regex fallback and no
best-effort mode: the engine either knows what a statement does, or it refuses to bless it.

## Revisit when

- A pure-Go parser tracks the real PostgreSQL grammar closely enough to satisfy all 27 fixtures.
- Or cross-compilation friction begins costing more than the correctness cgo buys — measured,
  not felt.
