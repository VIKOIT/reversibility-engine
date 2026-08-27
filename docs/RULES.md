# Rules Specification

**This document is the specification. The code is written to match it, not the other way
around.** Where an implementation disagrees with a table below, the implementation is the bug.

Three classification tables and one scoring procedure decide every grade the Reversibility
Engine emits:

| | | Rules |
| --- | --- | --- |
| [§1 PostgreSQL](#1-postgresql--pg001-to-pg059) | over a real PostgreSQL AST | 59 |
| [§2 Kubernetes](#2-kubernetes--k8s001-to-k8s015) | over a structural manifest diff | 15 |
| [§3 Scoring](#3-scoring) | how findings become a grade of A, B, C, or F | — |
| [§4 Owner rulings](#4-owner-rulings) | decisions that resolve ambiguities in the above | — |
| [§5 Terraform](#5-terraform--tf001-to-tf010) | over plan JSON, backed by a resource-type catalog | **9 active** (`TF003` retired) |

**A section number in this document is written `§n`. A section number in
[`docs/SPECIFICATION.md`](SPECIFICATION.md) is always written `docs/SPECIFICATION.md §n`.** Both
files number their sections from 1, and an unqualified reference to the wrong one sends a reader
to a section that exists and says something else.

## The rule every other rule defers to

**Unknown means unsafe.** An error, a panic, an unparseable file, or a construct no rule
describes must never become a passing grade. Every such path terminates in **F**. There is no
"probably fine": a verdict is `REVERSIBLE`, `COSTLY`, `IRREVERSIBLE`, `UNKNOWN`, or `WILL_FAIL`,
and the last three all fail.

**And a gate must prove that it ran.** The rule above governs what happens once a change has been
*read*. It says nothing about a run that read nothing at all, and that run once had the most
permissive outcome in the system, because "no findings" and "no analysis" produced the same green
check. So the second invariant is about the **shape of the run**, not the verdict: **no
certificate produced means exit 2, never exit 0.** Absence of output is never success. See
[docs/SPECIFICATION.md §2](SPECIFICATION.md) for the incident that established it.

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

### From a verdict to an exit code

A grade is not an exit code. The mapping is fixed, and it is the contract every CI integration
depends on:

| Exit | Meaning | Reached when |
| --- | --- | --- |
| `0` | The run completed and the gate was met. | The effective grade is at or above the threshold. |
| `1` | The run completed and the gate was **not** met. | The effective grade is below the threshold. The certificate is still written — it is the artifact the user asked for. |
| `2` | **The run did not complete.** | No arguments were given; no certificate was produced; a certificate's verdict cannot be read back; a policy file cannot be resolved; the changeset could not be fetched; the changeset held files that plausibly are migrations and no analyzer could assess them (`UNSUPPORTED_CONTENT`, and only when a gate was asked for — with no `--gate` or `--min-grade` nothing is being gated, exactly as grade F exits 0 there); or `--require-full-coverage` was given and coverage was `PARTIAL`. |

Three consequences follow, and each is enforced by a test rather than by convention:

- **`revctl` invoked with no arguments exits 2**, printing help to **stderr** — stdout is where a
  certificate goes. Asking for help explicitly (`--help`, `help`) still exits **0**; without that
  exception, users learn to ignore the exit code and the rule above stops protecting anything.
- **A grade of F is exit 1, not exit 2.** A correctly detected irreversible change is the engine
  working. Conflating it with a broken run is how a broken run gets ignored.
- **Nothing may turn *no analysis* into a passing grade**, which is the same rule as "nothing may
  turn an error into a passing grade" applied one level up. A run that analyzed nothing has no
  verdict to report, and a missing verdict is never a good one.

There used to be an exception written here, and it was the P0. It said that a changeset
genuinely containing no file any analyzer claims grades **A** with `Applicable: false` and gate
**PASS** — a completed run with a real, empty answer, distinguishable from a broken run because
a certificate exists to say so. The certificate did exist, and it said A / PASS, which nothing
downstream distinguishes from an analyzed A.

**There is no exception now.** A run that analyzed nothing reports `Grade: N/A` and
`AIGateStatus: NOT_APPLICABLE`, and the three-way outcome in §3 decides whether that is an
exit 0 or an exit 2. The distinction the old rule wanted is real — a docs-only pull request
genuinely is different from a broken run — and it is now carried by `Outcome`, which is a field
a reader can act on, rather than by a grade that reads as an endorsement.

## Changing a rule

1. **A rule with no fixture does not exist.** Every rule ID has a fixture directory under
   `testdata/fixtures/`, and `internal/fixture` fails the build if one is missing.
   The single exception is a **retired** ID — one that was specified, considered, and
   deliberately not implemented. A retired ID is declared as such in
   `internal/fixture/coverage_test.go`, has no fixture, and **is never reused and never
   renumbered**. `TF003` is the only one today. A retired ID with its reason written down tells a
   contributor the case was thought about; a gap in the sequence reads as an oversight.
2. Write or amend the fixture first, then the code.
3. Regenerate the verdict snapshot with `go test ./internal/engine -update` and **review the
   diff** — `testdata/fixtures/golden/verdicts.txt` shows what every fixture now grades, so a
   change that quietly moves fixtures between grades is visible in the pull request.
4. Disagreement about a classification is the most valuable contribution there is. Open an issue
   arguing the case; the tables are the product.

Process, layout, and engineering standards live in
[`docs/SPECIFICATION.md`](SPECIFICATION.md). Open questions about rules that have not been settled
are in [`docs/SPECIFICATION.md`](SPECIFICATION.md) §16 — do not resolve one by guessing.

---

## 1. PostgreSQL — PG001 to PG059

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
| PG016 | `DROP VIEW` / `DROP FUNCTION` / `DROP TRIGGER` — **plain views only; a materialized view is PG029** | COSTLY | SHORT |
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
| PG028 | `CREATE OR REPLACE VIEW` | COSTLY | SHORT |
| PG029 | `DROP MATERIALIZED VIEW` | COSTLY | EXCLUSIVE |
| PG030 | `ADD CONSTRAINT ... USING INDEX` | REVERSIBLE | SHORT |
| PG031 | `VALIDATE CONSTRAINT` | REVERSIBLE | FULL_SCAN |
| PG032 | `GRANT` / `REVOKE` | REVERSIBLE | SHORT |
| PG033 | `COMMENT ON` | REVERSIBLE | SHORT |
| PG034 | `SET` / `SET LOCAL` | REVERSIBLE | NONE |
| PG035 | `LOCK TABLE` | REVERSIBLE | EXCLUSIVE |
| PG036 | `ANALYZE` | REVERSIBLE | NONE |
| PG037 | `VACUUM` | REVERSIBLE | NONE |
| PG038 | `VACUUM FULL` | REVERSIBLE | EXCLUSIVE |
| PG039 | `CREATE EXTENSION` | REVERSIBLE | SHORT |
| PG040 | `CREATE SCHEMA` | REVERSIBLE | NONE |
| PG041 | `CREATE SEQUENCE` | REVERSIBLE | NONE |
| PG042 | `CREATE FUNCTION` (no `OR REPLACE`) | REVERSIBLE | NONE |
| PG043 | `CREATE OR REPLACE FUNCTION` | COSTLY | SHORT |
| PG044 | `CREATE TRIGGER` | REVERSIBLE | SHORT |
| PG045 | `CREATE MATERIALIZED VIEW ... WITH DATA` | REVERSIBLE | FULL_SCAN |
| PG046 | `CREATE MATERIALIZED VIEW ... WITH NO DATA` | REVERSIBLE | NONE |
| PG047 | `REFRESH MATERIALIZED VIEW` (`CONCURRENTLY` is FULL_SCAN) | REVERSIBLE | EXCLUSIVE |
| PG048 | `ALTER TYPE ... ADD VALUE` | IRREVERSIBLE | SHORT |
| PG049 | `CREATE POLICY` | REVERSIBLE | SHORT |
| PG050 | `DROP POLICY` | COSTLY | SHORT |
| PG051 | `ENABLE ROW LEVEL SECURITY` | REVERSIBLE | SHORT |
| PG052 | `DISABLE ROW LEVEL SECURITY` | IRREVERSIBLE | SHORT |
| PG053 | `ALTER INDEX ... RENAME TO` | COSTLY | SHORT |
| PG054 | `ALTER TABLE ... SET (...)` / `RESET (...)` | COSTLY | SHORT |
| PG055 | `ALTER TABLE ... SET LOGGED` / `SET UNLOGGED` | COSTLY | TABLE_REWRITE |
| PG056 | `ATTACH PARTITION` | REVERSIBLE | FULL_SCAN |
| PG057 | `DETACH PARTITION` (`CONCURRENTLY` is NONE) | COSTLY | SHORT |
| PG058 | `INSERT` | COSTLY | SHORT |
| PG059 | `INSERT ... ON CONFLICT DO UPDATE` | IRREVERSIBLE | SHORT |

**PG028 assumes the view already existed**, because that is the only reason the statement is
written with `OR REPLACE`. The previous definition is overwritten and is recorded nowhere in the
changeset, which makes the change COSTLY rather than reversible. **Its undo step is never a bare
`DROP VIEW`** — that would destroy an object that existed before the migration, and an undo plan
that destroys a pre-existing object is worse than no undo plan. What is emitted instead names
what has to be recovered and warns against the drop.

**PG029 is separate from PG016 because a materialized view holds rows of its own**, which is the
entire distinction between the two objects. Folding them together produced a rationale stating
that the object "holds no data of its own" — false — and an undo naming the wrong object type.
The rows are derived, so a `REFRESH` can rebuild them, which is why this is COSTLY and not
IRREVERSIBLE; the rebuild may be expensive and will not reproduce the old contents if the
sources have changed since.

**PG030 and PG031 are the second halves of the two safe patterns**, and before they existed the
engine graded both worse than the unsafe one-step forms they replace: `ADD CONSTRAINT ... USING
INDEX` promotes an index that already exists, so it neither scans nor builds, and `VALIDATE
CONSTRAINT` completes the `NOT VALID` sequence PG022 begins. A safety tool that punishes the safe
pattern teaches people to stop using it, which is worse than the coverage gap it sat inside.

**PG032 is REVERSIBLE because privileges are not data** and the opposite statement restores them
exactly. The engine does not verify that the opposite statement is present, and the rationale
says so on every finding. **PG033 is REVERSIBLE for a narrower reason**: overwriting a comment
loses the previous text, but a comment is not an object and not a row. The overwrite principle
that governs PG028 deliberately stops short of it.

### `CONCURRENTLY` changes the lock and never the verdict

> **`CONCURRENTLY` is a lock-hazard modifier, not a reversibility modifier.** A concurrent
> statement does exactly what its blocking counterpart does; it takes longer and holds less. It
> never changes what can be undone.

Four pairs follow this, and none of them was written with the others in view:

| Blocking | Concurrent | Verdict |
| --- | --- | --- |
| PG014 `DROP INDEX` — EXCLUSIVE | PG015 — NONE | COSTLY both |
| PG023 `CREATE INDEX` — EXCLUSIVE | PG024 — NONE | REVERSIBLE both |
| PG047 `REFRESH MATERIALIZED VIEW` — EXCLUSIVE | FULL_SCAN | REVERSIBLE both |
| PG057 `DETACH PARTITION` — SHORT | NONE | COSTLY both |

**The table encodes them two different ways and that is an inconsistency, not a distinction.**
PG014/PG015 and PG023/PG024 are separate rule IDs; PG047 and PG057 are single IDs with a
conditional lock. Both work. Neither can be changed retroactively, because rule IDs are never
renumbered and consumers alert on them.

**Going forward: one rule ID, conditional lock.** A reader looking up "can I undo a concurrent
index drop" should not have to discover that the answer lives under a different number from the
blocking form, when the answer is identical. The four existing rows stay as they are, and
[`docs/RULES.md` §1](RULES.md#1-postgresql--pg001-to-pg059) says which encoding each uses.

### The overwrite principle

> **A statement that overwrites state the migration does not record is COSTLY, not REVERSIBLE.**
> The undo exists in principle and cannot be written from the changeset alone.

PG012 (`RENAME`) and PG013 (`DROP CONSTRAINT`) already followed it before it was named. PG028,
PG043, PG050, PG054 and PG057 are the same shape. PG033 is the one deliberate exception, on the
ground that a comment is documentation rather than schema — flagged rather than smoothed over,
because it is the weakest application of the rule.

`REVERSIBLE` here means **the undo is writable from this changeset**, not merely that the
database could theoretically be restored.

### Creation and destruction are not mirrors

> **A rule for creating something and a rule for destroying it are separate rules with
> independent verdicts.** Creation adds where there was nothing, and its inverse is exact.
> Destruction removes something whose definition or contents the changeset does not hold, and
> its inverse is a reconstruction from evidence the engine does not have.

The pairs, and the gap in each:

| Create | Destroy | Why they differ |
| --- | --- | --- |
| PG025 `CREATE TABLE` — REVERSIBLE | PG001 `DROP TABLE` — IRREVERSIBLE | The rows. |
| PG039 `CREATE EXTENSION` — REVERSIBLE | PG011 `DROP EXTENSION` — IRREVERSIBLE | The cascade to dependent objects. |
| PG049 `CREATE POLICY` — REVERSIBLE | PG050 `DROP POLICY` — COSTLY | The policy expression. |
| PG051 `ENABLE RLS` — REVERSIBLE | PG052 `DISABLE RLS` — IRREVERSIBLE | The protection, while it was off. |
| PG042 `CREATE FUNCTION` — REVERSIBLE | PG016 `DROP FUNCTION` — COSTLY | The body. |

Reading these as a symmetric pair is the most natural mistake available here, and it is always
wrong in the permissive direction: it argues *"we can just put it back"* about the one case where
the changeset does not say what to put back. **The asymmetry is the rule, not the exception.**

The one genuine exception is PG032, where `GRANT` and `REVOKE` are both REVERSIBLE — because the
opposite statement really does restore the prior state exactly, which is the test. It is marked
here so a reader meets it as a considered exception rather than as a counter-example.

### An undo step must be safe to run, not merely correct

> **An undo step is a script an operator will paste under pressure.** A step that is a correct
> inverse of the change and destructive to execute must say so instead of being emitted bare.

Three rules turn on this, and it was learned from the first:

- **PG028** `CREATE OR REPLACE VIEW` — `DROP VIEW` is the correct schema inverse of *creating* a
  view and destroys one that existed before the migration. This shipped, graded **A**, and
  printed the drop.
- **PG029** `DROP MATERIALIZED VIEW` — the inverse names the object type the statement did not
  operate on, and omits the `REFRESH` without which the object is empty.
- Proposed for `CREATE TABLE ... AS SELECT` — the inverse destroys a snapshot the migration
  produced, which is usually the backup a later rollback depends on.

Where this applies, the emitted step is prose naming what has to be confirmed, and any
destructive statement in it is **commented out**. A plan that is pasted whole then warns instead
of deleting.

Note what this is not: it is not a reason to omit the undo. [`§3`](#undoplan) already says an
UNKNOWN finding replaces the whole plan, because listing steps for everything else would claim a
completeness the plan does not have. This is the narrower case where a step exists, is correct,
and needs a sentence beside it.

### PG052 and the third clause

**`DISABLE ROW LEVEL SECURITY` is IRREVERSIBLE, and not by the data-loss test.** It destroys no
data, and the setting is one line to restore, so the first two clauses of the discriminator in
§5 do not apply. The third does:

> A change is IRREVERSIBLE if it destroys data, destroys an identity that re-applying the same
> configuration cannot recreate, **or destroys a recovery capability that a future rollback
> would depend on.**

For as long as row-level security is off, every row is visible to every role, and no rollback
un-reads what was read. That is the same clause TF004 fires on for deletion protection, and the
same family: **one principle, two analyzers.** The rationale on the finding says so, because a
user reading an F on a one-line change is owed the reason.

PG050 (`DROP POLICY`) is deliberately *not* in this family. Dropping one policy narrows what is
protected; it does not switch the protection off.

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
  not contradict docs/SPECIFICATION.md §16.2: that rule is about overlapping rules on one
  *command*.
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
  and the product rests on those two never being confused. See the consequence in
  docs/SPECIFICATION.md §16.5, and what it does to the undo plan in §3 below.
- **Only an explicit `reclaimPolicy: Retain` prevents K8S003.** Absent, empty, or unresolvable is
  treated exactly like `Delete`, as the table above requires.
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
missing snapshot, or a table the snapshot does not describe, leaves the grade exactly where it
was.

**A stale snapshot is not in that group.** It is used and flagged rather than discarded — see
[`docs/PRODUCTION-CONTEXT.md`](PRODUCTION-CONTEXT.md) — so it still produces a band, and that band
still caps. Old numbers are still evidence; they are just evidence about a system that has moved
on, which is what the warning on the certificate says.

Note that `DISRUPTIVE`'s ceiling is already implied by the `FULL_SCAN` condition that gates the
band in the first place, so in practice `OUTAGE` is the only band that moves a grade. The cap is
implemented anyway, so that the two rules stay independent.

Durations come from `size_bytes / rate`, with the rate chosen by lock hazard and both rates
documented in [`ESTIMATES.md`](ESTIMATES.md). **They are estimates and are labelled as estimates
wherever they appear.**

```
AIGateStatus = NOT_APPLICABLE  <=>  Grade == N/A
AIGateStatus = PASS            <=>  Grade == A
                                    AND Coverage == FULL
                                    AND no candidate was ignored by policy
AIGateStatus = FAIL             otherwise
```

### Every grade carries its cause

**`GradeCauses` explains the grade and appears in every rendered output**, per the invariant in
[`docs/SPECIFICATION.md` §2](SPECIFICATION.md#2-the-philosophy-fail-closed): the assignment
first, then each cap that lowered it, naming the rule, file, or condition responsible.

```
- assigned A: every finding is REVERSIBLE
- capped at C: no usable down migration for 0031_add_users.up.sql
```

Grade A states that nothing capped it. That is not filler: *"nothing capped this"* and *"nobody
wrote down why"* must not render identically, and before this field existed a capped grade was
unexplainable from the certificate a reviewer actually reads.

### A policy `ignore:` closes the gate and does not touch coverage

An ignore is a human decision, exactly like a waiver, and it follows the waiver rule:

| | Effect on `Grade` | Effect on `Coverage` | Effect on `AIGateStatus` |
| --- | --- | --- | --- |
| A waiver | none | none | none — it moves `EffectiveGrade` and the exit code |
| An unreadable file | none | `PARTIAL` | closes it |
| A policy `ignore:` | none | **none — stays `FULL`** | **closes it** |

Coverage describes **capability**, not permission: the engine could have read an ignored file
and was told not to. `IgnoredByPolicy` lists every candidate excluded, and the markdown renders
it above the findings so a reader never has to infer what was skipped.

One principle spans all three: **humans may accept risk with their names on it; agents may not
inherit it.**

### Coverage: how much of the changeset was read

**Coverage is a fact about the changeset, not a penalty.** It is a second axis, and it is
deliberately not folded into the grade.

| Coverage | Reached when |
| --- | --- |
| `FULL` | Every file any analyzer could claim was claimed. A changeset with nothing claimable is vacuously full — nothing was skipped. |
| `PARTIAL` | Files that plausibly are migrations went unread. `UnanalyzedFiles` names every one of them, each with the reason. |

- **`PARTIAL` never changes the grade.** A file the engine cannot read is not evidence that the
  change is unsafe. Inventing severity from ignorance is the mirror image of inventing safety
  from it, which is the bug §3 already had once — and it is the easier of the two to defend,
  because a tool that over-reports looks conscientious.
- **A PASS requires grade A *and* full coverage.** An autonomous agent gets no merge on a
  changeset that was only partly understood. A human reading a `PARTIAL` certificate can see the
  list of files nobody analyzed and judge for themselves; an agent cannot, so it does not get the
  benefit of the doubt.
- **The markdown certificate names every unanalyzed file, above the findings.** All of them,
  never a count and never a sample. A list of what the engine *did* find, printed first, is
  exactly what makes an incomplete analysis look complete.
- **`--require-full-coverage` makes `PARTIAL` exit 2.** Off by default, because a partially
  covered changeset is still a real measurement of the part that was read. A team standardised on
  a migration format this engine cannot read will want it on.

**The exit code and `AIGateStatus` diverge here, deliberately.** Grade A with partial coverage
exits **0** under `--gate` and reports `aiGateStatus: FAIL`. The exit code is the human
pipeline's gate — it compares `EffectiveGrade` and honours waivers — and `--require-full-coverage`
is how a pipeline opts into the agent's stricter bar.

This is the same separation as the S10 waiver ruling and it is now the project's pattern for the
whole class: **the grade describes the evidence, and the gate decides what to do about it.** A
waiver moves the gate and never the grade; coverage moves the gate and never the grade. See
[`docs/SPECIFICATION.md` §2](SPECIFICATION.md#2-the-philosophy-fail-closed).

### What the run was able to do at all

**The engine never emits a passing grade for a changeset it did not analyze. Absence of
analysis is not evidence of safety.** Grading is therefore the second question. The first is
what the run was able to do at all, and it has three answers.

| Outcome | Reached when | Grade | Gate | Exit under a gate |
| --- | --- | --- | --- | --- |
| `ANALYZED` | At least one analyzer claimed at least one file. | graded normally | follows the grade | 0 / 1 by the grade |
| `NO_CANDIDATES` | The changeset contains no file any analyzer could ever claim — a docs-only pull request, Go source only. | **N/A** | `NOT_APPLICABLE` | **0** |
| `UNSUPPORTED_CONTENT` | Files are present that plausibly **are** migrations or manifests, and no analyzer claimed them. | **N/A** | `NOT_APPLICABLE` | **2** |

`A` means **analyzed and found reversible**, and nothing else. Neither non-analyzed outcome may
ever produce `A`, and neither may ever produce `PASS`. This replaces the rule that stood here
until the P0: *empty changeset with zero relevant files → grade A, `Applicable: false`, gate
PASS*. That rule shipped, it was wrong, and it is recorded rather than deleted because the
argument for it was plausible and will be made again — see
[`docs/SPECIFICATION.md` §2](SPECIFICATION.md#2-the-philosophy-fail-closed).

**What counts as plausibly a migration.** The predicate is deliberately narrow, because a false
`UNSUPPORTED_CONTENT` is an exit 2 on a pull request that deserved a clean 0:

- any file whose extension is `.py`, `.rb`, `.js`, or `.ts` **and** which sits under a path
  segment named `migrations`, `migration`, or `migrate` — Django's `<app>/migrations/` and
  Rails' `db/migrate/` are the two shapes this is drawn from;
- any `.sql` file no analyzer claimed, wherever it sits. A claimed `.sql` that fails to parse
  is not this case: it is `ANALYZED`, and PG027 grades it **F**.

A file under `migrations/` with any other extension — a `README.md`, a `.gitkeep`, a `.go`
source file — is **not** a candidate. The directory name alone is not the signal; the directory
name together with a language a migration is written in is.

**The message must name what it saw and what it could not do.** One line per directory, listing
the count, the directory, and the extensions:

```
found 13 files in django/contrib/auth/migrations that no analyzer supports
(.py migrations). Reversibility was not assessed.
```

**Partial coverage is the other half of this and is a separate axis.** A changeset holding one
`.sql` file and thirteen Django `.py` migrations is `ANALYZED` — one analyzer did claim a file —
and it is also `PARTIAL`. It grades on what was read and it does not pass the merge gate. See
"Coverage" below.

### UndoPlan

Generated **only** from the `UndoStep` fields of findings, in **reverse order of application**.

If any finding is `IRREVERSIBLE`, `UNKNOWN`, or `WILL_FAIL`, the plan is **replaced** by an
explicit statement that no complete undo exists, listing what cannot be undone and why. The three
carry different reasons and are printed with different wording, because the remedy differs:

| Verdict | What the plan says | Because |
| --- | --- | --- |
| `IRREVERSIBLE` | cannot be undone | The change will apply and cannot be taken back. |
| `UNKNOWN` | was not understood, so no undo can be written for it | Listing steps for everything *else* would claim a completeness the plan does not have. A confident-looking script printed beside an unclassified change is the wrong-safe-verdict failure this product exists to prevent. |
| `WILL_FAIL` | will not apply, so there is nothing to undo; fix the migration instead | The statement aborts and rolls its transaction back, so nothing it did survives to be undone — and neither does anything else in the same migration. A rollback script here would describe a state the database is never going to be in. |

Extending the replacement from `IRREVERSIBLE` to `UNKNOWN` was resolved in S4 and is recorded as
docs/SPECIFICATION.md §16.6; `WILL_FAIL` was added with the verdict itself. Both affect
presentation only — each of the three already grades **F**.

The replacement is written as SQL comments so the plan stays a pasteable script. An operator who
copies the whole thing under pressure then runs the steps that exist and reads the warning,
rather than hitting a syntax error and losing both.

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
- **Grade assembly is: assign, then cap, worst wins** (§4.1). Assignment is `>=3 COSTLY → C`,
  `1–2 COSTLY → B`, else A. Caps are applied unconditionally so order cannot matter.
- **The A row is read as a set of necessary conditions.** Failing any of them
  (`all REVERSIBLE`, `lock <= SHORT`, `down.sql ok`) caps the grade at B — B being the highest
  grade below A, the least punitive reading consistent with the table. This is what decides
  `FULL_SCAN`, which fails "lock <= SHORT" but does not reach the `TABLE_REWRITE` cap.
- **Down-migration status travels through the optional `analyzer.DownMigrationValidator`
  interface**, type-asserted by the orchestrator. The engine never imports an analyzer package;
  the delivery layer wires them (docs/SPECIFICATION.md §16.1, resolved).
- **`Blockers` are populated only for grade F**, per docs/SPECIFICATION.md §8. Findings explain B
  and C.
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

## 5. Terraform — TF001 to TF010

**Do not infer, extend, or soften this table.** Input is `terraform show -json` output and
nothing else — **`terraform.tfstate` is never read**, because state holds provider credentials
and attribute values in plaintext.

Only destruction is classified. A created or updated-in-place resource has a reverse by
construction, which is what keeps the catalog finite: the problem was never "hundreds of AWS
resource types", it is the types whose destruction hurts. TF004 is the one deliberate exception.

| Rule | Change | Reversibility |
| --- | --- | --- |
| TF001 | delete of a stateful resource | IRREVERSIBLE |
| TF002 | forced replacement (delete + create) of a stateful resource | IRREVERSIBLE |
| ~~TF003~~ | **RETIRED — never reused.** See below. | — |
| TF004 | a recovery capability was switched off | IRREVERSIBLE |
| TF005 | delete of a stateless resource | COSTLY |
| TF006 | replacement of a stateless resource | COSTLY |
| TF007 | in-place update | REVERSIBLE |
| TF008 | create | REVERSIBLE |
| TF009 | unparseable plan or unrecognized format version | UNKNOWN |
| TF010 | delete of a type the catalog does not classify | UNKNOWN |

Lock hazard is always NONE: Terraform takes no database lock. Plan format versions **1.0 and
1.1** are read; any other version is TF009, never a best-effort guess.

### The discriminator

> A resource change is IRREVERSIBLE if it destroys data, destroys an identity that re-applying
> the same configuration cannot recreate, **or destroys a recovery capability that a future
> rollback would depend on.**

The third clause is what TF004 fires on, and it is what makes TF001 on `aws_db_snapshot`
coherent: deleting a snapshot destroys no running system, it destroys the undo. Same family.

**The discriminator is not Terraform-specific.** It is stated here because this is where it was
written down first, and it governs [`§1`](#1-postgresql--pg001-to-pg059) too: **PG052**
(`DISABLE ROW LEVEL SECURITY`) is IRREVERSIBLE on the third clause and on nothing else. One
principle, two analyzers — a rule that reached the same verdict by a different route in each
would be two rules that happen to agree.

### Why TF003 is retired

`prevent_destroy` is a `lifecycle` meta-argument, and the JSON configuration representation does
not carry lifecycle blocks. The signal is also self-erasing: if `prevent_destroy` were still set,
`terraform plan` fails and emits no plan, so **any plan containing a delete already proves it is
not set**. Detecting its removal requires the previous configuration, which a plan does not
contain. TF001 and TF002 catch the destroy itself, at the moment it matters.

The number is never reused. A retired ID with a reason says the case was considered; a gap in the
sequence reads as an oversight.

### Classification order

1. **Evidence in the plan**, before the catalog. Any of these on the `before` object marks the
   resource STATEFUL: `allocated_storage`, `backup_retention_period`, `deletion_protection`,
   `ephemeral_block_device`, `final_snapshot_identifier`, `kms_key_id`, `point_in_time_recovery`,
   `private_key`, `snapshot_identifier`, `storage_encrypted`, `versioning`.
   **Evidence may only raise.** Its absence implies nothing, never "stateless".
   *Presence means present and meaningfully set*: `null`, `""`, `[]` and `{}` are not evidence;
   `false` and `0` are, because the attribute existing is the schema signal.
2. `force_destroy: true` or `skip_final_snapshot: true` on a destroyed object elevates to
   IRREVERSIBLE **whatever the class**, because the author explicitly disabled the mechanism that
   would have preserved anything.
3. **The catalog**, `catalog/terraform/aws.yaml`.
4. **User `terraform_types`** — classify an unknown type or tighten a known one. Never weaken.
5. Nothing matched → TF010.

**The type name is never matched.** `aws_db_subnet_group` contains "db" and holds nothing.

### TF004 — the closed list

Fires only on these exact paths and only in these directions. Anything else is TF007.

| Path | Transition |
| --- | --- |
| `deletion_protection` | true → false |
| `enable_deletion_protection` | true → false |
| `skip_final_snapshot` | false → true |
| `force_destroy` | false → true |
| `backup_retention_period` | n > 0 → 0 |
| `deletion_window_in_days` | n > 0 → 0 |
| `versioning.enabled` | true → false |
| `point_in_time_recovery.enabled` | true → false |

Two paths reach one level into a block; that is the only nesting permitted. A named path list is
auditable against this table, and general recursion into a provider-defined object is not.

### As built in S12

- **`*.tfplan.json` only, plus `--terraform-plan` for a plan named otherwise.** Claiming a bare
  `plan.json` would grade **F** on any repository that happens to contain one, because a file the
  analyzer claims and cannot read is `TF009`/UNKNOWN. The flag matches by path suffix in either
  direction: it is typed relative to a shell, while `Supports` is asked about the
  changeset-relative path, and the two spellings differ.
- **All Terraform findings carry `Line: 0` and `LockHazard: NONE`.** A plan is a JSON document
  describing resource changes, not a file with a blameable line, and Terraform takes no database
  lock. The Kubernetes rules reach `NONE` the same way, by the ruling in §4.2; that ruling
  names Kubernetes only, so the Terraform case is specified by the §5 table above rather
  than inherited from it.
- **`TF009` and `TF010` are different findings with different remedies**, which is why they are
  two rules and not one. `TF009` is "I cannot read this file" — a malformed plan, or a
  `format_version` outside the supported set. `TF010` is "I read it and do not know this type".
  Both grade **F**; only `TF010` carries the growth-loop output below.
- **The unknown-type output is one snippet and one link, covering every unclassified type at
  once.** Not one each: six unknown types meaning six paste operations is where somebody switches
  the gate off instead, which costs more safety than any single classification buys back. **The
  suggested class is always `STATEFUL`** — the fail-closed direction, and the honest one. A
  snippet that guessed `STATELESS` would be a snippet that talked the user into the answer they
  wanted.
- **Nothing is sent anywhere.** The issue link is a URL printed into a certificate the reader is
  already looking at; a human chooses to open it. `revctl check` has **no network path at all** —
  not on a cache miss, not when the catalog is old, not ever — and the embedded catalog stays
  fully functional offline for the lifetime of the binary.
- **The catalog is an input to the verdict, so it is attributed like one.** The analyzer
  implements `analyzer.CatalogVersioner`; the engine records `CatalogVersion` on the certificate
  and mixes the catalog digest into `InputDigest` — but **only when an analyzer implementing that
  interface actually claimed a file**. Registering the Terraform analyzer therefore changed no
  existing digest, and `verdicts.txt` did not move.
- **Every catalog entry requires an evidence link**, checked at load: a classification nobody can
  check is an opinion. This is also what stops generated output being merged unread — `revctl
  catalog scan` proposes candidates with an empty evidence field, and an entry without one fails
  the build.
- **`catalog scan` is a maintainer tool and nothing in the check path depends on it.** It shells
  out to `terraform providers schema -json`, fails with a message naming what to install when
  terraform is absent, and skips its own tests cleanly on a machine without it.
- **Coverage is published honestly: 92 of roughly 1,400 AWS resource types** — 48 stateful, 44
  stateless. Both the raw count and the denominator go in the docs. The stateless half is
  load-bearing rather than filler: an unclassified deleted type grades F, so the network, IAM,
  load-balancing and compute groups are what stand between a new user and an immediate failing
  gate.

