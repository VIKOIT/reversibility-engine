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

### Fixed

**The README advertised `schemaVersion` `1.0.0`; it has been `1.4.0` since the
Terraform analyzer landed.** The same paragraph tells consumers to pin against the
schema rather than against the tool, so the one number a downstream gate is
directed to depend on was four bumps out of date. The status badge and status line
also still read `v1.0.0` and now read `v1.1.2`.

## [v1.1.2] - 2026-08-26

The first release since `0.1.0` to publish binaries. `v1.0.0`–`v1.1.0` were cut as
tags and are not removed, but their release runs did not complete, so everything
below ships here.

### Security — a gate that reported success having analyzed nothing

**If you consume `VIKOIT/reversibility-engine@v1`, your gate passed without
running between the v1.1.0 image publish and this release.** No grade was wrong.
There was no grade.

`revctl` invoked with no arguments printed help and exited **0**. The frozen
`v1.0.x` Docker action pulls `ghcr.io/vikoit/reversibility-engine:v1` and sets no
`entrypoint:` and no `args:`, so it runs whatever the image declares. That
entrypoint used to be a script that performed the analysis; from v1.1.0 it was
`revctl` itself. Publishing the v1.1.0 image under the moving `:v1` tag therefore
turned every `@v1` consumer's gate into a green check over nothing.

The moving tag was the vector. The defect was the exit code: **the one invocation
that analyzes nothing was also the only one that could never fail.**

Fixed at the source and at every layer above it:

- `revctl` with no arguments now exits **2** and prints help to stderr — stdout is
  where a certificate goes. `revctl --help` and `revctl help` still exit 0, so
  nothing learns to ignore this exit code.
- The action exits 2 if no certificate was written, and now also if the verdict
  cannot be read back out of one. A step reporting FAIL while passing the job has
  proved only that something was written.
- An image whose no-argument invocation exits 0 is no longer publishable, and the
  self-test builds the image from the commit and asserts it — so this is caught
  before a tag moves rather than after.
- Every `docker run` in this repository and in the README now names
  `--entrypoint` explicitly. An inherited entrypoint is a silent dependency on a
  value somebody else can change.
- **Immutable image tags only.** `:v1` is no longer published;
  `.github/workflows/restore-image-tag.yml` repoints an alias from a runner, with
  the digest read back and the restored `ENTRYPOINT` asserted.

New invariant in `CLAUDE.md` §2: **a gate must prove it ran. No certificate
produced means exit 2, never exit 0.**

### Fixed — documentation named a version that never existed

The README, `docs/PRODUCTION-CONTEXT.md`, and `CLAUDE.md` described the composite
action as `@v2` and its image as `:v2`. **Neither has ever existed.** Every tag cut
is `v1.x`: `v1.0.0`–`v1.0.2` are the Docker action, `v1.1.0` onward are the
composite, and the transition needed no new major because every input kept
working. Users following the README were told to write a ref that resolves to
nothing. All references now read `@v1`, and §11e records the one-line ruling so
the ambiguity cannot regenerate.

### Removed — prebuilt `darwin/amd64` binaries

**Releases no longer publish `revctl` or `revsrv` for Intel Macs.** The matrix is
four targets, all built and verified on their native architecture:
`linux/amd64`, `linux/arm64`, `darwin/arm64`, `windows/amd64`.

`macos-13` is the only hosted Intel Mac runner, and on the v1.1.1 release it sat
in the queue for over two hours before the run had to be cancelled by hand.
Nothing bounds that — `timeout-minutes` starts when a job begins *executing*, so a
job waiting for a runner that never arrives waits forever.

Cross-compiling from Apple Silicon was considered and rejected. It would have
shipped the one artifact nobody could execution-test, and the per-target check —
does this binary still classify a `DROP TABLE`? — is the whole reason builds are
native rather than central. In a tool that gates merges, an untested binary is
worth less than no binary.

**Intel Macs:** build from source (`CGO_ENABLED=1 go install
github.com/VIKOIT/reversibility-engine/cmd/revctl@latest`) or use the Docker
image. Prebuilt binaries target Apple Silicon.

The action now says this directly. An Intel Mac runner previously would have
404'd on a missing asset, which reads as a broken release rather than an
unsupported runner; it now fails naming the remedy.

### Added

**Terraform plan analyzer.** `revctl check` now classifies `terraform show -json`
output: 10 rules (`TF001`–`TF010`) over a plan, backed by a catalog of AWS
resource types.

**It never reads `terraform.tfstate`.** State holds provider credentials and
attribute values in plaintext; a plan does not, and there is no code path that
opens one.

**Only destruction is classified.** A created or updated-in-place resource has a
reverse by construction, which is what keeps the catalog finite — the problem was
never "hundreds of AWS resource types", it is the types whose destruction hurts.

The discriminator, in three clauses: a change is irreversible if it destroys data,
destroys an identity that re-applying the same configuration cannot recreate, **or
destroys a recovery capability a future rollback would depend on.** The third
clause is why `TF004` grades a one-line `deletion_protection = false` alongside
deleting a snapshot — both destroy the undo rather than the system.

Classification runs in layers: evidence in the plan first (an attribute like
`allocated_storage` marks a type stateful whatever the catalog says), then the
embedded catalog, then user `terraform_types` in `.reversibility.yml`, then
`TF010`/UNKNOWN. Evidence and overrides may only ever raise severity. **The type
name is never matched** — `aws_db_subnet_group` contains "db" and holds nothing.

**92 AWS resource types classified, of roughly 1,400.** The stateless half is
load-bearing rather than filler: an unclassified deleted type grades F, so the
network, IAM, load-balancing and compute entries are what stand between a new user
and an immediate failing gate.

**No telemetry, and `check` never fetches.** The catalog is compiled in and works
offline for the lifetime of the binary. When a plan destroys a type the catalog
does not know, the certificate prints **one** `.reversibility.yml` snippet and
**one** pre-filled issue link covering every unknown type — one, because six paste
operations is where somebody switches the gate off instead. Nothing is sent
anywhere; a human chooses to open the link.

`revctl catalog show` prints the catalog's version, digest and coverage.
`revctl catalog scan` is a maintainer tool that proposes candidates from a
provider schema — it needs terraform on PATH, says so clearly when it is missing,
and nothing in the check path depends on it.

`--terraform-plan <path>` analyzes a plan whatever it is named; the default
convention is `*.tfplan.json`, deliberately narrow so a stray `plan.json` in a
repository does not grade F.

**`TF003` is retired and its number will never be reused.** `prevent_destroy` is a
`lifecycle` meta-argument, which the JSON configuration representation does not
carry — and the signal is self-erasing, since a plan containing a delete already
proves `prevent_destroy` is not set. `TF001`/`TF002` catch the destroy itself.

**The certificate schema is now `1.4.0`** — `catalogVersion` was added, absent
unless a plan was analyzed.

### Added — the WILL_FAIL verdict

**`WILL_FAIL` — a new reversibility verdict.** It means the change will not apply
at all, as distinct from `IRREVERSIBLE`, which means it cannot be undone. One is a
risk to weigh; the other is a defect, and the fix belongs in the migration rather
than in the rollback plan. It ranks above `IRREVERSIBLE`, always grades **F**, and
is reported separately in the blockers, the undo plan, SARIF, and Markdown.

It is reached from evidence only. Today that means one thing: `SET NOT NULL`
against a column a production snapshot shows contains nulls. Postgres validates
every existing row and a single violation aborts the statement and rolls the
transaction back, so this is a certainty rather than a risk — and no lock duration
is estimated for it, because the statement never gets far enough to hold one.

**Lock duration bands.** With a snapshot, a lock hazard of `FULL_SCAN` or heavier
is bucketed by how long it is expected to be held:

| Band | Duration | Effect |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | grade no better than B |
| `OUTAGE` | over 5m | grade no better than C |

**A band may only lower a grade, never raise one** — lower meaning worse. A small
table does not turn a C into a B; the absence of evidence of a problem is not
evidence of safety. A missing snapshot, a stale one, or a fingerprint that does
not match is treated as absent, never as reassurance.

When `pg_relation_size` is unavailable, size falls back to
`reltuples × Σ(avg_width across ALL columns of the table)`. `avg_width` is per
column and the sum matters: one column's width is not a row width. When neither is
available the engine does not guess — the context is treated as absent for that
finding.

New fixture group `testdata/fixtures/context/`, one fixture per band plus both
`SET NOT NULL` cases, the fallback path, and an incomplete snapshot. Each names the
grade it has with the snapshot and without it, so the direction of the rule is
checkable as data. A mandatory regression asserts all 47 existing fixtures grade
identically when no snapshot is supplied.

**The certificate schema is now `1.3.0`.** `WILL_FAIL` is a new value in an
existing enum, so a consumer that switches exhaustively on reversibility has a case
it has not seen — the one change here that warrants attention rather than a
footnote.

### Added — production context

**Production context — `revctl snapshot` and `revctl check --context`.** The
engine can now say that a rewrite covers 212M rows and roughly 48 GiB, that
dropping an index nothing has read is genuinely cheap, and — most usefully — that
a `SET NOT NULL` **will fail** because the column already contains nulls, before
it runs rather than after.

**The engine still never connects to anything during analysis.** A separate
command collects metadata into a file and the analysis reads the file. CI
therefore never needs a production credential, certificates stay byte-identical
between runs, and the analyzers stay pure functions over a changeset. An
architecture test fails the build if `pgx` or `client-go` can be reached from
`internal/domain`, `internal/analyzer`, `internal/engine`, or `internal/snapshot`
through any number of hops — and separately asserts the collector still reaches
them, so the guard cannot go vacuous.

**Metadata only, and tested rather than asserted.** Table sizes, row estimates,
index sizes and scan counts, column null fractions; storage classes with their
reclaim policies, claim capacities, workload replica counts. No row of user data,
no column values, no Secrets, no connection string. CI seeds a throwaway database
with passwords, API keys, and private-key material, runs the collector, and fails
if any of them reaches the output. The PostgreSQL connection is opened with
`default_transaction_read_only=on`; Kubernetes access is `List` only.

**Context never changes a grade.** Enrichment writes one optional field on a
finding and touches nothing else. A property test runs every fixture in the
repository twice — with and without a snapshot deliberately sized to trip any
size-sensitive rule — and asserts the grade, effective grade, gate status, and
every individual classification are identical. A stale snapshot (older than seven
days) is used and flagged rather than discarded; a **missing** snapshot is not an
error at all.

New dependencies, quarantined to `internal/snapshot/collect`:
`github.com/jackc/pgx/v5` and `k8s.io/client-go`, both pinned to the newest
versions that still declare `go 1.22`.

**The certificate schema is now `1.2.0`** — findings gained an optional `context`
object, and the certificate gained `contextWarnings`. Nothing was removed or
redefined.

Documentation: [`docs/PRODUCTION-CONTEXT.md`](docs/PRODUCTION-CONTEXT.md) for what
is collected and the grants it needs, [`docs/ESTIMATES.md`](docs/ESTIMATES.md) for
every formula and where it will be wrong.

### Added — the policy file

**Policy file — `.reversibility.yml`.** Discovered by walking up from the analysis
path, or named with `--config`, or ignored with `--no-config`. It carries a gate
threshold, `ignore` globs, expiring waivers, and tighten-only overrides.

A waiver requires a `reason` and an `expires` date; missing either is a
configuration error that exits 2, not a warning. `expires` is a calendar day, at
most 180 days out, and an expired waiver is inert — the finding returns with no
edit to the file. A waiver may not cover an `UNKNOWN` finding, and waived findings
are reported under a new `Waived` section rather than suppressed.

**A waiver never improves the measurement.** `grade` is what the evidence says and
no policy setting moves it; the new `effectiveGrade` is `grade` with waived
findings set aside and is what a CI threshold compares. `aiGateStatus` follows
`grade`, so a waiver can unblock a human's pipeline and can never authorise an
autonomous agent to merge. Without a policy, `effectiveGrade` equals `grade`.

The resolved policy is hashed into `inputDigest` and reported as `policyDigest`.
The hash covers the resolved configuration rather than the file's bytes, so
reformatting or editing a comment does not change a certificate.

**The certificate schema is now `1.1.0`** — `effectiveGrade`, `waived`, and
`policyDigest` were added. Nothing was removed or given a new meaning, so a
consumer written against `1.0.0` reads exactly what it read before.

### Fixed

- **The `fs` provider tested its include predicate against the absolute path on
  disk**, so a policy `ignore` of `legacy/**` matched nothing and the files it
  named were analyzed anyway. Extension checks never noticed, because they only
  look at a suffix. The predicate now sees the path as it appears in the
  changeset, which is what the git and GitHub providers already did.
- **The engine analyzed its own configuration.** `.reversibility.yml` is YAML, so
  the Kubernetes analyzer claimed it and correctly reported `K8S014`/UNKNOWN for a
  document with no kind — meaning adopting a policy graded your repository F
  because of the file you adopted it with.

### Added — earlier in this release

**Git ref resolution.** `revctl check --base <ref>` resolves a changeset from two
git refs instead of two directories, with `--head` defaulting to `HEAD` and path
arguments acting as pathspecs that scope the comparison to a subtree. The
comparison is three-dot (`base...head`), so it runs against the merge base — the
same range a pull request shows.

Content is read out of the object database, never out of the working tree: a
dirty checkout cannot change the certificate. Unchanged siblings in touched
directories come back as context, matching what the GitHub App already supplies,
so the CLI and the app grade the same pull request the same way.

Failures name their fix rather than repeating git's wording — not a repository,
unknown ref, ambiguous ref, and the common CI case of a shallow clone missing the
base commit, which says `fetch-depth: 0`. All of them are exit 2, a run that did
not complete, never a passing grade.

The certificate schema is unchanged.

**Prebuilt binaries.** Releases publish `revctl` and `revsrv` for `linux/amd64`,
`linux/arm64`, `darwin/arm64`, and `windows/amd64`, with a `checksums.txt` covering
all of them. Each target is compiled on a runner of its own architecture, because
`CGO_ENABLED=1` is mandatory and the darwin target needs an SDK that cannot be
put on a Linux runner. Every build verifies that it still classifies a
`DROP TABLE` before it is packaged — a binary that quietly lost the parser would
grade every migration A for lack of findings.

**Automatic major tag.** Publishing `v1.2.0` repoints `v1` at it, so `@v1` means
the newest v1.x without a manual step after every release. Prereleases are
excluded — `v1.2.0-rc.1` never becomes what `@v1` resolves to.

### Changed

**The GitHub Action is now a composite action, shipped in `v1.1.0`.** The Docker
container action at `v1.0.0`–`v1.0.2` is frozen, not removed.

The action downloads the released binary for whichever runner it is on and
verifies its SHA-256 against the release's `checksums.txt` before executing it. A
mismatch, or a missing checksum line, installs nothing and fails the job. The
practical gain is that the action is no longer restricted to Linux runners and no
longer pays a container pull per run.

It also analyzes a git range now rather than a reconstructed pair of directories.
That fixes a real gap: the v1 staging pass filtered deletions out of the changeset,
so `K8S003`, `K8S006`, and every other removal rule could not fire on a pull
request. They now do.

New inputs: `base`, `format`, `sarif-upload`, `version`, and `config` — the last
reserved for the policy file and currently an error rather than a no-op, because a
policy file that was silently ignored would leave a user believing their waivers
applied. New outputs: `gate-status`, `findings-count`, `certificate-path`.
Findings are additionally emitted as annotations and appear inline on the diff.

`min-grade` is now `gate` and `include` is now `path`. Both old names still work
and emit a deprecation warning. Setting a name together with its replacement is an
error rather than a silent choice between them.

### Removed

**`entrypoint.sh`.** It existed to drive the container action, which the composite
action replaces. The published image remains, with `revctl` itself as its
entrypoint, as the way to run the CLI without installing a toolchain.

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

[Unreleased]: https://github.com/VIKOIT/reversibility-engine/compare/v1.1.2...HEAD
[v1.1.2]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v1.1.2
[0.1.0]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v0.1.0
