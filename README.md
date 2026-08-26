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

Reversibility Engine reads a pull request — PostgreSQL migrations and rendered
Kubernetes manifests — and answers one question before the merge button is
pressed: *if this goes wrong, can we undo it, and what exactly would that cost?*

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

> **Status: v1.1.2.** Usable end to end, and packaged as a GitHub Action. Every
> certificate carries its own `schemaVersion`, currently `1.4.0`, which bumps on
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

Releases publish `revctl` and `revsrv` for `linux/amd64`, `linux/arm64`,
`darwin/arm64`, and `windows/amd64` — each built and verified on a runner of its
own architecture, because `CGO_ENABLED=1` cannot be cross-compiled to every
target and a binary nobody could execute is a binary nobody has tested. Any
container image must be glibc-based; musl cannot load the binary — see
[ADR/0001](ADR/0001-parser-choice.md).

**Intel Macs:** build from source (`CGO_ENABLED=1 go install
github.com/VIKOIT/reversibility-engine/cmd/revctl@latest`) or use the Docker
image. Prebuilt binaries target Apple Silicon.

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
configure: the action downloads the released binary for the runner it is on and
verifies its checksum before running it.

`gate` is the worst grade that still passes. Autonomous agents must run at `A` —
grade A is the only verdict that permits an agent to merge.

> **Upgrading from `v1.0.x`.** Through `v1.0.2` this was a Docker container
> action, so it ran on Linux only and paid an image pull every run. From `v1.1.0`
> it is a composite action and runs anywhere — same `@v1`, nothing to change.
> `min-grade` became `gate` and `include` became `path`; both old names still work
> and warn. Setting a name and its replacement at once is an error rather than a
> silent choice between them.
>
> There is no `v2`. `@v1` is the line, and `@v1` is what to write.

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

**Context never improves a grade — or changes one at all.** It writes one optional
field on a finding and touches nothing else. A property test runs every fixture
twice, with and without a snapshot sized to make any size-sensitive rule fire, and
asserts the grades are identical.

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
| PostgreSQL `.sql` migrations | 27 classified rules (PG001–PG027) over a real PostgreSQL AST — dropped tables and columns, truncation, `CASCADE`, narrowing type changes, unqualified `DELETE`/`UPDATE`, lock hazards, and down-migration presence |
| Rendered Kubernetes `.yaml` | 15 classified rules (K8S001–K8S015) over a structural diff — volume claim templates, selector mutations, PVC and storage-class changes, digest-pinned vs. floating images, removed probes, and workload strategy changes |
| Terraform `*.tfplan.json` | 10 classified rules (TF001–TF010) over `terraform show -json`, backed by a catalog of 92 AWS resource types. Only destruction is classified — a create or an in-place update has a reverse by construction. **State files are never read**: they hold credentials in plaintext |

The full, authoritative rule tables live in [`docs/RULES.md`](docs/RULES.md).
They are the specification, not documentation of the code — the code is written
to match them.

## Grades

| Grade | Meaning | AI merge gate |
| --- | --- | --- |
| **A** | Fully reversible, no significant lock hazard, down migration present and valid | ✅ PASS |
| **B** | One or two costly-to-reverse changes, or a lock heavier than SHORT | ❌ FAIL |
| **C** | Three or more costly changes, or a missing/unparseable down migration | ❌ FAIL |
| **F** | Irreversible data loss, an unknown construct, or an analyzer failure | ❌ FAIL |

---

## Rules Specification

Every grade this engine emits traces back to one table.
**[`docs/RULES.md`](docs/RULES.md)** is that specification — the code is written
to match it, and where the two disagree, the code is the bug.

| Section | Covers |
| --- | --- |
| [§1 PostgreSQL](docs/RULES.md#1-postgresql--pg001-to-pg027) | PG001–PG027: what each statement does to reversibility and lock hazard, the AST parser directive, and the three levels of down-migration validation |
| [§2 Kubernetes](docs/RULES.md#2-kubernetes--k8s001-to-k8s015) | K8S001–K8S015: volume claim templates, selector immutability, PVC and storage-class changes, digest pinning, dangling config references |
| [§3 Scoring](docs/RULES.md#3-scoring) | how findings become A/B/C/F, how the undo plan is assembled, and the determinism requirement |
| [§4 Owner rulings](docs/RULES.md#4-owner-rulings) | the decisions that resolved genuine ambiguities in the tables above |

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

The packaged form of `revctl`. It downloads the released binary for the runner it
is on, verifies its checksum, grades the change, posts the certificate, annotates
the diff, and fails the job when the grade is below `gate`.

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
| `version` | the action's own ref | Release of `revctl` to run. Pin it for a reproducible build. |
| `github-token` | `${{ github.token }}` | Reads the pull request and posts the comment. |
| `config` | — | Reserved for the policy file. Setting it is currently an error, not a no-op. |

**Outputs:** `grade`, `gate-status`, `findings-count`, and `certificate-path`.
Findings are also emitted as annotations, so they appear inline on the diff.

**Narrow `path` to where your migrations and manifests actually live.** The
default claims every `.sql` and `.yaml` in the repository, and the Kubernetes
analyzer cannot distinguish a file that is not a manifest from a manifest it
could not read — it reports `K8S014`/UNKNOWN for both, which is the correct
fail-closed answer and the reason to be deliberate about what it is shown. The
`exclude` defaults cover this repository's own YAML; no default can cover yours.

A run that could not complete fails the job even with `fail-on-gate: false`. A
broken analysis is not a passing one.

An image is published to `ghcr.io/vikoit/reversibility-engine`, so `revctl` can
be run outside Actions without installing anything:

```bash
docker run --rm -v "$PWD:/repo" -w /repo \
  --entrypoint /usr/local/bin/revctl \
  ghcr.io/vikoit/reversibility-engine:1.1.1 check ./migrations
```

**Immutable version tags only, and name the entrypoint.** There is deliberately
no `:v1` or `:latest` image tag to pull. A moving image tag once pointed a frozen
Docker action at a newer image whose entrypoint had changed underneath it, and
the result was a gate that reported success having analyzed nothing — the exact
failure this tool exists to prevent, introduced by a tag pattern rather than by
any rule. Naming `--entrypoint` costs one line and means an image change can
never silently redefine what your command runs.

### CLI — `revctl`

```bash
# Grade a directory of migrations. Every file is treated as newly added,
# which is the shape of a migration pull request.
revctl check ./migrations

# Compare two rendered trees. The Kubernetes rules need both sides to see
# what a change replaced.
revctl check --before ./k8s/base ./k8s/head

# Compare two git refs instead of two directories. The comparison runs against
# the merge base — the same range a pull request shows — and content is read
# from the refs, so uncommitted edits cannot change the certificate.
revctl check --base origin/main

# Any ref works, --head defaults to HEAD, and a path argument scopes the
# comparison to a subtree.
revctl check --base v1.2.0 --head HEAD ./migrations

# Machine-readable output for a pipeline gate.
revctl check ./migrations --format json --gate

# SARIF for GitHub code scanning, written to a file for upload.
revctl check ./migrations --format sarif --output results.sarif
```

Exit codes are the contract with CI: `0` success, `1` the gate was not met, `2`
the run did not complete. A pipeline can therefore distinguish *"the change is
unsafe"* from *"the tool broke"* — conflating those is how a broken tool ends up
ignored.

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
  the past, and the classification itself is still made from the source alone.
- **Git resolution needs the base commit present.** `--base origin/main` compares
  two refs through the merge base, the same comparison a pull request shows — but
  a shallow CI checkout does not contain the base commit. Set `fetch-depth: 0` on
  `actions/checkout`; the error says so when it happens.
- **Rendered manifests only.** Helm charts and Kustomize overlays must be
  rendered before analysis (`helm template`, `kustomize build`).
- **PostgreSQL only.** MySQL, SQL Server, and MongoDB are not supported.
- **No Terraform.** Cloud resource deletion is a real reversibility problem and a
  planned direction, not a current one.
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
cmd/revctl/          CLI entrypoint            cmd/revsrv/    GitHub App server
        |                                              |
        +--------------- internal/delivery/ -----------+     thin transport shell
                                |
                          internal/engine/                   orchestrator + scorer
                                |
        +-----------------------+-----------------------+
        |                       |                       |
internal/analyzer/       internal/provider/       internal/render/
  postgres/ kubernetes/    fs / github / fake       json/markdown/sarif
        |
                          internal/domain/                   types only, stdlib only
```

The core knows nothing about transport. Analyzers are pure functions over a
changeset — no network, no disk, no git. Deleting `internal/delivery/` must not
break a single engine test.

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

[`CLAUDE.md`](CLAUDE.md) is the contract: classification tables, scoring rules,
dependency rules, and testing requirements. Read it before changing anything.

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
