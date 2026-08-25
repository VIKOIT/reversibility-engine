# Rules Specification

**This document is the specification. The code is written to match it, not the other way
around.** Where an implementation disagrees with a table below, the implementation is the bug.

Two classification tables and one scoring procedure decide every grade the Reversibility Engine
emits:

| | |
| --- | --- |
| [§1 PostgreSQL](#1-postgresql--pg001-to-pg027) | 27 rules over a real PostgreSQL AST |
| [§2 Kubernetes](#2-kubernetes--k8s001-to-k8s015) | 15 rules over a structural manifest diff |
| [§3 Scoring](#3-scoring) | how findings become a grade of A, B, C, or F |
| [§4 Owner rulings](#4-owner-rulings) | decisions that resolve ambiguities in the above |

## The rule every other rule defers to

**Unknown means unsafe.** An error, a panic, an unparseable file, or a construct no rule
describes must never become a passing grade. Every such path terminates in **F**. There is no
"probably fine": a verdict is `REVERSIBLE`, `COSTLY`, `IRREVERSIBLE`, `UNKNOWN`, or `WILL_FAIL`,
and the last three all fail.

### The verdicts

| Verdict | Means | Severity |
| --- | --- | --- |
| `REVERSIBLE` | The change can be undone with no data loss. | 0 |
| `COSTLY` | It can be undone, but the undo is expensive, slow, or only correct within a window. | 1 |
| `UNKNOWN` | The engine could not determine the verdict. Treated as unsafe, never as safe. | 2 |
| `IRREVERSIBLE` | **You cannot undo this.** Undoing it cannot restore the prior state. | 3 |
| `WILL_FAIL` | **This will not even apply.** Production state has been checked and the statement is certain to abort. | 4 |

`WILL_FAIL` and `IRREVERSIBLE` are different failures and are reported as different failures.
One says the change cannot be reversed; the other says it will never happen, so there is nothing
to reverse and the fix belongs in the migration rather than in the rollback plan. A reader who
confuses them fixes the wrong thing.

`WILL_FAIL` is the only verdict that requires evidence beyond the source. It is never reached
from a guess: it exists because a production snapshot proved the statement cannot succeed. With
no snapshot, no rule in this document produces it.

## Changing a rule

1. **A rule with no fixture does not exist.** Every rule ID has a fixture directory under
   `testdata/fixtures/`, and `internal/fixture` fails the build if one is missing.
2. Write or amend the fixture first, then the code.
3. Regenerate the verdict snapshot with `go test ./internal/engine -update` and **review the
   diff** — `testdata/fixtures/golden/verdicts.txt` shows what every fixture now grades, so a
   change that quietly moves fixtures between grades is visible in the pull request.
4. Disagreement about a classification is the most valuable contribution there is. Open an issue
   arguing the case; the tables are the product.

Process, layout, and engineering standards live in [`CLAUDE.md`](../CLAUDE.md). Open questions
about rules that have not been settled are in [`CLAUDE.md`](../CLAUDE.md) §16 — do not resolve
one by guessing.

---

## 1. PostgreSQL — PG001 to PG027

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

**PG017 with a production snapshot.** When `pg_stats.null_frac > 0` for the column being
constrained, `SET NOT NULL` validates every existing row, finds a violation, and aborts —
rolling the transaction back. That is a certainty rather than a risk, so the verdict becomes
`WILL_FAIL` and the grade becomes **F**. Table size stops mattering at that point and no duration
band is computed: the statement never holds a lock for any length of time, and printing a
duration beside "this will not run" would be noise dressed as precision. With `null_frac == 0`,
or with no snapshot, PG017 is exactly what the table above says.

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

## 2. Kubernetes — K8S001 to K8S015

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

## 3. Scoring

```
Any IRREVERSIBLE  -> F
Any UNKNOWN       -> F        (fail-closed, no exceptions)
Any WILL_FAIL     -> F        (the migration cannot apply)
Any analyzer error -> F       (never degrade to a passing grade)

Otherwise:
  missing or unparseable down.sql            -> cap at C
  >= 3 COSTLY findings                       -> C
  1-2 COSTLY findings                        -> B
  LockHazard >= TABLE_REWRITE present        -> cap at B
  all REVERSIBLE, lock <= SHORT, down.sql ok -> A

With a production snapshot, additionally:
  LockDurationBand == DISRUPTIVE             -> cap at B
  LockDurationBand == OUTAGE                 -> cap at C
```

### Lock duration bands

A band is computed **only** when both hold: the lock hazard is at least `FULL_SCAN`, and a
production snapshot established the size of what the lock covers.

| Band | Estimated duration | Effect on the grade |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | cap at B |
| `OUTAGE` | over 5m | cap at C |

**A band may only lower a grade, never raise one.** "Lower" means worse: A → B → C → F. A
`NEGLIGIBLE` band imposes nothing at all — a small table does not turn a C into a B, because the
absence of evidence of a problem is not evidence of safety. The same is true of an absent band: a
missing snapshot, a stale one, or a table the snapshot does not describe all leave the grade
exactly where it was.

Note that `DISRUPTIVE`'s ceiling is already implied by the `FULL_SCAN` condition that gates the
band in the first place, so in practice `OUTAGE` is the only band that moves a grade. The cap is
implemented anyway, so that the two rules stay independent.

Durations come from `size_bytes / rate`, with the rate chosen by lock hazard and both rates
documented in [`ESTIMATES.md`](ESTIMATES.md). **They are estimates and are labelled as estimates
wherever they appear.**

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

---

## 4. Owner rulings

Resolved by the owner. Treat these as spec.

1. **A cap overrides an assignment.** Caps are not tie-breakers, they are ceilings. Zero COSTLY
   findings, everything REVERSIBLE, but a missing `down.sql` → final grade **C**. Formally: a
   grade is assigned, then every active cap is applied, and the worst result wins.
2. **Kubernetes findings never hold database locks.** Their `LockHazard` is strictly `NONE`.
   Never any other value.
3. **`type ChangeRef string`** — a commit SHA or PR ref.
   **`type UndoStep string`** — the exact command to run (an SQL statement or a `kubectl`
   invocation), not prose.

