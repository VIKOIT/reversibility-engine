# Reversibility Engine

Everyone measures deployment frequency. Almost nobody measures whether a change can be taken
back. The Reversibility Engine statically analyzes a pull request — PostgreSQL migrations and
rendered Kubernetes manifests — and answers one question before the merge button is pressed:
**if this goes wrong, can we undo it, and what exactly would that cost?** The answer is a
signed-off *reversibility certificate*: a grade of A, B, C, or F, the concrete undo plan, and an
explicit list of what cannot be undone. Grade F means a rollback would lose data. Autonomous
coding agents may merge on grade A and on nothing else.

The engine is **fail-closed by construction**. An unparseable file, an unrecognized statement,
an analyzer error, or a panic all grade **F**. Unknown means unsafe. A tool that sells trust
cannot afford to guess.

> **Status: complete.** All seven sessions are done: engine, both analyzers, scorer, three
> renderers, the `revctl` CLI, the `revsrv` GitHub App, and S7 hardening. 1,010 tests pass with
> the race detector on. See the session plan in [CLAUDE.md](CLAUDE.md) §4.

## What it checks

| Domain | Coverage |
| --- | --- |
| PostgreSQL `.sql` migrations | 27 classified rules (PG001–PG027) over a real PostgreSQL AST — dropped tables and columns, truncation, `CASCADE`, narrowing type changes, unqualified `DELETE`/`UPDATE`, lock hazards, and down-migration presence |
| Rendered Kubernetes `.yaml` | 15 classified rules (K8S001–K8S015) over a structural diff — volume claim templates, selector mutations, PVC and storage-class changes, digest-pinned vs. floating images, removed probes, and workload strategy changes |

Full, authoritative rule tables live in [CLAUDE.md](CLAUDE.md) §9 and §10.

## Grades

| Grade | Meaning | AI merge gate |
| --- | --- | --- |
| **A** | Fully reversible, no significant lock hazard, down migration present and valid | **PASS** |
| **B** | One or two costly-to-reverse changes, or a lock heavier than SHORT | FAIL |
| **C** | Three or more costly changes, or a missing/unparseable down migration | FAIL |
| **F** | Irreversible data loss, an unknown construct, or an analyzer failure | FAIL |

## Usage

Two interfaces over one decoupled core.

### CLI — `revctl`

```sh
# Grade a directory of migrations. Every file is treated as newly added,
# which is the shape of a migration pull request.
revctl check ./migrations

# Compare two rendered trees. The Kubernetes rules need both sides to see
# what a change replaced.
revctl check --before ./k8s/base ./k8s/head

# Machine-readable output for a pipeline gate.
# --gate exits 1 unless the grade is A; this is the setting autonomous agents must use.
revctl check ./migrations --format json --gate

# SARIF for GitHub code scanning, written to a file for upload
revctl check ./migrations --format sarif --output results.sarif
```

Exit codes are the contract with CI: **0** success, **1** the gate was not met, **2** the run did
not complete. A pipeline can therefore distinguish "the change is unsafe" from "the tool broke" —
conflating those is how a broken tool ends up ignored.

```
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

> **Not yet implemented:** resolving a changeset from a git ref (`--base origin/main`). The
> filesystem provider compares paths, not revisions; git and GitHub resolution arrives with the
> GitHub App in S6.

### GitHub App — `revsrv`

A stdlib `net/http` webhook server. On every `pull_request` event it fetches the changed files,
grades them, and posts the certificate back to the PR — **updating its previous comment** rather
than adding a new one on every push.

```sh
export GITHUB_WEBHOOK_SECRET=...      # required; authenticates every delivery
export GITHUB_APP_ID=123456           # GitHub App credentials, or...
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/revsrv/key.pem
# export GITHUB_TOKEN=ghp_...         # ...a single static token instead

revsrv                                # listens on :8080, or $REVSRV_ADDR
```

Endpoints: `POST /webhook` and `GET /healthz`.

Three properties are worth stating plainly, because they are what make the app trustworthy
rather than merely functional:

- **Zero trust.** Only `X-Hub-Signature-256` is honoured — the legacy SHA-1 header is ignored, so
  it cannot be used to downgrade the check. Comparison is constant-time, the body is size-capped
  before it is read, and a server started without a secret rejects everything. Nothing is parsed
  or logged before the signature verifies.
- **Fail-closed on the network.** If GitHub rate-limits or errors while fetching the diff, the app
  does **not** analyze whichever files arrived. It posts a grade **F** naming the failure. A pull
  request with no certificate is indistinguishable from one nobody checked.
- **Full context, not just the diff.** Some rules need files the change never touched — the
  StorageClass behind a deleted PVC, the Deployment still mounting a deleted ConfigMap. The
  provider fetches those from the base commit, or the rules would go blind in production.

## Determinism

Identical input produces a **byte-identical** certificate. No timestamps, no UUIDs, no
hostnames, no map-iteration order. Certificates are therefore diffable, cacheable, and safe to
use as a merge gate — a rerun never changes the verdict.

## Development

Requires **Go 1.22+** and a **C toolchain** — the Postgres analyzer links the real PostgreSQL
parser through cgo, so `CGO_ENABLED=1` is mandatory. See
[ADR/0001](ADR/0001-parser-choice.md) for that tradeoff, the parser stack-overflow guard, and the
musl/Alpine constraint.

```sh
make build        # build revctl and revsrv into ./bin
make test         # go test -race
make lint         # golangci-lint
make cover        # coverage profile + 85% gate on analyzer and engine
make fuzz         # fuzz all 9 targets (FUZZ_TIME=30s each)
make verify       # everything CI runs
make run-cli ARGS="check ./testdata/fixtures"
```

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

The core knows nothing about transport. Analyzers are pure functions over a changeset — no
network, no disk, no git. Deleting `internal/delivery/` must not break a single engine test.

## Contributing

[CLAUDE.md](CLAUDE.md) is the contract: classification tables, scoring rules, dependency rules,
and testing requirements. Read it before changing anything. Two rules matter most:

1. **A rule with no fixture does not exist.** Every rule ID has a fixture pair in `testdata/`.
2. **No code path may turn an error, a panic, or an unknown into a passing grade.**

## License

TBD.
