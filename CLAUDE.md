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
internal/provider/                   FileProvider interface: fs, github, fake
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
| `sigs.k8s.io/yaml` (JSON-typed decode) + `gopkg.in/yaml.v3` (stream decoder) | `internal/analyzer/kubernetes` | S3 |
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

`FileProvider` is implemented **three** times: `fsProvider` (local dir/diff), `githubProvider`,
`fakeProvider` (reads `testdata/`). Never write "simulated" or placeholder fetch code — use
`fakeProvider`.

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
    SchemaVersion string    // "1.0.0" — bump on any breaking field change
    Grade         Grade
    AIGateStatus  string    // PASS | FAIL
    Applicable    bool
    InputDigest   string    // SHA256 over sorted (path, content)
    Findings      []Finding // sorted by File, then Line, then RuleID
    UndoPlan      []UndoStep
    Blockers      []string  // human-readable reasons for F
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
| — | — | `DownMigrations []DownMigrationStatus` | §9 requires recording which of the three validation levels passed, and the certificate is the only output artifact. See §16.1. |

The domain package also carries enum algebra: `Valid()`, `Severity()`, `LockHazard.AtLeast()`,
`Grade.Rank()`, `Grade.Cap()`, `Grade.Gate()`, and `SortFindings()`. These are properties of the
types, not policy — **no scoring, parsing, or I/O lives in `domain`**, and it remains
stdlib-only.

**Zero values are never safe.** An unset `Reversibility` is invalid, an unset `LockHazard` sorts
above `EXCLUSIVE`, and an unset `Grade` ranks below `F` and gates `FAIL`. A finding nobody
classified must never read as harmless. `TestZeroValuesAreNeverSafe` enforces this.

## 9. AUTHORITATIVE Classification — PostgreSQL

**Do not infer, extend, or soften this table. Anything not listed is `UNKNOWN`.**

| Rule | Statement | Reversibility | LockHazard |
| --- | --- | --- | --- |
| PG001 | `DROP TABLE` | IRREVERSIBLE | EXCLUSIVE |
| PG002 | `DROP COLUMN` | IRREVERSIBLE | EXCLUSIVE |
| PG003 | `TRUNCATE` | IRREVERSIBLE | EXCLUSIVE |
| PG004 | `DROP SCHEMA` / `DROP DATABASE` | IRREVERSIBLE | EXCLUSIVE |
| PG005 | any statement containing `CASCADE` | IRREVERSIBLE | EXCLUSIVE |
| PG006 | `ALTER COLUMN TYPE` narrowing (bigint→int, text→varchar(n), numeric precision reduced, timestamptz→date) | IRREVERSIBLE | TABLE_REWRITE |
| PG007 | `ALTER COLUMN TYPE` widening | COSTLY | TABLE_REWRITE |
| PG008 | `DELETE` / `UPDATE` without `WHERE` | IRREVERSIBLE | EXCLUSIVE |
| PG009 | `DELETE` / `UPDATE` with `WHERE` | IRREVERSIBLE | SHORT |
| PG010 | `ALTER SEQUENCE ... RESTART` | IRREVERSIBLE | SHORT |
| PG011 | `DROP TYPE` / `DROP SEQUENCE` / `DROP EXTENSION` | IRREVERSIBLE | EXCLUSIVE |
| PG012 | `RENAME TABLE` / `RENAME COLUMN` | COSTLY | SHORT |
| PG013 | `DROP CONSTRAINT` (any) | COSTLY | SHORT |
| PG014 | `DROP INDEX` (non-concurrent) | COSTLY | EXCLUSIVE |
| PG015 | `DROP INDEX CONCURRENTLY` | COSTLY | NONE |
| PG016 | `DROP VIEW` / `DROP FUNCTION` / `DROP TRIGGER` | COSTLY | SHORT |
| PG017 | `ALTER COLUMN SET NOT NULL` | COSTLY | FULL_SCAN |
| PG018 | `ADD COLUMN NOT NULL` without `DEFAULT` | COSTLY | EXCLUSIVE |
| PG019 | `ADD COLUMN` with volatile `DEFAULT` | REVERSIBLE | TABLE_REWRITE |
| PG020 | `ADD COLUMN` nullable / constant `DEFAULT` | REVERSIBLE | NONE |
| PG021 | `ADD FOREIGN KEY` / `ADD CHECK` without `NOT VALID` | REVERSIBLE | FULL_SCAN |
| PG022 | `ADD ... NOT VALID` | REVERSIBLE | SHORT |
| PG023 | `CREATE INDEX` non-concurrent | REVERSIBLE | EXCLUSIVE |
| PG024 | `CREATE INDEX CONCURRENTLY` | REVERSIBLE | NONE |
| PG025 | `CREATE TABLE` / `CREATE VIEW` / `CREATE TYPE` | REVERSIBLE | NONE |
| PG026 | `ALTER COLUMN DROP NOT NULL` / `SET DEFAULT` / `DROP DEFAULT` | REVERSIBLE | SHORT |
| PG027 | unparsed or unrecognized statement | UNKNOWN | EXCLUSIVE |

**Rationale notes that must be embedded in output:**

- **PG012** (rename) is COSTLY because it breaks the previous application version — rollback of
  code fails while the schema is renamed.
- **PG013** is COSTLY because rows violating the dropped constraint may be inserted before
  rollback, making re-adding it impossible.

### Parser directive

Use `github.com/pganalyze/pg_query_go/v5` — a **real AST**, not regex. It is **cgo**. Isolate it
behind an internal `SQLParser` interface in `internal/analyzer/postgres/parser` so it can be
swapped. CI must build with `CGO_ENABLED=1`.

**musl / Alpine constraint.** `pg_query_go` vendors the PostgreSQL C parser and links against
libc. It is built and tested against glibc. On Alpine/musl the build needs `apk add build-base`,
and even then the parser's recursive descent can exhaust musl's default 128 KiB thread stack
(glibc allows 8 MiB), so deeply nested SQL may fault on musl where it succeeds on glibc.
**Ship the server image on a glibc base** (`debian-slim` or `distroless/base`), not Alpine. If an
Alpine build ever becomes a requirement, raise the thread stack explicitly and add a
deep-nesting fixture to CI. See `ADR/0001-parser-choice.md`.

**Never fall back to regex.** If the parser is unavailable, that is an analyzer error → **F**.

### Down-migration validation

For each `NNN_name.up.sql` require `NNN_name.down.sql`. Also accept the directory form
`migrations/NNN/up.sql` + `migrations/NNN/down.sql`.

Validate three levels and **record which passed**:

1. File exists.
2. File is non-empty and parses.
3. Every `CREATE X` in up has a matching `DROP X` in down, and vice versa.

**Level 3 is a heuristic — mark it advisory. It must never alone produce grade F.**

### As built in S2

- **`internal/analyzer/postgres/parser` is the only package that imports `pg_query`.** It lifts
  the parse tree into a neutral `parser.Statement`; no parser type crosses the `SQLParser`
  interface. Classification in `rules.go` therefore runs with no cgo in the path, and is tested
  against a stub parser as well as the real one.
- **Only up migrations are classified.** A down migration describes the rollback, not the change
  being assessed — classifying it would report the undo of a safe change as destructive. Down
  files are read solely by `ValidateDownMigrations`. A `.sql` file that is not recognisably a
  down migration is treated as an up migration, which is the safe direction.
- **A multi-command `ALTER TABLE` is flattened into one finding per command.** Collapsing them
  would hide the destructive half of `ALTER TABLE t ADD COLUMN a int, DROP COLUMN b`. This does
  not contradict §16.2: that rule is about overlapping rules on one *command*.
- **A file that fails to parse yields one PG027 finding for that file, not an analyzer error.**
  One malformed migration must not erase the findings of the others; the certificate should show
  everything that is wrong at once. A parse failure still grades F via UNKNOWN.
- **Removed files are not classified.** Statements in a deleted migration are not going to run.
- **Undo steps are real commands.** Where the engine cannot reconstruct part of one — the body of
  a dropped view, the definition of a dropped constraint — it emits the command with the missing
  part marked by the SQL comment `/* original definition unavailable: ... */`, so the result
  stays a statement an operator can paste, complete, and run. Never prose.
- **`Finding.Statement` excludes the trailing semicolon**, because that is the extent the parser
  reports, and it is whitespace-normalized so reformatting a migration cannot change a digest.

## 10. AUTHORITATIVE Classification — Kubernetes

Compare old vs new manifest by `apiVersion` / `kind` / `namespace` / `name`.

| Rule | Change | Reversibility |
| --- | --- | --- |
| K8S001 | `StatefulSet.spec.volumeClaimTemplates` modified | IRREVERSIBLE |
| K8S002 | `spec.selector` modified on Deployment/StatefulSet/DaemonSet | IRREVERSIBLE |
| K8S003 | PVC removed while its StorageClass `reclaimPolicy` is `Delete` or unknown | IRREVERSIBLE |
| K8S004 | PVC storage request decreased | IRREVERSIBLE |
| K8S005 | `storageClassName` changed on a PVC | IRREVERSIBLE |
| K8S006 | Namespace or CRD removed | IRREVERSIBLE |
| K8S007 | `Service.spec.clusterIP` or `.type` changed | COSTLY |
| K8S008 | container image not pinned by digest or immutable tag (`latest`, floating tags) | COSTLY |
| K8S009 | ConfigMap/Secret removed while still referenced by a workload | COSTLY |
| K8S010 | `Deployment.strategy` changed to `Recreate` | COSTLY |
| K8S011 | probe (readiness/liveness) removed | COSTLY |
| K8S012 | replicas / resources / env / labels changed | REVERSIBLE |
| K8S013 | new workload added | REVERSIBLE |
| K8S014 | manifest fails to parse or kind unrecognized | UNKNOWN |
| K8S015 | container image changed, new image explicitly pinned by a cryptographic digest (`@sha256:...`) | REVERSIBLE |

**K8S008 exists because a rollback target that cannot be identified is not a rollback target.**

**K8S008 vs K8S015 — the digest rule (owner ruling).** Only a cryptographic digest pins an image.
Static analysis cannot prove that a tag — semver included — still points at the same bytes on the
remote registry, because tags are mutable by design. **Any tag without a digest is K8S008/COSTLY.**
K8S015 applies only when the new image carries an explicit `@sha256:` (or `@sha512:`) digest.

### As built in S3

- **Document boundaries come from a real YAML stream decoder**, never from splitting bytes on
  `---`. `gopkg.in/yaml.v3`'s `Decoder` yields one document node at a time; each is then decoded
  through `sigs.k8s.io/yaml` for JSON-compatible types. **Do not replace this with string
  splitting.** Byte splitting cannot tell whether a separator-looking line sits inside a scalar,
  and it does not understand the `...` end-of-document marker at all — both silently change which
  objects the engine sees. Used alone, `sigs.k8s.io/yaml` decodes only the *first* document of a
  stream and returns a **nil error**, which would hide every object after the first in any
  `helm template` output. `TestParseManifestDocumentBoundaries` guards both failure modes.
- **Objects are decoded into `map[string]any`, not typed structs.** Typed structs would need the
  full `k8s.io/api` dependency and would silently drop fields the vendored version does not know,
  which is fatal when the question is "did anything change".
- **A file whose content is byte-identical on both sides is context, not change.** Its objects are
  indexed so K8S003 can find a StorageClass and K8S009 can find a referencing workload, but it
  generates no findings. Without this, every run would indict the whole cluster.
- **A changed object matching no rule yields K8S014/UNKNOWN** — the Kubernetes analogue of PG027.
  Silence about a change the engine does not understand is indistinguishable from a safe change,
  and the product rests on those two never being confused. See the consequence in §16.5.
- **Only an explicit `reclaimPolicy: Retain` prevents K8S003.** Absent, empty, or unresolvable is
  treated exactly like `Delete`, as §10 requires.
- **Quantities are compared numerically with correct scales.** `1Gi` (2^30) is not `1G` (10^9); a
  string comparison would call that shrink a growth. A quantity that cannot be parsed is
  K8S014/UNKNOWN, never assumed unchanged.
- **K8S008 treats a tag as pinned only if it contains a digit and no floating word.** A digest
  always pins. No tag at all means `:latest`. A registry port (`registry:5000/app`) is not a tag.
- **K8S012 emits one finding per changed *category*** (replicas, resources, env, labels), not per
  changed leaf, so adjusting cpu and memory together is one decision, not two.
- **All Kubernetes findings carry `LockHazard: NONE` and `Line: 0`.** A structural diff has no
  single line to blame, and inventing one sends readers to the wrong place.

## 11. AUTHORITATIVE Scoring

```
Any IRREVERSIBLE  -> F
Any UNKNOWN       -> F        (fail-closed, no exceptions)
Any analyzer error -> F       (never degrade to a passing grade)

Otherwise:
  missing or unparseable down.sql            -> cap at C
  >= 3 COSTLY findings                       -> C
  1-2 COSTLY findings                        -> B
  LockHazard >= TABLE_REWRITE present        -> cap at B
  all REVERSIBLE, lock <= SHORT, down.sql ok -> A
```

```
AIGateStatus = PASS  <=>  Grade == A
AIGateStatus = FAIL   otherwise
```

Empty changeset with zero relevant files → grade **A**, `Applicable: false`, gate **PASS**.

### UndoPlan

Generated **only** from the `UndoStep` fields of findings, in **reverse order of application**.

If any finding is `IRREVERSIBLE`, the plan is **replaced** by an explicit statement that no
complete undo exists, listing what cannot be undone.

### Determinism

**Hard requirement.** Identical input must produce a **byte-identical** certificate.

- No timestamps. No UUIDs. No hostnames.
- No map-iteration order anywhere inside the certificate.
- Sort everything explicitly.
- A test must run the engine **100×** over a fixture and assert identical SHA256 output.

### As built in S4

- **`Engine.Certify` returns `(certificate, error)` and the certificate is ALWAYS valid**, error
  or not. On any failure it is a fully populated grade F with the reason in `Blockers`. The error
  exists so operators can tell a broken toolchain from a dangerous migration — never so a caller
  can treat the certificate as missing.
- **The single `recover()` boundary lives in `Certify`.** A panic discards whatever the run had
  concluded and produces the `ENGINE_PANIC` certificate: grade F, UNKNOWN, no undo step. A
  partial conclusion from a broken run is not evidence.
- **Grade assembly is: assign, then cap, worst wins** (§15.1). Assignment is `>=3 COSTLY → C`,
  `1–2 COSTLY → B`, else A. Caps are applied unconditionally so order cannot matter.
- **The A row is read as a set of necessary conditions.** Failing any of them
  (`all REVERSIBLE`, `lock <= SHORT`, `down.sql ok`) caps the grade at B — B being the highest
  grade below A, the least punitive reading consistent with the table. This is what decides
  `FULL_SCAN`, which fails "lock <= SHORT" but does not reach the `TABLE_REWRITE` cap.
- **Down-migration status travels through the optional `analyzer.DownMigrationValidator`
  interface**, type-asserted by the orchestrator. The engine never imports an analyzer package;
  the delivery layer wires them (§16.1 resolved).
- **`Blockers` are populated only for grade F**, per §8. Findings explain B and C.
- **`InputDigest` hashes length-prefixed fields over both sides of every change** — path,
  previous path, status, previous content, current content — sorted by path. Length prefixing
  stops `"ab"+"c"` from colliding with `"a"+"bc"`; hashing the previous side keeps two changesets
  that reach the same final state from different starting points distinguishable.
- **Every certificate slice is normalized to empty, never nil.** `encoding/json` renders nil as
  `null` and empty as `[]`, so a nil would make two certificates of identical meaning serialize
  differently — determinism has to survive the renderers.

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
- **Resolving a changeset from a git ref is not implemented.** The fs provider compares paths,
  not revisions. The README says so plainly rather than advertising a flag that does not exist.

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

## 15. Owner rulings — authoritative, same weight as the tables above

Resolved by the owner. Treat these as spec.

1. **A cap overrides an assignment.** Caps are not tie-breakers, they are ceilings. Zero COSTLY
   findings, everything REVERSIBLE, but a missing `down.sql` → final grade **C**. Formally: a
   grade is assigned, then every active cap is applied, and the worst result wins.
2. **Kubernetes findings never hold database locks.** Their `LockHazard` is strictly `NONE`.
   Never any other value.
3. **`type ChangeRef string`** — a commit SHA or PR ref.
   **`type UndoStep string`** — the exact command to run (an SQL statement or a `kubectl`
   invocation), not prose.

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
   §10 table**, constrained by the owner to digests only. A tag, however version-like, cannot be
   proven immutable by static analysis and stays K8S008/COSTLY.
6. **UNKNOWN findings also replace the undo plan.** §11 says the plan is replaced by a statement
   that no complete undo exists "if any finding is IRREVERSIBLE". S4 applies the same to UNKNOWN.

   The reason is §2: an UNKNOWN finding is a change nobody understood, so a plan that lists steps
   for everything *else* claims a completeness it does not have — a confident-looking script
   printed beside an unclassified change is the wrong-safe-verdict failure this product exists to
   prevent. It affects presentation only; the grade was already F either way.

   Confirm or reject. Rejecting is a one-line change in `unreversibleFindings`.

## 17. Fixture conventions

45 fixture directories under `testdata/fixtures/`: 27 Postgres rules, 14 Kubernetes rules, and
4 `DOWN*` fixtures for the three down-migration validation levels plus the directory form.

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
