# CLAUDE.md — Reversibility Engine

**This file is the contract.** A fresh session with zero memory of prior work must be able to
read this file alone and continue correctly. If something here conflicts with your instinct,
this file wins. If something is not here, do not invent it — ask.

---

## 1. What this is

A static-analysis engine that measures whether a code change can be safely rolled back,
**before it is merged**. It emits a `ReversibilityCertificate` (grade A/B/C/F plus an undo plan)
and acts as a merge gate.

Autonomous AI agents may merge **only on grade A**.

Two interfaces over one decoupled core:

| Binary | Purpose |
| --- | --- |
| `revctl` | Open-source CLI (cobra) |
| `revsrv` | GitHub App webhook server (stdlib `net/http`) |

## 2. The philosophy: fail-closed

**The product sells trust. A wrong "safe" verdict destroys it.**

- Unknown means unsafe.
- Never guess.
- An error, a panic, an unparseable file, or an unrecognized construct **must never** become a
  passing grade. Every one of those paths terminates in **F**.
- There is no "probably fine". There is `REVERSIBLE`, `COSTLY`, `IRREVERSIBLE`, `UNKNOWN` — and
  `UNKNOWN` fails.

## 3. MVP scope

**In:** static analysis only. PostgreSQL `.sql` migrations. Rendered Kubernetes manifests (`.yaml`).

**Out — do not build, do not stub, do not leave a TODO for:** live DB rehearsal, Terraform,
Helm chart templating, cost estimation, web UI, auth/billing, AI/LLM calls of any kind.

## 4. Session plan

Execute **one session at a time**. Stop at the end of each and report. Do not start session
N+1 until the owner approves.

| Session | Deliverable | Status |
| --- | --- | --- |
| S0 | Repo bootstrap, CLAUDE.md, Makefile, CI workflow, module layout with empty packages. No logic. | **DONE** |
| S1 | `internal/domain` types + full test fixtures in `testdata/` + failing tests. No analyzer logic. | **DONE** |
| S2 | Postgres analyzer until all its fixtures pass. | **DONE** |
| S3 | Kubernetes analyzer until all its fixtures pass. | **DONE** |
| S4 | Scorer + certificate assembly + determinism tests. | **DONE** |
| S5 | Renderers (JSON, Markdown, SARIF) + Cobra CLI. | **DONE** |
| S6 | GitHub App webhook server. | **DONE** |
| S7 | Hardening: fuzz tests, panic-recovery boundary tests, golden-file determinism. | **DONE** |

The v0.2 plan continues the same way. Each session is written up in full in
`.claude/commands/s8.md` … `s12.md`, one file per session.

| Session | Deliverable | Status |
| --- | --- | --- |
| S8 | Git ref resolution: a `gitProvider` behind `--base` / `--head`. | **BUILT — awaiting approval** |
| S9 | GitHub Action (`action.yml`) + release workflow. | **BUILT — awaiting approval** |
| S10 | Policy file `.reversibility.yml` with expiring waivers. | **BUILT — awaiting approval** |
| S11 | Production context snapshots (`revctl snapshot`, `--context`). | |
| S12 | Terraform plan analyzer. | |

Update the Status column when a session is approved as complete.

## 5. Layout

```
cmd/revctl/                          CLI entrypoint (cobra)
cmd/revsrv/                          GitHub App server entrypoint
internal/domain/                     Types only. ZERO third-party imports.
internal/fixture/                    Fixture + expectation loader (test support; see note below)
internal/analyzer/                   Analyzer interface
internal/analyzer/postgres/
internal/analyzer/postgres/parser/   SQLParser interface, isolates cgo pg_query_go
internal/analyzer/kubernetes/
internal/engine/                     Registry, orchestrator, scorer
internal/provider/                   FileProvider interface: fs, git, github, fake
internal/policy/                     .reversibility.yml: parse, validate, apply
internal/render/                     json, markdown, sarif
internal/delivery/cli/
internal/delivery/github/
pkg/certificate/                     Public, versioned certificate schema
testdata/fixtures/
```

## 6. Dependency rules

**Hard rules — a violation is a bug, not a style preference:**

1. `internal/domain` imports **nothing** outside stdlib. Everything else depends on it; it
   depends on nothing.
2. Analyzers receive `[]domain.ChangedFile` and return `([]domain.Finding, error)`. They
   **never** touch network, disk, git, or GitHub. All I/O happens in a `FileProvider`.
3. Transport (`internal/delivery/`) is a thin shell. **Deleting `internal/delivery/` must not
   break engine tests.**
4. Accept interfaces, return structs.
5. Zero global mutable state.

**Allowed third-party dependencies — the complete list:**

| Module | Used by | Introduced in |
| --- | --- | --- |
| `github.com/spf13/cobra` | `internal/delivery/cli` | S5 (in go.mod) |
| `github.com/pganalyze/pg_query_go/v5` | `internal/analyzer/postgres/parser` | S2, **cgo** (in go.mod) |
| `github.com/google/go-github/v66` | `internal/delivery/github`, `internal/provider` | S6 (in go.mod) |
| `sigs.k8s.io/yaml` (JSON-typed decode) + `gopkg.in/yaml.v3` (stream decoder) | `internal/analyzer/kubernetes`; `sigs.k8s.io/yaml` alone in `internal/policy` | S3, S10 |
| `github.com/google/go-cmp` | tests only | S1 |

Adding anything else requires the owner's approval. `go.mod` is intentionally dependency-free
until the session that first needs a given module — do not pre-add them.

## 7. Core interfaces

```go
type Analyzer interface {
    Name() string
    Supports(path string) bool
    Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error)
}

type FileProvider interface {
    ChangedFiles(ctx context.Context, ref domain.ChangeRef) ([]domain.ChangedFile, error)
}
```

`FileProvider` is implemented **four** times: `fsProvider` (local dir/diff), `gitProvider`
(two refs, S8), `githubProvider`, `fakeProvider` (reads `testdata/`). Never write "simulated" or
placeholder fetch code — use `fakeProvider`.

## 8. Domain types

```go
type Reversibility string // REVERSIBLE, COSTLY, IRREVERSIBLE, UNKNOWN
type LockHazard   string // NONE, SHORT, FULL_SCAN, TABLE_REWRITE, EXCLUSIVE
type Grade        string // A, B, C, F

type Finding struct {
    RuleID        string        // stable, e.g. "PG001"
    File          string
    Line          int
    Statement     string        // normalized, truncated to 200 chars
    Reversibility Reversibility
    LockHazard    LockHazard
    Rationale     string        // why, in one sentence
    UndoStep      string        // "" if none possible
}

type ReversibilityCertificate struct {
    SchemaVersion  string    // "1.1.0" — bump on any breaking field change
    Grade          Grade     // the measurement; no policy may move it
    EffectiveGrade Grade     // Grade minus waived findings; what CI compares (S10)
    AIGateStatus   string    // PASS | FAIL — follows Grade, never EffectiveGrade
    Applicable     bool
    InputDigest    string    // SHA256 over sorted (path, content), plus the policy if any
    PolicyDigest   string    // SHA256 over the resolved policy, "" if none (S10)
    Findings       []Finding // sorted by File, then Line, then RuleID
    Waived         []WaivedFinding // findings a live waiver accepted (S10)
    UndoPlan       []UndoStep
    Blockers       []string  // human-readable reasons for F
}
```

**LockHazard severity ordering** — required to evaluate `>=` and `<=` in the scoring rules.
Derived from those rules, not invented:

```
NONE < SHORT < FULL_SCAN < TABLE_REWRITE < EXCLUSIVE
```

**As built in S1, three fields differ from the sketch above.** All three are typing decisions,
not semantic ones; the JSON wire format is unchanged.

| Field | Sketch | As built | Why |
| --- | --- | --- | --- |
| `Finding.UndoStep` | `string` | `UndoStep` | `UndoPlan []UndoStep` is assembled directly from these fields, so they must share a type. |
| `AIGateStatus` | `string` | `GateStatus` | The gate is derived in exactly one place, `Grade.Gate()`. A named type means a typo'd `"Pass"` cannot compile. |
| — | — | `DownMigrations []DownMigrationStatus` | `docs/RULES.md` §1 requires recording which of the three validation levels passed, and the certificate is the only output artifact. See §16.1. |

The domain package also carries enum algebra: `Valid()`, `Severity()`, `LockHazard.AtLeast()`,
`Grade.Rank()`, `Grade.Cap()`, `Grade.Gate()`, and `SortFindings()`. These are properties of the
types, not policy — **no scoring, parsing, or I/O lives in `domain`**, and it remains
stdlib-only.

**Zero values are never safe.** An unset `Reversibility` is invalid, an unset `LockHazard` sorts
above `EXCLUSIVE`, and an unset `Grade` ranks below `F` and gates `FAIL`. A finding nobody
classified must never read as harmless. `TestZeroValuesAreNeverSafe` enforces this.

## 9-11, 15. Classification and scoring — moved

**The rule tables, the scoring procedure, and the owner rulings now live in
[`docs/RULES.md`](docs/RULES.md).** They are the product specification and are read by
contributors who are not working through this file, so they were given a document of their own.

| Was | Now |
| --- | --- |
| §9 AUTHORITATIVE Classification — PostgreSQL | [`docs/RULES.md` §1](docs/RULES.md#1-postgresql--pg001-to-pg027) |
| §10 AUTHORITATIVE Classification — Kubernetes | [`docs/RULES.md` §2](docs/RULES.md#2-kubernetes--k8s001-to-k8s015) |
| §11 AUTHORITATIVE Scoring | [`docs/RULES.md` §3](docs/RULES.md#3-scoring) |
| §15 Owner rulings | [`docs/RULES.md` §4](docs/RULES.md#4-owner-rulings) |

Nothing about them changed in the move except the section numbers, and every code comment that
cited the old numbering was rewritten in the same commit. **Those tables remain authoritative:
do not infer, extend, or soften them, and do not invent a rule that is not written there.**

The section numbers below are unchanged, so §12, §13, §14, §16, and §17 still mean what every
existing reference to them means.

## 11b. Renderers and CLI — as built in S5

- **`pkg/certificate` is the public schema and the JSON renderer emits it**, not the internal
  type. Go forbids importing `internal/`, so without this package no external consumer could read
  a certificate at all. The internal model may be refactored freely; this schema may not.
- **Renderers never re-derive a grade or a gate status.** They print what the engine decided. A
  second definition of the gate would be a second chance to get it wrong in the permissive
  direction.
- **Markdown escaping distinguishes code spans from prose.** Findings carry text from the
  analyzed repository, which is attacker-controlled in any pull request from a fork. Outside a
  code span, `<` and `>` become entities. *Inside* one they must stay raw — SQL is full of `<`,
  `>`, `<=`, `<>`, and entity-escaping would render `&lt;=` literally to the reviewer. Safety
  inside the span comes from neutralizing backticks, pipes, and newlines so the span cannot be
  closed early. `TestMarkdownEscapesUntrustedContent` and `TestMarkdownPreservesSQLOperators`
  hold both halves.
- **SARIF maps UNKNOWN to `error`, not `warning`.** Downgrading it would let a change nobody
  understood pass a code-scanning gate.
- **SARIF reports `executionSuccessful: true` even on grade F.** A correctly detected destructive
  migration is the tool working; reporting otherwise makes code scanning treat it as a broken
  upload.
- **The SARIF tool version is pinned to the certificate schema version, not a build stamp.** A
  version that moved with every build would break byte-identical output.
- **CLI exit codes are the contract with CI:** `0` success, `1` gate not met, `2` run did not
  complete. Keeping "unsafe change" distinct from "broken tool" is what stops a broken tool from
  being ignored. The certificate is still written when the gate fails — it is the artifact the
  user asked for.
- **`cli.Execute` returns an exit code; only `main` calls `os.Exit`.** Streams are injected, so
  the whole command tree is tested in-process.
- **The `fs` provider is given an `Include` predicate by the caller**, built from
  `Engine.Supports`. Knowing which extensions matter is the analyzers' business, and deriving it
  from the registry means a new analyzer widens the net without a second list to update.
- **Two-tree comparison (`--before`) returns unchanged files as MODIFIED with identical sides**,
  matching `fakeProvider`, because K8S003 and K8S009 need context the change did not touch
  (§16.4).
- ~~**Resolving a changeset from a git ref is not implemented.**~~ **RESOLVED in S8** — see §11d.
  The fs provider still compares paths, not revisions; `--base` selects the git provider instead.

## 11c. GitHub App — as built in S6

**Security.** Nothing is parsed, logged, or acted on before the signature is verified.

- **Only `X-Hub-Signature-256` is honoured.** GitHub also sends the SHA-1 `X-Hub-Signature`;
  accepting it would let an attacker downgrade the check by omitting the stronger header. There
  is no negotiation. **Never add a SHA-1 fallback.**
- Comparison uses `hmac.Equal` — constant time. A byte-by-byte compare leaks how much of a forged
  signature was correct, which is enough to recover the rest one byte at a time.
- The body is size-capped (`http.MaxBytesReader`, 25 MiB) *before* it is read, because the body
  is authenticated only after being read in full — an unbounded read is an unauthenticated
  attacker's memory-exhaustion primitive.
- A server with **no** secret rejects everything rather than accepting everyone.
- Rejection responses are uniform: missing, malformed, and wrong signatures return the same body.
  Each distinction is a hint toward a working forgery. Status is 401 for missing, 403 for invalid.

**Noise reduction.** The app owns exactly one comment per pull request, found by the invisible
HTML marker `<!-- reversibility-engine:certificate -->` and updated in place. Every page of
comments is searched, because on a long-running PR the app's comment gets buried and giving up
early would post a duplicate. Matching on a marker rather than on body text means the
certificate's wording can change freely without the app losing its own comment.

**Fail-closed network.** Any failure fetching the changeset — rate limit, 5xx, a file over
2 MiB, more than 500 changed files — produces `engine.UnavailableCertificate` with
`domain.RuleProviderError`: grade F, gate FAIL, and the reason in `Blockers`. The certificate is
still **posted**. Silence is the one unacceptable outcome, because a pull request with no
certificate looks identical to one that was never analyzed. Grading whichever files did arrive
is never an option.

**Context files (§16.4, resolved).** The provider lists each directory a changed file lives in,
at the **base** commit, and fetches the supported siblings the diff did not touch — returning
them as MODIFIED with identical sides, matching the fake and filesystem providers. This is what
makes K8S003 and K8S009 work in production. Scope is bounded to touched directories; a rule whose
context lies outside that still sees nothing and must return UNKNOWN.

**Other decisions.**

- Analysis runs **after** the HTTP response (202 Accepted). GitHub abandons a delivery that takes
  more than about ten seconds. `WithSynchronousProcessing()` exists for tests, where a background
  goroutine would make every assertion a race.
- Shutdown is graceful for the same reason: an abrupt exit drops certificates for deliveries
  GitHub already considers sent and will not retry.
- Only `opened`, `reopened`, `synchronize`, and `ready_for_review` are analyzed. Other actions
  cannot change the diff, so re-running would burn API budget to reach the same answer.
- Unsubscribed events and ignored actions return **200**, so GitHub stops retrying.
- An authenticated but incomplete payload (no base or head SHA) is a **400**. Comparing the wrong
  commits would certify a change nobody made.
- App authentication (JWT → per-installation token) is hand-rolled against `crypto/rsa` and
  `encoding/json` rather than adding a JWT dependency: the claim set is three fields and the
  signature is one RSA operation. Both PKCS#1 and PKCS#8 keys are accepted, because GitHub has
  issued both. Tokens are cached per installation and refreshed 5 minutes before expiry.
- `GITHUB_WEBHOOK_SECRET` **and** credentials are both required to start. A server that
  authenticates correctly and then cannot post is worse than no gate — the pull request looks
  reviewed.

## 11d. Git ref resolution — as built in S8

`--base <ref>` (with `--head`, defaulting to `HEAD`) resolves the changeset from git rather than
from two directories. Path arguments become git pathspecs, scoping the comparison to a subtree.

- **git is shelled out to; no git library is linked.** git's own answer to what a ref, a merge
  base, and a rename are is the definition of those words here. A second implementation would be
  a second thing that can disagree with the pull request the developer is looking at.
- **Blobs are read from the object database (`git show <sha>:<path>`), never from the working
  tree.** A dirty checkout is invisible: the certificate describes the refs it names, which is
  what lets someone else reproduce it. `TestCheckResolvesAGitRange` leaves uncommitted edits in
  place and asserts they do not appear.
- **The comparison is three-dot (`base...head`), and the previous side is read at the merge
  base.** Reading old content at the base ref instead would pair each old file with a newer
  sibling and describe a transition nobody proposed.
- **Refs are resolved to SHAs once, up front.** A branch that moves mid-run cannot then produce a
  changeset assembled from two different commits.
- **An ambiguous ref is an error, not a preference.** git resolves a branch and a tag of the same
  name by precedence and warns on stderr; accepting that would certify a comparison the user did
  not ask for.
- **A rename is reported as a REMOVED plus an ADDED, never as `StatusRenamed`.** The Kubernetes
  rules compare whole objects, and K8S003/K8S009 need to see the removal to ask what still
  depends on it. A copy (`C`) yields only the new path, since the source is untouched.
- **An unrecognised diff status is an error.** `U` (unmerged) and `X` (a bug in git, by git's own
  documentation) cannot be classified, and guessing a side would hand an analyzer a
  half-populated file.
- **Unchanged siblings in touched directories are returned as MODIFIED with identical sides**,
  matching the fake, fs, and GitHub providers (§16.4). Without this the same pull request would
  grade differently from the CLI than from the app, and the more permissive answer is the one a
  developer would see first.
- **Failures name the fix.** Not a repository, unknown ref, ambiguous ref, and — the common CI
  case — a shallow clone missing the base commit, which says `fetch-depth: 0` in the message.
  All of them reach the caller as a failure to fetch the changeset, which is grade F and exit 2.

## 11e. GitHub Action and releases — as built in S9

The action is a **composite** action at the repo root, with its shell in `action/`. v1 was a
Docker container action; that tag still is one, and is frozen.

- **A released binary, verified, not a container and never `go install`.** A container pinned
  the action to Linux runners and paid an image pull per run; `go install` would need a Go
  toolchain and a C compiler on every consumer's runner, because of cgo. The action downloads
  the release asset for the runner's OS and architecture and **refuses to execute anything whose
  SHA-256 does not match the release's `checksums.txt`** — this binary decides whether changes
  merge, so an unverified one does not run. A missing checksum line is equally fatal: there is
  nothing to verify against.
- **`latest` is deliberately not cached.** An `actions/cache` key is immutable once written, so
  a key containing "latest" would pin the first binary it ever saw and keep serving it. Pinning
  a version is what earns the cache.
- **The action analyzes a git range (`--base`), not a staged pair of trees.** v1 reconstructed
  two directories by copying changed files, which passed `--diff-filter=d` and therefore **never
  showed the analyzer a deleted file** — K8S003, K8S006, and every other removal rule could not
  fire on a real pull request. S8's provider removed the need for any of it.
- **Deprecated input names are kept but never merged.** `min-grade` → `gate`, `include` →
  `path`. Setting a name and its replacement together is an **error**, not a precedence
  decision: resolving it silently would pick one during exactly the upgrade that introduced the
  mistake. A gate that breaks on upgrade gets deleted rather than fixed, which is why the old
  names still work at all.
- **`config` is an error, not a no-op.** The policy file arrives in S10. An input that was read
  by nothing would leave a user believing their waivers applied — a failure in the permissive
  direction, which is the one this product exists to prevent.
- **The verdict is read back from the JSON certificate, never re-derived in shell.** A second
  definition of the grade is a second chance to get it wrong in the permissive direction.
- **`jq` is checked for up front.** Without the check, a runner lacking it yields an empty grade
  read from nowhere — the one shape of failure that can look like a pass.

**Releases** (`.github/workflows/release.yml`) build each target on a runner of its own
architecture. This is forced, not chosen: `CGO_ENABLED=1` is mandatory, cross-compiling cgo
needs a toolchain per target, and the darwin targets need the macOS SDK, which cannot be put on
a Linux runner. **goreleaser's OSS edition cannot split a build across runners** — that is a Pro
feature — so the matrix does it directly and one job merges the per-target checksums. Every
target verifies its own binary still classifies a `DROP TABLE` before it is packaged: a build
that silently lost the parser would grade every migration A for lack of findings.

No `-ldflags` version stamping anywhere. A build stamp reaching rendered output would break the
byte-identical guarantee (§11b).

**The major tag moves itself.** Consumers write `@v2` and expect the newest v2.x; nothing in git
moves that tag, and forgetting to do it by hand fails silently — everyone stays on the previous
release and the fix they were told to upgrade for never arrives. A final job repoints it through
the API and then reads it back, because a tag that quietly did not move is the same failure.
Prereleases are excluded: `v2.1.0-rc.1` must never become what `@v2` means.

`.github/workflows/action-selftest.yml` runs the action against this repository's own fixtures
and asserts the grade, the gate status, the finding count, **and that a grade F actually fails
the job**. An action that reports F and exits zero is not a gate.

## 11f. Policy file — as built in S10

`.reversibility.yml`, discovered by walking up from the analysis path and stopping at the
directory holding `.git`. `--config` names one explicitly; `--no-config` discards one.

**The one thing a policy may never do is improve the measurement.** §14 says configuration must
not improve a grade, and S10 says a waiver downgrades a finding to advisory. Both are honoured by
splitting the number in two, which is the owner's ruling:

| Field | Means | Moved by a policy? |
| --- | --- | --- |
| `Grade` | what the evidence says about the change | **never** |
| `EffectiveGrade` | `Grade` with waived findings set aside | yes — this is what CI compares |
| `AIGateStatus` | PASS iff `Grade` is A | **never** |

So a waiver unblocks a human's pipeline and **can never authorise an autonomous agent to merge**.
`EffectiveGrade` equals `Grade` whenever no policy applied, so a consumer always has exactly one
field to gate on and never has to know whether a policy existed.

- **A waiver requires `reason` and `expires`, and both are errors rather than warnings.** A
  warning in a CI log is not read, and the waiver would take effect anyway — which is the
  unexplained suppression this design exists to prevent. Exit code 2: a run that could not
  resolve its own configuration does not know what it was meant to enforce.
- **`expires` is a date, never a duration, capped at 180 days from the parse date.** "90d" is
  relative to a moment nobody records, so it renews on every read and never expires at all.
- **An expired waiver is inert.** The finding returns with no edit and no announcement.
- **A waiver is reported, never deleted.** Waived findings appear in the certificate's `Waived`
  section with their reason and expiry, in Markdown, JSON, and as SARIF `suppressions` — SARIF
  keeps the original level, because a waiver records acceptance, it does not reduce severity.
- **A waiver may not cover UNKNOWN, an unreadable verdict, or an analyzer error.** Accepting a
  risk nobody has characterised is not a decision anyone is in a position to make.
- **The undo plan still covers waived findings.** A waiver accepts a risk; it does not invent an
  undo. A plan that omitted the waived half would claim a completeness it does not have.
- **Overrides tighten only,** and they move `Grade` as well — tightening is the one direction
  configuration is allowed to push the measurement. An override to REVERSIBLE is refused at parse
  time; one that would weaken an actual finding is refused when applied.
- **Order is fixed: overrides tighten, then waivers match the result.** Waiving first would let a
  waiver written for a mild classification swallow a finding somebody separately marked severe.
- **`today` is injected** (`engine.WithToday`), defaulting to the system date. It is the only
  value in the engine not derived from the input, so it is injected rather than read where it
  would be untestable.
- **The policy is hashed into `InputDigest`, and exposed as `PolicyDigest`.** The digest covers
  the *resolved* policy, not the file's bytes, so reformatting or editing a comment does not
  change a certificate — and deliberately not which waivers are currently live, since a digest
  that changed overnight on its own would stop being evidence. It is mixed in **only when a
  policy exists**, so every digest ever produced without one is unchanged.
- **The engine does not analyze its own configuration.** `.reversibility.yml` is YAML, so the
  Kubernetes analyzer claimed it and reported K8S014/UNKNOWN — adopting a policy graded your
  repository F because of the file you adopted it with. `policy.IsPolicyFile` excludes it.

**Two bugs S10's tests found in earlier sessions' code**, both fixed here:

- The `fs` provider tested its `Include` predicate against the **absolute** path on disk, so
  `ignore: ["legacy/**"]` matched nothing. Extension checks never noticed because they only look
  at the suffix. The predicate now sees the path as it will appear in the changeset, which is
  what the git and GitHub providers already did.
- `.reversibility.yml` grading itself, above.

**A waiver's `path` matches `Finding.File` exactly as rendered**, which is repository-relative
under `--base` and the action, and relative to the named directory under `revctl check ./dir`. A
pattern matching nothing is inert rather than approximately applied: over-matching a waiver is
the one direction this must never fail in. Pinned by `TestWaiverPathMatchesTheFindingAsReported`.

Glob syntax in `ignore` and `waivers[].path` is `**` for whole segments, `*`/`?`/`[...]` within
one, delegating to `path.Match` per segment. **This is deliberately not git pathspec syntax** —
pathspecs are what the git provider hands to git, these are what the policy matches, and two
syntaxes under one name would be worse than two names.

## 12. Engineering standards

- Go 1.22+, `log/slog` for logging, stdlib `net/http` — **no web framework**.
- Explicit error returns. **No panic in library code.**
- A **single** `recover()` boundary in the engine orchestrator, converting a panic into grade
  **F** with `RuleID: "ENGINE_PANIC"` — never into a pass.
- All errors wrapped with `%w` and context. Sentinel errors live in `domain`.
- Every exported symbol documented. Comments explain **why**, never what.
- `context.Context` as the first parameter on anything that could block.
- Table-driven tests. Fixtures live in `testdata/`, **not** in Go string literals.
- `golangci-lint` clean; `go vet` clean; race detector on in CI.

## 13. Testing rules

- **Before implementing an analyzer, write its fixtures and failing tests first.**
- **One fixture pair per rule ID** — all 27 Postgres rules and all 14 Kubernetes rules.
  **A rule with no fixture does not exist.**
- Golden-file tests for the Markdown and JSON renderers.
  As built: golden files exist for **all three** renderers across 8 scenarios spanning every
  grade plus the not-applicable case, in `testdata/fixtures/golden/`. Regenerate deliberately
  with `go test ./internal/render -update` and review the diff — a test that silently rewrote
  its own expectations would turn every rendering regression into a pass.
- Fuzz test on the SQL analyzer: it must never panic and **must never return `REVERSIBLE` for
  malformed input**.
- Property test: adding any statement to a changeset can only **lower or hold** the grade,
  never raise it.
- Minimum **85% coverage** on `internal/analyzer` and `internal/engine`.

### As built in S7 — hardening

**`internal/analyzer/postgres/parser/complexity.go` is load-bearing. Do not remove it.**

Fuzzing found that a chain of a few thousand operators — `SELECT 1+1+1+…`, or `NOT NOT NOT …` —
overflows the C parser's stack. That is a **hard process crash inside cgo**, which `recover()`
cannot catch, and on `revsrv` it made a ~10 KB pull request a remote denial of service. The guard
refuses structurally extreme input *before* it reaches cgo (100 nesting levels, 500 operators per
statement) and reports PG027/UNKNOWN, so it grades F. Word operators count as much as symbolic
ones: counting only punctuation left `NOT` chains able to crash the process. See ADR/0001.

**A verdict the domain does not recognise grades F.** An analyzer that returns an empty or
misspelled `Reversibility` used to merely fail the "all REVERSIBLE" test and cap at B. It is now
a blocker, because a classification nobody can read is exactly what UNKNOWN is for.

Fuzz targets, all runnable with `make fuzz`:

| Target | Package | Property |
| --- | --- | --- |
| `FuzzAnalyze` | `analyzer/postgres` | never panics; unparseable SQL is never anything but PG027/UNKNOWN |
| `FuzzValidateDownMigrations` | `analyzer/postgres` | the three levels stay ordered and always explain a failure |
| `FuzzParse` | `analyzer/postgres/parser` | the cgo seam survives arbitrary bytes; no statements returned beside an error |
| `FuzzAnalyze` / `FuzzAnalyzeAdded` | `analyzer/kubernetes` | never panics; lock hazard is always NONE |
| `FuzzCertify` | `engine` | **an IRREVERSIBLE or UNKNOWN finding can never sit in a passing certificate** |
| `FuzzCertifyIsDeterministic` | `engine` | identical input, identical bytes, whatever the input |
| `FuzzWebhookRejection` | `delivery/github` | nothing reaches the processor without a signature from the real secret |
| `FuzzAuthenticPayload` | `delivery/github` | an authentic but malformed body never 5xxs or dispatches a half-built job |

**Panic-boundary sweep** (`internal/engine/panic_test.go`): every panic point in the Analyzer
contract (`Analyze`, `Supports`, `Name`, `ValidateDownMigrations`) crossed with every kind of
panic value (string, error, struct, int, nil pointer, slice, `panic(nil)`), plus seven runtime
faults. All end in grade F with `ENGINE_PANIC`. Findings gathered before a panic are discarded
deliberately: a partial conclusion from a broken run is not evidence.

**Verdict snapshot** (`testdata/fixtures/golden/verdicts.txt`): the grade, gate, counts, and
input digest for all 46 fixtures in one file. Regenerate with `go test ./internal/engine -update`
and **review the diff** — a scoring change that quietly moves fixtures between grades shows up
here as changed lines. The digest column catches a fixture edited by accident even when its grade
does not move.

## 14. Do Not

- Do not invent classification rules or scoring weights not written above.
- Do not use regex for SQL parsing.
- Do not write placeholder or simulated data-fetching code.
- Do not add dependencies beyond the table in §6.
- Do not build anything listed under "Out of scope" (§3).
- Do not let any code path turn an error, a panic, or an unknown into a passing grade.

## 16. Open questions — resolve with the owner, do not guess

A future session must **ask**, never decide alone. Items marked ASSUMED are encoded in the S1
fixtures so they are cheap to reverse — correcting one is a data edit, not a code change.

1. ~~**Down-migration reporting channel.**~~ **RESOLVED in S2.** `Analyze` returns only findings,
   so down-migration status travels through a separate exported function in the same package:

   ```go
   postgres.ValidateDownMigrations(ctx, p parser.SQLParser, files []domain.ChangedFile)
       ([]domain.DownMigrationStatus, error)
   ```

   It is stateless, so the analyzer holds no per-run state and stays safe to share. **S4's
   orchestrator must call it alongside `Analyze`** and put the result on the certificate — a
   scorer that only calls `Analyze` will silently lose the `down.sql` cap.
2. **Rule precedence when several rules match one statement.** ASSUMED in S1 fixtures: one
   finding per statement; `PG005` (CASCADE) overrides any base rule it overlays; otherwise the
   lowest rule number wins. `DROP TABLE x CASCADE` is therefore **one** PG005 finding, not
   PG001 + PG005.
3. **PG006 vs PG007 need the prior column type,** which a migration file does not contain.
   ASSUMED in S1 fixtures: the analyzer tracks types declared by `CREATE TABLE` / earlier
   `ALTER` statements *within the same changeset*. A type it cannot resolve is PG027/UNKNOWN.
   Confirm before S2, since a schema-baseline input would be a different design.
4. **`ChangedFiles` must return unchanged context files, not only changed ones.** K8S003 and
   K8S009 cannot be decided from the changed file alone: K8S003 needs the StorageClass that
   nobody edited, and K8S009 needs the workload that still references the deleted ConfigMap.
   A GitHub pull-request diff does not contain either. `fakeProvider` supplies them (unchanged
   files come back as MODIFIED with identical sides), so the fixtures are satisfiable — but the
   `fs` and `github` providers cannot do the same without reading the whole tree. Decide before
   S6 whether `ChangedFiles` returns context files or `FileProvider` grows a second method.
   Until then, **a rule that needs context it cannot get must return UNKNOWN, not REVERSIBLE.**
5. ~~**No Kubernetes rule covers a container image change.**~~ **RESOLVED: K8S015 is now in the
   `docs/RULES.md` §2 table**, constrained by the owner to digests only. A tag, however version-like, cannot be
   proven immutable by static analysis and stays K8S008/COSTLY.
6. **UNKNOWN findings also replace the undo plan.** `docs/RULES.md` §3 says the plan is replaced by a statement
   that no complete undo exists "if any finding is IRREVERSIBLE". S4 applies the same to UNKNOWN.

   The reason is §2: an UNKNOWN finding is a change nobody understood, so a plan that lists steps
   for everything *else* claims a completeness it does not have — a confident-looking script
   printed beside an unclassified change is the wrong-safe-verdict failure this product exists to
   prevent. It affects presentation only; the grade was already F either way.

   Confirm or reject. Rejecting is a one-line change in `unreversibleFindings`.

## 17. Fixture conventions

47 fixture directories under `testdata/fixtures/`: 27 Postgres rules, 15 Kubernetes rules,
4 `DOWN*` fixtures for the three down-migration validation levels plus the directory form, and
1 `ENCODING*` fixture pinning the decoding seam.

**Two fixtures may not claim the same rule** — `TestEveryRuleHasAFixture` rejects it, so a
fixture proving something *about* an existing rule takes a non-table rule ID, as `DOWN*` and
`ENCODING*` both do. A non-table rule ID additionally has to assert `downMigrations`, which is
what stops that escape hatch from becoming a way to assert nothing.

`ENCODING001_utf8_bom` carries a UTF-8 BOM on both its up and down migrations and asserts the
PG002 the statement deserves. Before the strip in `parser.PgQuery.Parse` it reported PG027
"could not be parsed": still F, so nothing unsafe merged, but the certificate named no rule and
described nothing the migration did. Fail-closed guarantees the grade, not that the grade is
explicable — and on Windows-authored migrations the inexplicable one was the common case.
Only a *leading* BOM is stripped; an interior one is corruption and still fails.

Two shapes, both resolved by `provider.Fake`:

```
<fixture>/migrations/...        every file is ADDED — what a migration PR looks like
<fixture>/old/... + new/...     the two trees are diffed by path; the old//new/ prefix is
                                stripped, so a finding's File is "deployment.yaml"
```

Every fixture carries `expected.json`:

```json
{
  "rule": "PG001",
  "note": "why this fixture is shaped the way it is",
  "findings": [
    {"ruleId":"PG001","file":"...","line":1,
     "reversibility":"IRREVERSIBLE","lockHazard":"EXCLUSIVE","wantUndoStep":false}
  ],
  "downMigrations": [{"migration":"0001_x","exists":true,"parses":true,"symmetric":true}]
}
```

Rules for writing and changing fixtures:

- **Expectations pin classification only** — rule ID, file, line, reversibility, lock hazard,
  and whether an undo step exists. Rationale and undo-step *wording* are never pinned, so
  improving the prose a human reads cannot break the suite.
- **`wantUndoStep` is presence, not text.** An IRREVERSIBLE finding must carry no undo step;
  offering one would be a lie, and `TestFixtureAssertionsAreWellFormed` rejects it.
- **Every Kubernetes image is digest-pinned** except in `K8S008_image_not_pinned`. Otherwise
  K8S008 fires in every fixture and the expectations stop meaning anything.
- **Line 0 means file-level**, which is what all Kubernetes findings currently use. Whether S3
  can attribute a YAML line number is an implementation decision, not a spec one.
- Unknown JSON fields are rejected on load, so `lockHazzard` fails loudly instead of silently
  asserting the zero value.

`internal/fixture` is a **layout addition** beyond §5, made because the analyzer tests, the
engine tests (S4), and the renderer golden tests (S5) all read this format. Four copies of the
loader would drift.
