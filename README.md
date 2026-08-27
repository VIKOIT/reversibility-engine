# Reversibility Engine
![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go&logoColor=white)
![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0-663366)
![Status](https://img.shields.io/badge/status-v1.1.2-brightgreen)
![Policy](https://img.shields.io/badge/policy-fail--closed-critical)
![Rules](https://img.shields.io/badge/rules-27%20PG%20%C2%B7%2015%20K8S%20%C2%B7%209%20TF-blue)

[![CI](https://github.com/VIKOIT/reversibility-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/VIKOIT/reversibility-engine/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/VIKOIT/reversibility-engine)](https://github.com/VIKOIT/reversibility-engine/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/VIKOIT/reversibility-engine)](https://goreportcard.com/report/github.com/VIKOIT/reversibility-engine)




**"We can always roll back" is an assumption. This measures it.**

Reversibility Engine reads a pull request — PostgreSQL migrations, rendered
Kubernetes manifests, and Terraform plans — and answers one question before the
merge button is pressed: *if this goes wrong, can we undo it, and what exactly
would that cost?*

The answer is a reversibility certificate: a grade of **A, B, C, or F**, a
concrete undo plan, and an explicit list of what cannot be undone. Grade F means
a rollback would lose data. Autonomous coding agents may merge on grade A and on
nothing else.

```console
$ revctl check ./migrations

## Reversibility Certificate — Grade F

**Not reversible.** Rolling this back would lose data, or the engine could not
determine what it does.

| | |
| --- | --- |
| **Grade** | F |
| **AI merge gate** | ❌ FAIL |
| **Findings** | 1 |

### Blockers

- PG001 at 0042_drop_legacy.up.sql:1: irreversible — Dropping table legacy_orders
  destroys every row it holds; recreating the table restores the shape but not the data.

### Undo plan

    -- NO COMPLETE UNDO EXISTS. This changeset cannot be fully reversed.
    -- The following changes have no reverse:
    --   PG001 at 0042_drop_legacy.up.sql:1: DROP TABLE legacy_orders — cannot be undone
```

The engine is **fail-closed by construction**. An unparseable file, an
unrecognized statement, an analyzer error, or a panic all grade F. Unknown means
unsafe. A tool that sells trust cannot afford to guess.

A gate must also prove that it *ran*. A run that analyzed nothing exits **2**,
never `0` — absence of output is never success. See [Fail-closed by
construction](#fail-closed-by-construction).

> **Status: v1.1.2.** Usable end to end, and packaged as a GitHub Action. Every
> certificate carries its own `schemaVersion`, currently `1.5.0`, which bumps on
> any breaking field change — so a consumer can pin against the schema rather than
> against the tool. Every rule ID has a fixture pair in `testdata/`: a rule with no
> fixture does not exist.

---

## Install

**Most people should not install anything.** If the goal is to gate pull
requests, use the [GitHub Action](#github-action) — it carries the cgo build so
you do not have to. Install locally to run `revctl` by hand, to self-host
`revsrv`, or to develop the engine.

Requires **Go 1.22+ and a C toolchain**. The Postgres analyzer links the real
PostgreSQL parser through cgo, so `CGO_ENABLED=1` is mandatory — a
`CGO_ENABLED=0` build fails rather than silently dropping the parser, which is
the correct behaviour. See [ADR/0001](ADR/0001-parser-choice.md).

```bash
# Install with the Go toolchain
go install github.com/VIKOIT/reversibility-engine/cmd/revctl@latest
go install github.com/VIKOIT/reversibility-engine/cmd/revsrv@latest
```

Or build from a clone:

```bash
git clone https://github.com/VIKOIT/reversibility-engine.git
cd reversibility-engine

make build          # binaries land in ./bin
make verify         # everything CI runs: build, vet, lint, tests, coverage gate

# or without make:
CGO_ENABLED=1 go build -o bin/revctl ./cmd/revctl
CGO_ENABLED=1 go build -o bin/revsrv ./cmd/revsrv
```

Or download a prebuilt binary — no Go toolchain, no C compiler:

```bash
# Linux amd64; swap the asset name for your platform.
VERSION=v1.1.2
curl -fsSLO "https://github.com/VIKOIT/reversibility-engine/releases/download/$VERSION/revctl_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/VIKOIT/reversibility-engine/releases/download/$VERSION/checksums.txt"

# Verify before running it. This binary decides whether changes merge.
sha256sum --check --ignore-missing checksums.txt

tar -xzf revctl_linux_amd64.tar.gz revctl
sudo install revctl /usr/local/bin/
```

### Supported release targets

**Four targets, and every published artifact is built and verified on its own
native architecture.** `CGO_ENABLED=1` is mandatory, cgo cannot be cross-compiled
without a toolchain per target, and each build proves it still classifies a
`DROP TABLE` before it is packaged — a build that silently lost the parser would
grade every migration A for lack of findings.

| Target | Built on | Prebuilt binary |
| --- | --- | --- |
| `linux/amd64` | `ubuntu-latest` | ✅ |
| `linux/arm64` | `ubuntu-24.04-arm` | ✅ |
| `darwin/arm64` (Apple Silicon) | `macos-14` | ✅ |
| `windows/amd64` | `windows-latest` | ✅ |
| `darwin/amd64` (Intel Mac) | — | ❌ **dropped in `v1.1.2`** |

**`darwin/amd64` was dropped in `v1.1.2` and is not coming back by
cross-compilation.** `macos-13` is the only hosted Intel Mac runner and it queues
without bound — on the `v1.1.1` release it sat over two hours before the run had
to be cancelled by hand, and `timeout-minutes` does not help, because that clock
starts when a job begins *executing*. Cross-compiling from Apple Silicon was
considered and rejected: it would ship the one artifact nobody could
execution-test, and in a tool that gates merges an untested binary is worth less
than no binary. Restoring the target means a native Intel runner is available
again; nothing else qualifies.

**Intel Mac users have two supported paths**, stated plainly because they are
supported rather than degraded:

```bash
# 1. Build from source. Needs a C toolchain; Xcode command line tools are enough.
CGO_ENABLED=1 go install github.com/VIKOIT/reversibility-engine/cmd/revctl@latest

# 2. Or run the published image (see the Docker section below).
docker run --rm -v "$PWD:/repo" -w /repo \
  --entrypoint /usr/local/bin/revctl \
  ghcr.io/vikoit/reversibility-engine:1.1.2 check ./migrations
```

The action itself now fails on an Intel Mac runner with a message naming the
remedy, rather than 404ing on a missing asset — a missing asset reads as a broken
release, which is not what this is.

Any container image built around the binary must be **glibc-based**; musl cannot
load it — see [ADR/0001](ADR/0001-parser-choice.md).

---

## Quickstart — gate a pull request

Add this workflow and every pull request gets graded, with the certificate posted
as a comment that is replaced in place rather than appended to.

```yaml
# .github/workflows/reversibility.yml
name: Reversibility

on:
  pull_request:

permissions:
  contents: read
  pull-requests: write     # without this the certificate cannot be posted

jobs:
  certify:
    runs-on: ubuntu-latest   # or macos-latest, or windows-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0     # the base commit must be reachable to diff against

      - uses: VIKOIT/reversibility-engine@v1
        with:
          gate: B
```

That is the whole setup. There is no Go toolchain to install and no C compiler to
configure.

`gate` is the worst grade that still passes, and it gates **your** pipeline: it
follows the grade and honours policy waivers.

**An autonomous agent must read `gate-status`, not the job's exit code.** The two
are not the same and are not meant to be — `gate-status` is `PASS` only at grade
A *and* full coverage, so a change the engine only partly read fails it while the
job still succeeds. Set `require-full-coverage: true` if you want the job to fail
there too.

### This is a composite action, not a Docker action

**From `v1.1.0` onward the action is a composite action that downloads a verified
release binary.** Through `v1.0.2` it was a Docker container action. The
difference is visible in what your workflow can do:

| | Docker action (`v1.0.0`–`v1.0.2`, frozen) | Composite action (`v1.1.0`+) |
| --- | --- | --- |
| Runners | `ubuntu-*` only | **any** — Linux, macOS, Windows |
| Startup | pulls a container image every run | downloads one binary, cached when a version is pinned |
| What runs | the image's `ENTRYPOINT`, whatever it happens to be | `revctl` at a named version, **checksum-verified** |
| Toolchain on the runner | none | none |

The binary's SHA-256 is checked against the release's `checksums.txt` before it
executes, and a **missing** checksum line is equally fatal — there would be
nothing to verify against. This binary decides whether changes merge, so an
unverified one does not run.

A pinned `version:` is cached; `latest` deliberately is not, because an
`actions/cache` key is immutable once written and a key containing "latest" would
pin the first binary it ever saw and keep serving it forever.

> **Upgrading from `v1.0.x` — nothing to change.** Keep writing `@v1`. The
> container-to-composite transition happened *within* `v1` and needed no new
> major, because every input kept working. If your workflow pinned `runs-on:
> ubuntu-latest` only because the old action required it, that constraint is gone.
>
> `min-grade` became `gate` and `include` became `path`. Both old names still
> work. **Setting a name and its replacement together is an error, not a
> precedence decision** — resolving it silently would pick one during exactly the
> upgrade that introduced the mistake.
>
> **There is no `v2`, and there never was.** Every tag ever cut is `v1.x`. `@v1`
> is the line, and `@v1` is what to write. Older documentation that said `@v2` was
> pointing at a ref that resolves to nothing.

---

## Fail-closed by construction

Two separate invariants, and the second is newer than the first because it was
learned the expensive way.

**1. Unknown means unsafe.** An unparseable file, an unrecognized construct, an
analyzer error, or a panic all end in grade **F**. There is no "probably fine":
there is `REVERSIBLE`, `COSTLY`, `IRREVERSIBLE`, `UNKNOWN`, and `WILL_FAIL` — and
the last two both fail.

**2. A gate must prove that it ran.** Every rule above governs what happens once a
change has been *read*. None of them says anything about a run that read nothing
at all, and that run used to have the most permissive outcome in the system,
because "no findings" and "no analysis" produced the same green check.

### Exit codes are the contract with CI

| Code | Meaning |
| --- | --- |
| `0` | The run completed and the gate was met. |
| `1` | The run completed and **the gate was not met** — the change grades worse than the threshold. |
| `2` | **The run did not complete.** Nothing was analyzed, or the verdict cannot be trusted. |

Keeping "unsafe change" distinct from "broken tool" is what stops a broken tool
from being quietly ignored. Each of the following is held by a test that fails
when the gate stops gating, not by convention:

| Situation | Result |
| --- | --- |
| `revctl` with **no arguments** | **exit 2**, help printed to **stderr** — stdout is where a certificate goes |
| `revctl --help`, `revctl help` | exit 0 — asking for help is a different act, and without this users learn to ignore the exit code |
| The action wrote **no certificate file** | exit 2 |
| A certificate exists but its **verdict cannot be read back** | exit 2 |
| A policy that will not resolve (a waiver missing `reason` or `expires`) | exit 2 |
| Files that may be migrations that **no analyzer supports**, under a gate | **exit 2**, grade `N/A`, and the certificate names the files |
| `--require-full-coverage` and some file was not analyzed | **exit 2**, and stderr names each file and why |
| The provider cannot fetch the changeset (rate limit, 5xx, oversized diff) | grade **F**, and the certificate is still posted |
| A panic anywhere in an analyzer | grade **F**, rule `ENGINE_PANIC` |
| SQL that will not parse, or a YAML file that is not a manifest | grade **F** (`PG027` / `K8S014`, `UNKNOWN`) |

**A run that could not complete fails the job even with `fail-on-gate: false`.** A
broken analysis is not a passing one.

### Why `revctl` with no arguments exits 2

Because it once exited `0`, and that was the most dangerous line in the project.

The frozen `v1.0.x` Docker action pulls an image and names **no** `entrypoint:`
and **no** `args:` — it runs whatever the image declares. That entrypoint used to
be a script that performed the analysis; from `v1.1.0` it was `revctl` itself.
Publishing the `v1.1.0` image under the moving `:v1` tag therefore turned every
`@v1` consumer's gate into a green check over nothing. No rule misfired. No grade
was wrong. **There was no grade, and no grade passed.**

The moving tag was the vector. The defect was the exit code: *the one invocation
that analyzes nothing was also the only one that could never fail.*

What now stands in the way, at every layer:

- Bare `revctl` exits 2 and prints help to stderr.
- The action exits 2 when no certificate was written, and again when the grade
  cannot be read back out of one. A step reporting FAIL while passing the job has
  proved only that something was written.
- **An image whose no-argument run exits 0 is never published** — the self-test
  builds the image from the commit and asserts this, so it is caught before a tag
  moves rather than after.
- **Immutable image version tags only.** `:v1` and `:latest` are not published.
- **Every container invocation names `--entrypoint` explicitly.** An inherited
  entrypoint is a silent dependency on a value somebody else can change.

---

## Terraform plans

`revctl check` classifies `terraform show -json` output alongside migrations and
manifests. **It never reads `terraform.tfstate`**: state holds provider
credentials and attribute values in plaintext, a plan does not, and there is no
code path here that opens one.

```bash
# Produce a plan as JSON. The engine reads this JSON — never the binary plan
# file, and never the state file.
terraform plan -out=tfplan
terraform show -json tfplan > infra.tfplan.json

# Analyze it. *.tfplan.json is claimed by convention.
revctl check ./infra

# Or name a plan that is called something else.
revctl check --terraform-plan ./build/plan-output.json .
```

```console
$ revctl check ./infra

## Reversibility Certificate — Grade F

### Blockers

- TF001 at main.tfplan.json: irreversible — aws_db_instance.orders is being
  destroyed. aws_db_instance is stateful (the plan shows allocated_storage on it):
  destroying it destroys data, an identity that re-applying cannot recreate, or a
  recovery capability a rollback would need.

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🔴 | `TF001` | main.tfplan.json | IRREVERSIBLE | NONE | `aws_db_instance.orders (delete)` |
```

Note the `Lock` column: **lock hazard is always `NONE` for Terraform**, because
Terraform takes no database lock. The rationale names the *evidence* that decided
the classification — here `allocated_storage` on the `before` object, which marks
the type stateful before the catalog is even consulted.

**Only destruction is classified.** A created or updated-in-place resource has a
reverse by construction. That is what keeps the catalog finite: the problem was
never "hundreds of AWS resource types", it is *the types whose destruction
hurts*.

**The discriminator, in three clauses.** A resource change is irreversible if it
destroys data, destroys an identity that re-applying the same configuration
cannot recreate, **or destroys a recovery capability that a future rollback would
depend on.** The third clause is why a one-line `deletion_protection = false`
grades alongside deleting a snapshot — both destroy the undo rather than the
system, and the rationale says so in as many words.

**Classification runs in layers, and every layer may only raise severity:**

1. **Evidence in the plan, before the catalog.** An attribute such as
   `allocated_storage` or `backup_retention_period` on the `before` object marks a
   type stateful whatever the catalog says. *Presence means present and
   meaningfully set* — `null`, `""`, `[]` and `{}` are not evidence; `false` and
   `0` are, because the attribute existing at all is the schema signal. Evidence
   may only raise: its absence implies nothing, never "stateless".
2. **The embedded catalog** — [`catalog/terraform/aws.yaml`](catalog/terraform/aws.yaml),
   compiled into the binary.
3. **User `terraform_types`** in `.reversibility.yml` — classify an unknown type,
   or tighten a known one. Never weaken; weakening is a configuration error, and
   the path for accepting a known risk is a waiver, which carries a reason and an
   expiry.
4. Nothing matched, so nothing is assumed → `TF010`/UNKNOWN → **F**.

**The type name is never matched.** `aws_db_subnet_group` contains "db" and holds
nothing. A regex over type names would be a classification nobody could audit.

### The catalog

**92 AWS resource types out of roughly 1,400** — 48 stateful, 44 stateless. The
raw count and the honest denominator are both published, because a project that
knows its own limits reads better than one quoting a flattering ratio. The
stateless half is load-bearing rather than filler: an unclassified deleted type
grades F, so the network, IAM, load-balancing and compute entries are what stand
between a new user and an immediate failing gate.

```bash
revctl catalog show    # version, digest, and coverage of the embedded catalog
```

When a plan destroys a type the catalog does not know, the certificate prints
**one** `.reversibility.yml` snippet and **one** pre-filled issue link covering
every unknown type at once — one, because six paste operations is where somebody
switches the gate off instead. The suggested class is always `STATEFUL`: that is
the fail-closed direction, and a snippet that guessed `STATELESS` would be a
snippet that talked the user into the answer they wanted.

**Strictly no telemetry, and `check` never fetches.** Nothing in this codebase
sends anything anywhere. The catalog is compiled in and stays fully functional
offline for the lifetime of the binary — not on a cache miss, not when the catalog
is old, not ever. The issue link is a URL printed into a certificate the reader is
already looking at; a human chooses to open it.

`revctl catalog scan` is a **maintainer** tool. It shells out to `terraform
providers schema -json` to propose candidates, names what to install when
terraform is absent, and nothing in the check path depends on it.

The certificate carries `catalogVersion` — present only when a plan was actually
analyzed — and the catalog digest is mixed into `inputDigest`, so a verdict is
attributable to the catalog that produced it.

> **`TF003` is retired and its number is never reused.** `prevent_destroy` is a
> `lifecycle` meta-argument, which the JSON configuration representation does not
> carry — and the signal is self-erasing, since a plan containing a delete already
> proves `prevent_destroy` is not set. `TF001`/`TF002` catch the destroy itself,
> at the moment it matters. A retired ID with its reason tells a contributor the
> case was considered; a gap in the sequence reads as an oversight.

The authoritative table is [`docs/RULES.md` §5](docs/RULES.md#5-terraform--tf001-to-tf010).

---

## Production context

The engine classifies `ALTER COLUMN TYPE` as a table rewrite. Without seeing your
database it cannot say whether that is 200 milliseconds or 14 minutes.

```console
$ revctl snapshot --dsn "$REPLICA_DSN" --out .reversibility/pg.json
$ revctl check ./migrations --context .reversibility/pg.json

- PG017 at 0042_tighten.up.sql:1 — Adding NOT NULL to orders.shipped_at forces a
  scan of every row under lock…
  - In production: THIS MIGRATION WILL FAIL. Column orders.shipped_at currently
    contains nulls (about 31% of rows), and SET NOT NULL rejects the whole
    statement if any row violates it. Backfill the column first. That is roughly
    65.7M rows to fix.
  - Estimated FULL_SCAN lock: ~3m — an approximation from table size, not a
    measurement.
```

**The engine never connects to a database or a cluster during analysis.** A
separate command writes a snapshot file; the analysis reads the file. So CI never
holds a production credential, a certificate stays byte-identical between runs,
and the analyzers stay pure functions over a changeset. An architecture test fails
the build if the drivers can be reached from anywhere but the collector.

**Metadata only.** Table sizes, row estimates, index scan counts, column null
fractions; storage classes, claim capacities, replica counts. No row of user data,
no column values, no Secrets, no connection string. That is tested, not asserted:
CI seeds a throwaway database with passwords and API keys, runs the collector, and
fails if any of them appears in the output.

**Context is a one-way ratchet: it may make a verdict worse, never better.** A
classification whose severity would *drop* is discarded rather than applied, and
the lock hazard is restored unconditionally — context describes how long a lock is
held, never which lock is taken. A property test runs every fixture with and
without a snapshot sized to trip every band and asserts the graded-with-context
result is **never better** than the one without.

The vocabulary, because it has been ambiguous: *lowering* a grade means making it
worse (A → B → C → F) and is permitted; *raising* one means making it better and
never happens. **The absence of evidence of a problem is not evidence of safety**,
so a small table does not turn a C into a B, and no snapshot is ever read as a
signal that a change is safe.

**When context cannot be used, only the first two outcomes are quiet:**

| Situation | Outcome |
| --- | --- |
| The snapshot file is not there | Context absent. **Not an error** — a workflow can pass `--context` before the first snapshot exists. |
| The object is not in the snapshot, or the name is ambiguous | No context for that finding. Context that names the wrong object is worse than none. |
| The snapshot is stale (over 7 days) | **Used**, with a warning on the certificate. |
| The file exists and cannot be read or decoded | **Exit 2.** |
| Two snapshots of one kind describe different sources | **Exit 2**, naming both `--environment` labels and both fingerprints. |

A source mismatch is the loudest thing that can happen here, not the quietest:
merging two databases into one view would answer questions about a table that
exists in only one of them, with no way to tell which.

Exactly two things reach a verdict from a snapshot:

**`WILL_FAIL` — the migration will not apply at all.** A new verdict, distinct
from `IRREVERSIBLE`: one means you cannot undo the change, the other means it will
not happen, so the fix belongs in the migration rather than in the rollback plan.
A reader who confuses them fixes the wrong thing, which is why it is reported
apart from `IRREVERSIBLE` in the blockers, the undo plan, SARIF, and Markdown. It
ranks above every verdict an analyzer can produce from source alone and always
grades **F**. Today it is reached from one piece of evidence: `SET NOT NULL`
against a column a snapshot shows contains nulls. Postgres validates every
existing row and one violation aborts the statement, so this is a certainty rather
than a risk — and no lock duration is estimated for it, because the statement
never gets far enough to hold one.

**Lock duration bands.** With a snapshot, a lock hazard of `FULL_SCAN` or heavier
is bucketed by how long it is expected to be held:

| Band | Duration | Effect on the grade |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | no better than **B** |
| `OUTAGE` | over 5m | no better than **C** |

A band exists only where duration actually scales with size. `PG014` takes an
`EXCLUSIVE` lock, but dropping an index is not slower for being large and no rate
is defined for it — applying a scan rate there would cap a grade at C for an
operation that finishes in milliseconds.

**A waiver cannot cover `WILL_FAIL`**, the same as `UNKNOWN`: a waiver accepts a
trade-off, and there is no trade-off in a statement that cannot apply. Waiving it
would document a bug rather than accept one, and the pipeline it unblocked would
fail at deploy instead of at review.

**Stale context is used and flagged, never discarded** — it lands in
`contextWarnings`. Falling back to none would make the certificate quietly less
informative exactly when somebody stopped refreshing the snapshot. A **missing**
snapshot file is not an error at all; one that exists and cannot be read is exit 2.

**Estimates always carry a `~`.** The throughput constants are hard-coded and
deliberately not configurable: a knob would let somebody tune the estimate until
it was reassuring, and a number tuned to reassure is worse than no number.

Full detail, including required grants and how to run it on a schedule:
[`docs/PRODUCTION-CONTEXT.md`](docs/PRODUCTION-CONTEXT.md). Every formula behind
every number: [`docs/ESTIMATES.md`](docs/ESTIMATES.md).

---

## Policy — `.reversibility.yml`

A gate with no legitimate escape hatch gets switched off entirely the first time
it is wrong, and a gate that is switched off protects nothing. The escape hatch is
a file, discovered by walking up from the analysis path:

```yaml
version: 1
gate: A                    # minimum passing grade

ignore:
  - "legacy/**"
  - "**/*.generated.sql"

waivers:
  - rule: PG012
    path: "migrations/0031_*.sql"
    reason: "expand-contract; old code removed in #482"
    expires: 2026-10-01
    approved_by: "vikoit"

overrides:                 # tighten only — never loosen
  - rule: K8S008
    severity: IRREVERSIBLE

terraform_types:           # classify a resource type the catalog does not know
  - type: google_sql_database_instance
    class: STATEFUL        # STATEFUL or STATELESS; never weakens the catalog
```

**A waiver never improves the measurement.** The certificate keeps two numbers:

| Field | Meaning |
| --- | --- |
| `grade` | What the evidence says. A `DROP TABLE` is irreversible whoever signed off on it, so no policy setting moves this. |
| `effectiveGrade` | `grade` with waived findings set aside. This is what a CI threshold compares — and it equals `grade` whenever no policy applied, so it is always the right field to gate on. |
| `aiGateStatus` | PASS only when `grade` is A. It follows the measurement, so **a waiver can unblock your pipeline and can never let an autonomous agent merge.** |

The rules, all enforced:

- `reason` and `expires` are **required**. Missing either is a configuration
  error and exits 2 — not a warning, because a warning in a CI log is not read
  and the waiver would apply anyway.
- `expires` is a date, never a duration, and at most 180 days out. A duration is
  relative to a moment nobody records, so it renews on every run and never
  expires. An expired waiver is inert and the finding returns on its own.
- A waived finding is **reported, never deleted** — it appears under `Waived`
  with its reason and expiry, and as a SARIF `suppression` at its original
  severity. Silent suppression is how a safety tool stops being one.
- A waiver **cannot cover an `UNKNOWN` finding or an analyzer error.** Accepting a
  risk nobody has characterised is not a decision anyone is in a position to make.
- `overrides` may only make a rule stricter. One that weakens a finding is a
  configuration error.
- `terraform_types` classifies a resource type the embedded catalog does not
  carry, or tightens one it does. It may **never** weaken a catalog entry — that
  path is a waiver, which carries a reason and an expiry. See
  [Terraform plans](#terraform-plans).
- The resolved policy is hashed into `inputDigest` and reported as
  `policyDigest`, so a verdict is attributable to its configuration. Reformatting
  the file or editing a comment does not change either.

> **A waiver's `path` matches the finding's `file` exactly as the certificate
> prints it.** Under the action and `--base` that is repository-relative
> (`migrations/0031_backfill.sql`), which is what the example above assumes.
> Running `revctl check ./migrations` by hand reports paths relative to the
> directory you named, so the same waiver would need `path: "0031_*.sql"`. A
> pattern that matches nothing is silently inert rather than approximately
> applied — over-matching a waiver is the one direction this must not fail in.

`--config` names a policy explicitly; `--no-config` ignores one entirely, which is
how you see what the gate says without it.

---

## What it checks

| Domain | Coverage |
| --- | --- |
| PostgreSQL `.sql` migrations | 59 classified rules (PG001–PG059) over a real PostgreSQL AST — dropped tables and columns, truncation, `CASCADE`, narrowing type changes, unqualified `DELETE`/`UPDATE`, lock hazards, and down-migration presence |
| Rendered Kubernetes `.yaml` | 15 classified rules (K8S001–K8S015) over a structural diff — volume claim templates, selector mutations, PVC and storage-class changes, digest-pinned vs. floating images, removed probes, and workload strategy changes |
| Terraform `*.tfplan.json` | **9 active rules** (`TF001`–`TF010`, of which `TF003` is retired and never reused) over `terraform show -json`, backed by an embedded catalog of 92 AWS resource types. Only destruction is classified — a create or an in-place update has a reverse by construction. **State files are never read**: they hold credentials in plaintext. See [Terraform plans](#terraform-plans) |

The full, authoritative rule tables live in [`docs/RULES.md`](docs/RULES.md).
They are the specification, not documentation of the code — the code is written
to match them.

## Grades

| Grade | Meaning | AI merge gate |
| --- | --- | --- |
| **A** | Fully reversible, no significant lock hazard, down migration present and valid | ✅ PASS |
| **B** | One or two costly-to-reverse changes, a lock heavier than SHORT, or a `DISRUPTIVE` lock band | ❌ FAIL |
| **C** | Three or more costly changes, a missing/unparseable down migration, or an `OUTAGE` lock band | ❌ FAIL |
| **F** | Irreversible data loss, a change that **will not apply**, an unknown construct, or an analyzer failure | ❌ FAIL |
| **N/A** | **The engine did not analyze this change**, so there is no measurement. Never a pass. | ➖ NOT APPLICABLE |

**`A` means analyzed and found reversible, and nothing else.** A changeset with
nothing this engine reads does not grade A — it grades `N/A`, and the certificate
says which of two things happened:

| `outcome` | What it means | Exit under `--gate` |
| --- | --- | --- |
| `ANALYZED` | An analyzer claimed at least one file. The grade above is a real measurement. | `0` / `1` by the grade |
| `NO_CANDIDATES` | Nothing here this engine reads — a docs-only pull request, Go source alone. Genuinely nothing to assess. | **`0`** |
| `UNSUPPORTED_CONTENT` | Files that plausibly **are** migrations, and no analyzer can read them — Django `.py` migrations, Rails `.rb` migrations. | **`2`** |

The last row is the one that matters. Until schema `1.5.0` a pull request of
thirteen Django migrations graded **A** with gate **PASS**, because "no findings"
and "no analysis" were the same value. If you gate on `grade != 'F'`, switch to
`grade == 'A'` or read `outcome`; if you gate on `aiGateStatus == 'PASS'` or on
the exit code, you are already correct.

### Coverage — how much of the change was read

A changeset can be part readable and part not: one `.sql` migration beside three
Django `.py` ones. **Coverage is a second axis, and it is deliberately not folded
into the grade.**

| `coverage` | Meaning |
| --- | --- |
| `FULL` | Everything any analyzer could claim was claimed. A change with nothing claimable is vacuously full — nothing was skipped. |
| `PARTIAL` | Files that may be migrations went unread. `unanalyzedFiles` names every one, with the reason. |

- **`PARTIAL` never changes the grade.** A file this engine cannot read is not
  evidence that your change is unsafe. Inventing severity from ignorance is the
  mirror of inventing safety from it.
- **`aiGateStatus` is `PASS` only at grade A *and* `FULL` coverage.** An
  autonomous agent gets no merge on a change that was only partly understood. You
  can read the list of skipped files and decide; it cannot.
- **The certificate names every unanalyzed file, above the findings**, so you
  never have to infer what was skipped.

```console
$ revctl check --gate ./db/migrate          # one .sql, one .rb
grade A · coverage PARTIAL · aiGateStatus FAIL · exit 0

$ revctl check --gate --require-full-coverage ./db/migrate
revctl: --require-full-coverage: 1 file(s) that may be migrations were not analyzed
  - db/migrate/0002_backfill.rb (no analyzer reads .rb migrations)
exit 2
```

**The exit code and `aiGateStatus` diverge here on purpose.** The exit code is
your pipeline's gate — it follows the grade and honours waivers — and
`--require-full-coverage` is how you opt into the agent's stricter bar. Teams
standardised on a migration format this engine cannot read will want it on.

The five verdicts a finding can carry, in severity order:

```
REVERSIBLE  <  COSTLY  <  UNKNOWN  <  IRREVERSIBLE  <  WILL_FAIL
```

`UNKNOWN` and `WILL_FAIL` both grade **F**, and for different reasons: one is a
change nobody understood, the other is a change that cannot succeed. Neither can
be covered by a waiver.

---

## Rules Specification

Every grade this engine emits traces back to one table.
**[`docs/RULES.md`](docs/RULES.md)** is that specification — the code is written
to match it, and where the two disagree, the code is the bug.

| Section | Covers |
| --- | --- |
| [§1 PostgreSQL](docs/RULES.md#1-postgresql--pg001-to-pg059) | PG001–PG059: what each statement does to reversibility and lock hazard, the AST parser directive, and the three levels of down-migration validation |
| [§2 Kubernetes](docs/RULES.md#2-kubernetes--k8s001-to-k8s015) | K8S001–K8S015: volume claim templates, selector immutability, PVC and storage-class changes, digest pinning, dangling config references |
| [§3 Scoring](docs/RULES.md#3-scoring) | how findings become A/B/C/F, how the undo plan is assembled, and the determinism requirement |
| [§4 Owner rulings](docs/RULES.md#4-owner-rulings) | the decisions that resolved genuine ambiguities in the tables above |
| [§5 Terraform](docs/RULES.md#5-terraform--tf001-to-tf010) | TF001–TF010: the destruction discriminator, the classification layers, the closed list `TF004` fires on, and why `TF003` is retired |

Two rules govern changing any of it: **a rule with no fixture does not exist**,
and **no code path may turn an error, a panic, or an unknown into a passing
grade.** Disagreement about a classification is the most valuable contribution
there is — open an issue arguing the case.

---

## Why not `squawk`, `atlas lint`, or `kube-linter`?

Those tools are good and this one does not replace them. They answer **"is this
statement safe to apply?"** — will it take a heavy lock, will it break under
load, does it violate a style rule.

Reversibility Engine answers a different question: **"can this changeset be taken
back?"** That difference produces three things they do not:

- **A verdict on the whole changeset**, not per-statement warnings. Ten safe
  statements plus one `DROP COLUMN` is an irreversible change, and the grade says
  so.
- **An undo plan** — the actual reverse steps, in reverse order, or an explicit
  statement that no complete undo exists.
- **A machine-readable gate** designed for autonomous agents, where "no warnings"
  is not the same as "provably reversible."

A migration can be perfectly safe to apply and still be impossible to reverse.
That gap is the entire product.

---

## Usage

Three interfaces over one decoupled core. The engine itself knows about none of
them.

### GitHub Action

**A composite action**, not a Docker action — see [This is a composite action,
not a Docker action](#this-is-a-composite-action-not-a-docker-action). It
downloads the released binary for the runner it is on, **verifies its SHA-256
against the release's `checksums.txt`**, grades the change, posts the certificate,
annotates the diff, and fails the job when the grade is below `gate`. It runs on
Linux, macOS, and Windows runners, and needs no toolchain on any of them.

```yaml
- uses: VIKOIT/reversibility-engine@v1
  with:
    gate: A                            # A, B, C, or F
    path: 'db/migrations k8s'
    comment: true
```

**Inputs**

| Input | Default | Meaning |
| --- | --- | --- |
| `path` | `.` | Space-separated git pathspecs selecting what to analyze. |
| `base` | auto | Ref to compare against. A pull request uses its base commit and a push the commit it moved from; any other event must set this. |
| `gate` | `A` | Worst grade that still passes. `F` accepts everything, turning the action into a reporter. |
| `format` | `markdown` | Certificate written to the workspace: `markdown`, `json`, or `sarif`. |
| `comment` | `true` | Post the certificate to the pull request, replacing this action's previous one. |
| `sarif-upload` | `false` | Upload findings to code scanning. Needs `security-events: write`. |
| `exclude` | `.github/** action.yml` | Pathspecs to skip, applied on top of `path`. |
| `fail-on-gate` | `true` | Fail the job when the grade is below `gate`. |
| `version` | the action's own ref | Release of `revctl` to run. Pin a full version for a reproducible build **and** for the download to be cached — `latest` is deliberately never cached. A major-only ref such as `@v1` has no release of its own, so it resolves to the latest release. |
| `binary` | — | Path to an existing `revctl`, skipping the download. It exists so this repository can test the action against an unreleased build; consumers have no reason to set it. |
| `github-token` | `${{ github.token }}` | Reads the pull request and posts the comment. |
| `config` | — | **Setting this is an error, not a no-op.** Naming a policy file explicitly is not wired up yet, and an input read by nothing would leave a user believing their waivers applied. A `.reversibility.yml` at the repository root **is** picked up regardless — the action does not disable discovery. |

**Outputs:** `grade`, `gate-status`, `findings-count`, and `certificate-path`.
Findings are also emitted as annotations, so they appear inline on the diff.

**The verdict is read back out of the JSON certificate, never re-derived in
shell.** A second definition of the grade is a second chance to get it wrong in
the permissive direction. `jq` is checked for up front, because a runner lacking
it would otherwise yield an empty grade read from nowhere — the one shape of
failure that can look like a pass.

**Narrow `path` to where your migrations and manifests actually live.** The
default claims every `.sql` and `.yaml` in the repository, and the Kubernetes
analyzer cannot distinguish a file that is not a manifest from a manifest it
could not read — it reports `K8S014`/UNKNOWN for both, which is the correct
fail-closed answer and the reason to be deliberate about what it is shown. The
`exclude` defaults cover this repository's own YAML; no default can cover yours.

A run that could not complete fails the job even with `fail-on-gate: false`. A
broken analysis is not a passing one.

An image is published to `ghcr.io/vikoit/reversibility-engine`, so `revctl` can
be run outside Actions without installing anything — including on an Intel Mac,
which no longer receives a prebuilt binary:

```bash
docker run --rm -v "$PWD:/repo" -w /repo \
  --entrypoint /usr/local/bin/revctl \
  ghcr.io/vikoit/reversibility-engine:1.1.2 check ./migrations
```

**Immutable version tags only, and name the entrypoint.** There is deliberately
no `:v1` or `:latest` image tag to pull. A moving image tag once pointed a frozen
Docker action at a newer image whose entrypoint had changed underneath it, and
the result was a gate that reported success having analyzed nothing — the exact
failure this tool exists to prevent, introduced by a tag pattern rather than by
any rule. Naming `--entrypoint` costs one line and means an image change can
never silently redefine what your command runs.

### CLI — `revctl`

Four commands: `check`, `snapshot`, `catalog`, and `version`.

```bash
# Grade a directory of migrations. Every file is treated as newly added,
# which is the shape of a migration pull request.
revctl check ./migrations

# Compare two rendered trees. The Kubernetes rules need both sides to see
# what a change replaced.
revctl check --before ./k8s/base ./k8s/head

# Compare two git refs instead of two directories. The comparison runs against
# the merge base — the same range a pull request shows — and content is read
# from the object database, so uncommitted edits cannot change the certificate.
revctl check --base origin/main

# Any ref works, --head defaults to HEAD, and a path argument acts as a git
# pathspec scoping the comparison to a subtree.
revctl check --base v1.2.0 --head HEAD ./migrations

# Terraform: *.tfplan.json is claimed by convention, or name a plan explicitly.
revctl check ./infra
revctl check --terraform-plan ./build/plan-output.json .

# Policy: one is discovered by walking up from the analysis path. Name one
# explicitly, or run without any to see what the gate says unconfigured.
revctl check ./migrations --config ops/.reversibility.yml
revctl check ./migrations --no-config

# Production context, collected beforehand and read from a file. The check
# itself never connects to anything. --environment is a label recorded in the
# file; it is what names the offending snapshot if two of them ever disagree
# about which database they came from.
revctl snapshot --dsn "$REPLICA_DSN" --environment prod --out .reversibility/pg.json
revctl snapshot --kube-context prod --environment prod --out .reversibility/k8s.json
revctl check ./migrations --context .reversibility/pg.json

# The embedded Terraform catalog: version, digest, coverage.
revctl catalog show

# The certificate schema version this build emits.
revctl version

# Machine-readable output for a pipeline gate.
revctl check ./migrations --format json --gate

# SARIF for GitHub code scanning, written to a file for upload.
revctl check ./migrations --format sarif --output results.sarif
```

**Exit codes are the contract with CI: `0` success, `1` the gate was not met, `2`
the run did not complete.** A pipeline can therefore distinguish *"the change is
unsafe"* from *"the tool broke"* — conflating those is how a broken tool ends up
ignored. **`revctl` with no arguments is exit 2**, not a friendly zero; see
[Fail-closed by construction](#fail-closed-by-construction) for why that
distinction is the most important one in the tool.

### GitHub App — `revsrv`

A stdlib `net/http` webhook server. On every `pull_request` event it fetches the
changed files, grades them, and posts the certificate back to the PR — updating
its previous comment rather than adding a new one on every push.

```bash
export GITHUB_WEBHOOK_SECRET=...      # required; authenticates every delivery
export GITHUB_APP_ID=123456           # GitHub App credentials, or...
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/revsrv/key.pem
# export GITHUB_TOKEN=ghp_...         # ...a single static token instead

revsrv                                # listens on :8080, or $REVSRV_ADDR
```

Endpoints: `POST /webhook` and `GET /healthz`.

Three properties are worth stating plainly, because they are what make the app
trustworthy rather than merely functional:

- **Zero trust.** Only `X-Hub-Signature-256` is honoured — the legacy SHA-1
  header is ignored, so it cannot be used to downgrade the check. Comparison is
  constant-time, the body is size-capped before it is read, and a server started
  without a secret rejects everything. Nothing is parsed or logged before the
  signature verifies.
- **Fail-closed on the network.** If GitHub rate-limits or errors while fetching
  the diff, the app does not analyze whichever files arrived. It posts a grade F
  naming the failure. A pull request with no certificate is indistinguishable
  from one nobody checked.
- **Full context, not just the diff.** Some rules need files the change never
  touched — the StorageClass behind a deleted PVC, the Deployment still mounting
  a deleted ConfigMap. The provider fetches those from the base commit, or the
  rules would go blind in production.

---

## Determinism

Identical input produces a byte-identical certificate. No timestamps, no UUIDs,
no hostnames, no map-iteration order. Certificates are therefore diffable,
cacheable, and safe to use as a merge gate — a rerun never changes the verdict.

---

## Limitations

Stated plainly, because a safety tool that overstates its coverage is worse than
none.

- **Static analysis only during a check.** The engine reads files; it never
  connects to a database or a cluster while grading. `revctl snapshot` closes part
  of the gap by collecting metadata beforehand — see
  [Production context](#production-context) — but a snapshot is a description of
  the past, and every classification other than `WILL_FAIL` and the lock duration
  bands is still made from the source alone.
- **Git resolution needs the base commit present.** `--base origin/main` compares
  two refs through the merge base, the same comparison a pull request shows — but
  a shallow CI checkout does not contain the base commit. Set `fetch-depth: 0` on
  `actions/checkout`; the error says so when it happens.
- **Rendered manifests only.** Helm charts and Kustomize overlays must be
  rendered before analysis (`helm template`, `kustomize build`).
- **PostgreSQL only.** MySQL, SQL Server, and MongoDB are not supported.
- **Terraform is plan-only, and AWS-only.** The analyzer reads
  `terraform show -json` output; it does not parse `.tf` source, and it
  deliberately never opens `terraform.tfstate`. The catalog covers **92 AWS
  resource types of roughly 1,400** — every other provider, and every AWS type not
  in it, grades `TF010`/UNKNOWN and therefore **F** on destruction. That is the
  fail-closed answer rather than a silent pass; classify the type in
  `.reversibility.yml` or contribute it upstream through the link the certificate
  prints.
- **Removing `prevent_destroy` cannot be detected**, which is why `TF003` is
  retired. A plan carries no `lifecycle` block, and a plan containing a delete
  already proves `prevent_destroy` is not set. `TF001`/`TF002` catch the destroy
  itself.
- **Prebuilt binaries do not cover Intel Macs.** `darwin/amd64` was dropped in
  `v1.1.2`; build from source or use the Docker image. See
  [Supported release targets](#supported-release-targets).
- **Down migrations are checked structurally, not semantically.** The engine
  verifies that a down migration exists, parses, and roughly inverts the up. It
  cannot prove the two are true inverses.
- **The action needs `jq` on the runner.** Every GitHub-hosted runner has it. A
  self-hosted one may not, and the action fails loudly rather than reading an
  empty grade from nowhere.
- **The action cannot comment on a pull request from a fork.** A fork receives a
  read-only `GITHUB_TOKEN`. The gate still fails correctly, but the certificate
  cannot be posted. `pull_request_target` would fix it and is deliberately not
  the default — it runs the base branch's workflow with write access to a pull
  request containing untrusted code.
- **A YAML file that is not a Kubernetes manifest grades UNKNOWN.** The analyzer
  has no way to tell it apart from a manifest it failed to read, and guessing in
  the permissive direction is the one thing this engine will not do. Scope it
  with `path` and `exclude`.

---

## Architecture

```
cmd/revctl/   CLI entrypoint                cmd/revsrv/   GitHub App server
      |                                            |
      +---------------- internal/delivery/ --------+      thin transport shell
                              |
                        internal/engine/                  orchestrator, scorer,
                              |                           policy, enrichment
      +------------+----------+----------+------------+
      |            |                     |            |
internal/     internal/             internal/     internal/
 analyzer/     provider/             render/       policy/
  postgres/     fs / git /            json /        .reversibility.yml
  kubernetes/   github / fake         markdown /
  terraform/                          sarif       internal/snapshot/
   + catalog/terraform/aws.yaml                    collect/ — pgx and client-go
     (embedded at build time)                      live here and nowhere else
      |
                        internal/domain/                  types only, stdlib only
```

The core knows nothing about transport. Analyzers are pure functions over a
changeset — **no network, no disk, no git** — and all I/O happens behind a
`FileProvider`, implemented four times (`fs`, `git`, `github`, `fake`). Deleting
`internal/delivery/` must not break a single engine test.

The two live-collection drivers are quarantined **by a test, not by convention**:
`internal/snapshot/architecture_test.go` fails the build if `internal/domain`,
`internal/analyzer/...`, `internal/engine`, or `internal/snapshot` can reach `pgx`
or `client-go` through any number of hops — and separately asserts that
`internal/snapshot/collect` still can, so the guard cannot go vacuous. Analysis
links none of it.

---

## Development

```bash
make build        # build revctl and revsrv into ./bin
make test         # go test -race
make lint         # golangci-lint
make cover        # coverage profile + 85% gate on analyzer and engine
make fuzz         # fuzz all 9 targets (FUZZ_TIME=30s each)
make verify       # everything CI runs
make run-cli ARGS="check ./testdata/fixtures"
```

See [`ADR/0001`](ADR/0001-parser-choice.md) for the cgo tradeoff, the parser
stack-overflow guard, and the musl/Alpine constraint.

---

## Contributing

[`docs/SPECIFICATION.md`](docs/SPECIFICATION.md) is the contract: classification tables, scoring
rules, dependency rules, and testing requirements. Read it before changing anything.

Two rules matter most:

1. **A rule with no fixture does not exist.** Every rule ID has a fixture pair in
   `testdata/`.
2. **No code path may turn an error, a panic, or an unknown into a passing
   grade.**

Disagreement about a classification is the most valuable contribution there is —
open an issue arguing the case. The tables are the product; the code is an
implementation detail.

Issues labelled [`good first issue`](https://github.com/VIKOIT/reversibility-engine/labels/good%20first%20issue)
are scoped for a first contribution.

---


## License

Copyright (c) 2026 Abdul Ghani (VIKOIT). This project is **dual-licensed** — use
it under whichever fits you:

| | |
| --- | --- |
| **[AGPL-3.0-only](LICENSE)** | Free for everyone. Note section 13: if you modify the engine and expose it to users **over a network** — which is exactly what `revsrv` is for — you must offer those users your modified source. |
| **[Commercial license](COMMERCIAL.md)** | For organizations that cannot meet that obligation: embedding the engine in a proprietary product, running a modified copy as a hosted service, or working under a policy that excludes AGPL code. |

The AGPL was chosen deliberately. A merge gate whose rules cannot be inspected is
a gate that should not be trusted, and section 13 guarantees that anyone whose
pull requests are being graded by a modified copy of this engine can read the
rules it is grading them by.

Not sure which applies? [COMMERCIAL.md](COMMERCIAL.md) has a plain checklist, or
email **vikoit07@gmail.com**. Third-party dependency licenses are in
[NOTICE](NOTICE).

### Contributor License Agreement

Pull requests require a signed [**CLA**](CLA.md). Dual licensing only works if the
maintainer holds sufficient rights over every line, so the CLA grants the right to
license your contribution under both licenses above.

**You keep the copyright in your work** — it is a license grant, not an
assignment. A bot checks it on your first pull request; signing is one comment,
and you only do it once.
