# Reversibility Engine
![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go&logoColor=white)
![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0-663366)
![Status](https://img.shields.io/badge/status-v1.0.0-brightgreen)
![Policy](https://img.shields.io/badge/policy-fail--closed-critical)
![Rules](https://img.shields.io/badge/rules-27%20PG%20%C2%B7%2015%20K8S-blue)

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

> **Status: v1.0.0.** Usable end to end, and packaged as a GitHub Action. Every
> certificate carries its own `schemaVersion`, currently `1.0.0`, which bumps on
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

> **No prebuilt binaries.** Releases are tagged, but the only published artifact
> is the action's container image. Install from source using either method above.
> Any image must be glibc-based; musl cannot load the binary — see
> [ADR/0001](ADR/0001-parser-choice.md).

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
    runs-on: ubuntu-latest   # Docker actions run on Linux runners only
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0     # the base commit must be reachable to diff against

      - uses: VIKOIT/reversibility-engine@v1
        with:
          min-grade: B
```

That is the whole setup. There is no Go toolchain to install and no C compiler to
configure: the action ships the cgo build inside its image.

`min-grade` is the worst grade that still passes. Autonomous agents must run at
`A` — grade A is the only verdict that permits an agent to merge.

---

## What it checks

| Domain | Coverage |
| --- | --- |
| PostgreSQL `.sql` migrations | 27 classified rules (PG001–PG027) over a real PostgreSQL AST — dropped tables and columns, truncation, `CASCADE`, narrowing type changes, unqualified `DELETE`/`UPDATE`, lock hazards, and down-migration presence |
| Rendered Kubernetes `.yaml` | 15 classified rules (K8S001–K8S015) over a structural diff — volume claim templates, selector mutations, PVC and storage-class changes, digest-pinned vs. floating images, removed probes, and workload strategy changes |

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

The packaged form of `revctl`. It reconstructs the two trees the engine compares,
grades the change, posts the certificate, and fails the job when the grade is
below `min-grade`.

```yaml
- uses: VIKOIT/reversibility-engine@v1
  with:
    min-grade: A                       # A, B, C, or F
    include: 'db/migrations/*.sql k8s/**/*.yaml'
    comment: true
```

**Inputs**

| Input | Default | Meaning |
| --- | --- | --- |
| `github-token` | `${{ github.token }}` | Reads the pull request and posts the comment. |
| `min-grade` | `B` | Worst grade that still passes. `F` accepts everything, turning the action into a reporter. |
| `include` | `*.sql *.yaml *.yml` | Space-separated git pathspecs selecting what to analyze. |
| `exclude` | `.github/** action.yml` | Pathspecs to skip, applied on top of `include`. |
| `comment` | `true` | Post the certificate to the pull request. |
| `fail-on-gate` | `true` | Fail the job when the grade is below `min-grade`. |

**Outputs:** `grade`, `gate`, `applicable`, and `certificate` (path to the
rendered Markdown, also written to the workspace as
`reversibility-certificate.md`).

**Narrow `include` to where your migrations and manifests actually live.** The
defaults claim every `.sql` and `.yaml` in the repository, and the Kubernetes
analyzer cannot distinguish a file that is not a manifest from a manifest it
could not read — it reports `K8S014`/UNKNOWN for both, which is the correct
fail-closed answer and the reason to be deliberate about what it is shown. The
`exclude` defaults cover this repository's own YAML; no default can cover yours.

A run that could not complete fails the job even with `fail-on-gate: false`. A
broken analysis is not a passing one.

The action's image is published to `ghcr.io/vikoit/reversibility-engine`, so
`revctl` can be run outside Actions without a Go toolchain:

```bash
docker run --rm --entrypoint revctl \
  -v "$PWD:/repo" -w /repo \
  ghcr.io/vikoit/reversibility-engine:v1 check ./migrations
```

### CLI — `revctl`

```bash
# Grade a directory of migrations. Every file is treated as newly added,
# which is the shape of a migration pull request.
revctl check ./migrations

# Compare two rendered trees. The Kubernetes rules need both sides to see
# what a change replaced.
revctl check --before ./k8s/base ./k8s/head

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

- **Static analysis only.** The engine reads files; it never connects to a
  database or a cluster. It cannot see table sizes, actual row counts, or whether
  a column is truly unused.
- **No git ref resolution yet.** `--base origin/main` is not implemented. The
  filesystem provider compares paths, not revisions; git resolution ships with
  the GitHub App path.
- **Rendered manifests only.** Helm charts and Kustomize overlays must be
  rendered before analysis (`helm template`, `kustomize build`).
- **PostgreSQL only.** MySQL, SQL Server, and MongoDB are not supported.
- **No Terraform.** Cloud resource deletion is a real reversibility problem and a
  planned direction, not a current one.
- **Down migrations are checked structurally, not semantically.** The engine
  verifies that a down migration exists, parses, and roughly inverts the up. It
  cannot prove the two are true inverses.
- **The action runs on Linux runners only.** It is a Docker container action;
  `windows-latest` and `macos-latest` cannot run it.
- **The action cannot comment on a pull request from a fork.** A fork receives a
  read-only `GITHUB_TOKEN`. The gate still fails correctly, but the certificate
  cannot be posted. `pull_request_target` would fix it and is deliberately not
  the default — it runs the base branch's workflow with write access to a pull
  request containing untrusted code.
- **A YAML file that is not a Kubernetes manifest grades UNKNOWN.** The analyzer
  has no way to tell it apart from a manifest it failed to read, and guessing in
  the permissive direction is the one thing this engine will not do. Scope it
  with `include` and `exclude`.

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

