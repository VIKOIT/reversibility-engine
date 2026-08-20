# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Two things carry their own compatibility promise and are called out whenever they
move:

- **The certificate schema** (`pkg/certificate`, `schemaVersion`) — downstream
  merge gates parse it, so a breaking field change bumps that version, not just
  the release version.
- **Rule IDs** (`PG001`, `K8S001`, …) — people suppress, alert on, and dashboard
  against them, so they are never renumbered or reused.

## [Unreleased]

Nothing yet.

## [0.1.0] - 2026-08-20 — Initial Release

First public release. Usable end to end; the certificate schema and CLI flags may
still change before 1.0.

### Added

**The engine.** Static analysis that grades whether a changeset can be rolled
back, emitting a `ReversibilityCertificate` with a grade of A, B, C, or F, a
concrete undo plan, and an explicit list of what cannot be undone. Fail-closed by
construction: an error, a panic, an unparseable file, or an unrecognized
construct all grade F.

**PostgreSQL analyzer** — 27 rules (`PG001`–`PG027`) classified over a real
PostgreSQL AST via `pg_query_go`, never regex. Distinguishes `DELETE` with and
without `WHERE`, narrowing from widening type changes, `CONCURRENTLY` from not,
and constraints added `NOT VALID`. Validates down migrations at three levels:
existence, parseability, and create/drop symmetry (advisory).

**Kubernetes analyzer** — 15 rules (`K8S001`–`K8S015`) over a structural manifest
diff, matched by apiVersion, kind, namespace, and name. Covers volume claim
templates, selector immutability, PVC storage and storage-class changes, digest
pinning, dangling ConfigMap and Secret references, and removed probes.

**Scoring and certificates** — grade assembly with assign-then-cap semantics,
undo-plan generation in reverse order of application, and a SHA-256 input digest
that attributes a certificate to an exact changeset.

**`revctl`** — CLI over the engine. Formats: `json` (the public schema),
`markdown` (pull request comments), `sarif` (GitHub code scanning). Exit codes
are the CI contract: `0` success, `1` gate not met, `2` run did not complete.

**`revsrv`** — GitHub App webhook server. Validates `X-Hub-Signature-256` with a
constant-time comparison, posts the certificate to the pull request and updates
its own previous comment rather than adding a new one, and fetches unchanged
context files from the base commit so the rules that need them are not blind.

**`pkg/certificate`** — the public, versioned wire schema at `1.0.0`, so external
consumers have a stable contract without importing anything internal.

**Determinism** — identical input produces a byte-identical certificate. No
timestamps, no run IDs, no hostnames, no map-iteration order. Verified by a test
that runs the engine 100× over every fixture.

### Security

- **Parser stack-overflow guard.** A chained SQL expression of a few thousand
  operators overflowed the C parser's stack — a hard process crash inside cgo
  that `recover()` cannot catch, making a ~10 KB pull request a remote denial of
  service against `revsrv`. Structurally extreme input is now refused before it
  reaches cgo and reported as `PG027`/`UNKNOWN`, which grades F. Found by
  fuzzing. See [ADR/0001](ADR/0001-parser-choice.md).
- **Multi-document manifest truncation.** `sigs.k8s.io/yaml` decodes only the
  first document of a stream and returns a nil error, which would have silently
  hidden every object after the first in rendered Helm output. Documents are now
  split by a real YAML stream decoder.
- **Fail-closed on network failure.** A rate limit or API error while fetching a
  changeset produces a grade F certificate naming the failure, and posts it.
  Analyzing whichever files happened to arrive is never an option.
- **Markdown injection.** Finding text is attacker-controlled in any pull request
  from a fork. Backticks, pipes, and newlines are neutralized so a statement
  cannot escape its code span, while `<`, `>`, and `<=` survive intact inside it
  so SQL stays readable.

### Fixed

- **Verdict determinism.** The Postgres schema tracker processed files in slice
  order, so the same `ALTER` in two files produced different rationales depending
  on arrival order — the same grade, different certificate bytes. Both analyzers
  now sort internally. Found by fuzzing; the minimized input is committed as a
  regression seed.
- **Unrecognized verdicts.** An analyzer returning an empty or misspelled
  `Reversibility` merely failed the "all REVERSIBLE" check and capped the grade at
  B. A classification nobody can read is what `UNKNOWN` is for; it now grades F.

### Known limitations

- Resolving a changeset from a git ref is not implemented. The filesystem
  provider compares paths, not revisions.
- Context-file lookup is bounded to directories the change already touches, so a
  rule whose context lies elsewhere returns `UNKNOWN` rather than guessing.
- No tagged binaries or container images are published yet; install from source.
- Kubernetes findings carry no line numbers — a structural diff has no single
  line to blame.

[Unreleased]: https://github.com/VIKOIT/reversibility-engine/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v0.1.0
