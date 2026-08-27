# Proposal — PostgreSQL dialect coverage, first 30

**STATUS: RULED AND SHIPPED. This file is now a record of how the rows were arrived at, not a
request.** [`docs/RULES.md`](../RULES.md) is authoritative; where the two differ, that one wins.

All 30 rows below were approved and implemented as **PG028–PG059**, each with a fixture. The
provisional IDs in the tables no longer match the shipped ones — the mapping is:

| Proposal | Shipped |
| --- | --- |
| D1 `CREATE OR REPLACE VIEW` | PG028 |
| D2 `DROP MATERIALIZED VIEW` | PG029 |
| #18, #19 the two safe-pattern halves | PG030, PG031 |
| #12/#13 `GRANT`/`REVOKE`, #14 `COMMENT ON` | PG032, PG033 |
| #1–#5 Tier P | PG034–PG038 |
| #6–#11, #15–#17, #20 Tier H | PG039–PG048 |
| #21–#30 Tier M | PG049–PG059 |

**Two rulings changed a proposed classification**, and both are worth reading because the
reasoning generalises:

- **#24 `DISABLE ROW LEVEL SECURITY` → IRREVERSIBLE, not COSTLY.** Not by the data-loss test —
  it destroys no data — but by the **third clause** of the discriminator, the same clause TF004
  fires on for deletion protection. One principle, two analyzers.
- **#30 `INSERT ... ON CONFLICT DO UPDATE` → IRREVERSIBLE (PG059), split from `INSERT`
  (PG058).** It is an `InsertStmt` whose effect is an update, and the row count it overwrites is
  unknowable from the statement alone.

**Still open:** nothing from this list. The constructs the probe still reports as PG027 —
`CREATE ROLE`, `DROP ROLE`, `SET STATISTICS`, `OWNER TO`, `ALTER SEQUENCE OWNED BY`, `REINDEX`,
`CLUSTER`, identity columns, `CREATE TABLE ... AS` — were never proposed and still fail closed.
They are the next batch, if there is one.

---

## What was asked for, and what this is

The ask was the top 30 dialect cases **by frequency in the corpus**. This is not that, and the
difference matters enough to state before the table rather than after it.

**The 711-case dialect corpus lives in the harness repository, not here.** It is not on this
machine and the harness is paused. So there is no frequency data behind the ordering, and any
number I attached to one would be invented. What is here instead:

| Column | Where it comes from |
| --- | --- |
| **Today** | **Measured.** Every construct below was run through the built `revctl` and the verdict recorded verbatim. This column is data. |
| **Tier** | **My judgment, not measurement.** See the tiers below. |
| Proposed classification | My proposal, for your ruling. |

The frequency tiers:

| Tier | Meaning |
| --- | --- |
| **P** | **Preamble.** Emitted by the migration tool itself, not written by a developer. Structurally present in a very large fraction of migrations in any repository that uses the tool at all. |
| **H** | Common in hand-written migrations. |
| **M** | Occasional. |
| **L** | Rare, but included because the classification is surprising. |

Tier P is the one claim here I would defend without corpus data, because it does not depend on
developer behaviour: if a tool emits `SET lock_timeout` in its transaction wrapper, every
migration it produces contains one.

**Re-ranking this against the real corpus is one command.** The probe that produced the "Today"
column is in `scripts/probe-dialect.sh` (added with this proposal, and it analyzes only — it
changes nothing). Point it at the corpus when the harness resumes and it will regenerate the
measured column and count occurrences.

---

## Before the table: two defects, which are not coverage gaps

These came out of probing and they are not "missing from the table". They are constructs the
**code already claims** and classifies wrongly, in the permissive direction, and both emit an
undo step an operator could run under pressure. They outrank everything below.

### D1 — `CREATE OR REPLACE VIEW` grades A / PASS and emits a destructive undo step

```console
$ revctl check ./migrations     # CREATE OR REPLACE VIEW active_orders AS SELECT ...
Grade A · aiGateStatus PASS

Undo plan:
  DROP VIEW active_orders;
```

`ViewStmt.Replace` is never read, so `CREATE OR REPLACE VIEW` is treated as `CREATE VIEW` and
matched by PG025 (REVERSIBLE / NONE). When the view already existed — which is the entire
reason anyone writes `OR REPLACE` — the migration **overwrote a definition that is recorded
nowhere**, and the generated undo drops a view that existed before the change. The certificate
says the change is fully reversible. It is not, and the plan it prints makes it worse.

Proposed: a distinct rule, **COSTLY / SHORT**, with no undo step — the prior definition cannot
be reconstructed from the migration, and printing `DROP VIEW` in its place is the wrong-safe
failure this product exists to prevent.

The same defect exists for `CREATE OR REPLACE FUNCTION`, which reaches PG027 today and so fails
closed by luck rather than by design. It should be classified deliberately, not left to the
default that happens to be right.

### D2 — `DROP MATERIALIZED VIEW` is graded as `DROP VIEW`, and the rationale is false

```console
$ revctl check ./migrations     # DROP MATERIALIZED VIEW order_totals;
Grade B · PG016 · COSTLY · SHORT
  "View order_totals holds no data of its own, so restoring it only requires
   replaying its definition"

Undo plan:
  CREATE VIEW order_totals /* original definition unavailable ... */;
```

`convertDrop` folds `OBJECT_MATVIEW` into `KindDropView`. A materialized view **does** hold data
of its own — that is the whole distinction — so the printed rationale is not merely imprecise,
it asserts the opposite of the truth. The undo names the wrong object type, and a real recovery
needs `CREATE MATERIALIZED VIEW` plus a `REFRESH` that can run for hours holding a lock.

Note also that PG016 in `docs/RULES.md` lists `DROP VIEW` / `DROP FUNCTION` / `DROP TRIGGER` and
says nothing about materialized views. **The code is claiming a construct the authoritative
table does not list**, which by this project's own rule makes the code the bug.

Proposed: a distinct rule, **IRREVERSIBLE / EXCLUSIVE**. The materialized rows are destroyed, and
"re-run the query" is not a restoration — it produces whatever the sources say now.

Both defects are one-line reproductions and neither needs the corpus. They are separable from
the 30 below and I would ship them first.

---

## The 30

`Today` is what the engine does now. `→` is the proposal. Provisional IDs only.

### Tier P — emitted by migration tooling, not by developers

| # | ID | Construct | Today | → Reversibility | → Lock | Note |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | PG028 | `SET statement_timeout` / `SET LOCAL lock_timeout` | **F** UNKNOWN | REVERSIBLE | NONE | Session-scoped and nothing persists. Every Rails, Sqitch, and Flyway wrapper emits one, so today a large class of repositories cannot reach grade A **at all** — the tool's own boilerplate fails the gate. I would rank this first on any corpus. |
| 2 | PG029 | `LOCK TABLE ... IN ... MODE` | **F** UNKNOWN | REVERSIBLE | EXCLUSIVE | Takes exactly the lock it names, for the transaction, and changes nothing. The honest lock hazard is the one in the statement. |
| 3 | PG030 | `ANALYZE` | **F** UNKNOWN | REVERSIBLE | NONE | Planner statistics only. |
| 4 | PG031 | `VACUUM` (plain) | **F** UNKNOWN | REVERSIBLE | NONE | `VACUUM FULL` is a different statement and a different row — see #5. |
| 5 | PG032 | `VACUUM FULL` | **F** UNKNOWN | REVERSIBLE | EXCLUSIVE | Rewrites the table under `ACCESS EXCLUSIVE`. Nothing is lost; the lock is the whole risk. |

### Tier H — common in hand-written migrations

| # | ID | Construct | Today | → Reversibility | → Lock | Note |
| --- | --- | --- | --- | --- | --- | --- |
| 6 | PG033 | `CREATE EXTENSION` | **F** UNKNOWN | REVERSIBLE | SHORT | Additive. Note the deliberate asymmetry with PG011 (`DROP EXTENSION`, IRREVERSIBLE): dropping can cascade to dependent objects, creating cannot. |
| 7 | PG034 | `CREATE SCHEMA` | **F** UNKNOWN | REVERSIBLE | NONE | Mirrors PG025 exactly. Already parsed (`KindCreateSchema`). |
| 8 | PG035 | `CREATE SEQUENCE` | **F** UNKNOWN | REVERSIBLE | NONE | Already parsed (`KindCreateSequence`). |
| 9 | PG036 | `CREATE FUNCTION` (no `OR REPLACE`) | **F** UNKNOWN | REVERSIBLE | NONE | Already parsed (`KindCreateFunction`). |
| 10 | PG037 | `CREATE OR REPLACE FUNCTION` | **F** UNKNOWN | **COSTLY** | SHORT | See D1. The prior body is recorded nowhere; **no undo step**. |
| 11 | PG038 | `CREATE TRIGGER` | **F** UNKNOWN | REVERSIBLE | SHORT | Takes `SHARE ROW EXCLUSIVE` on the table. Already parsed (`KindCreateTrigger`). |
| 12 | PG039 | `GRANT` | **F** UNKNOWN | REVERSIBLE | SHORT | No object or data is touched. **Caveat for your ruling:** if the grantee already held the privilege, `REVOKE` removes something that pre-existed. I propose accepting that as REVERSIBLE — privileges are not data — but it is the same shape as #13 and you may want them to match. |
| 13 | PG040 | `REVOKE` | **F** UNKNOWN | **COSTLY** | SHORT | Removes a privilege whose prior state the migration does not record. Same reasoning as PG013 (`DROP CONSTRAINT`). |
| 14 | PG041 | `COMMENT ON` | **F** UNKNOWN | REVERSIBLE | SHORT | Overwrites any previous comment, which is unrecorded — but a comment is documentation, not schema or data. Proposed REVERSIBLE on that ground; flagging it because it is the weakest application of the overwrite principle below. |
| 15 | PG042 | `CREATE MATERIALIZED VIEW ... WITH DATA` | **F** UNKNOWN | REVERSIBLE | FULL_SCAN | Additive, but it runs the query and holds a read lock on the sources for the duration. |
| 16 | PG043 | `CREATE MATERIALIZED VIEW ... WITH NO DATA` | **F** UNKNOWN | REVERSIBLE | NONE | Definition only; no scan. |
| 17 | PG044 | `REFRESH MATERIALIZED VIEW` | **F** UNKNOWN | REVERSIBLE | EXCLUSIVE | Derived data, so nothing original is lost. `REFRESH ... CONCURRENTLY` is FULL_SCAN and should be a separate row for the same reason PG023 and PG024 are separate. |
| 18 | PG045 | `ALTER TABLE ... ADD CONSTRAINT ... UNIQUE USING INDEX` | **F** UNKNOWN | REVERSIBLE | SHORT | **This is the recommended safe pattern** — build the index concurrently, then promote it. Today it grades F, so the engine actively pushes users toward the unsafe one-step form, which grades better. That inversion is worth fixing regardless of where it ranks by frequency. |
| 19 | PG046 | `ALTER TABLE ... VALIDATE CONSTRAINT` | **F** UNKNOWN | REVERSIBLE | FULL_SCAN | The second half of the PG022 `NOT VALID` pattern, and it has the same problem as #18: the engine grades the safe two-step sequence worse than the unsafe one. Nothing is lost; the scan is the cost. |
| 20 | PG047 | `ALTER TYPE ... ADD VALUE` | **F** UNKNOWN | **IRREVERSIBLE** | SHORT | **PostgreSQL cannot remove an enum value.** This is genuinely irreversible and currently reaches F only by falling through the default — right answer, no reasoning behind it. It is the clearest argument in this document for table coverage over a softer default: coverage turns an accidental F into a stated one. |

### Tier M — occasional

| # | ID | Construct | Today | → Reversibility | → Lock | Note |
| --- | --- | --- | --- | --- | --- | --- |
| 21 | PG048 | `CREATE POLICY` | **F** UNKNOWN | REVERSIBLE | SHORT | Additive. |
| 22 | PG049 | `DROP POLICY` | **F** UNKNOWN | COSTLY | SHORT | Definition unrecorded, and it removes a security control. |
| 23 | PG050 | `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` | **F** UNKNOWN | REVERSIBLE | SHORT | Additive restriction. |
| 24 | PG051 | `ALTER TABLE ... DISABLE ROW LEVEL SECURITY` | **F** UNKNOWN | COSTLY | SHORT | Removes a control. No data lost, so not IRREVERSIBLE — but this is a ruling I would like explicitly, because "reversible" reads oddly beside "silently exposed every row". |
| 25 | PG052 | `ALTER INDEX ... RENAME TO` | **F** UNKNOWN | COSTLY | SHORT | Mirrors PG012 (`RENAME TABLE` / `RENAME COLUMN`). |
| 26 | PG053 | `ALTER TABLE ... SET (...)` / `RESET (...)` | **F** UNKNOWN | COSTLY | SHORT | Storage parameters; the prior value is unrecorded. |
| 27 | PG054 | `ALTER TABLE ... SET UNLOGGED` / `SET LOGGED` | **F** UNKNOWN | COSTLY | TABLE_REWRITE | Both directions rewrite the table. Reversible in principle; the rewrite and the durability window are the cost. |
| 28 | PG055 | `ALTER TABLE ... ATTACH PARTITION` | **F** UNKNOWN | REVERSIBLE | FULL_SCAN | Scans to validate the partition constraint unless a matching one already exists. |
| 29 | PG056 | `ALTER TABLE ... DETACH PARTITION` | **F** UNKNOWN | COSTLY | SHORT | The rows survive as a standalone table and can be re-attached. `DETACH CONCURRENTLY` is NONE and wants its own row. |
| 30 | PG057 | `INSERT` | **F** UNKNOWN | COSTLY | SHORT | Seed and backfill data. Undo needs the inserted keys, which the migration has but the plan cannot generally reconstruct. **Sub-case needing its own ruling:** `INSERT ... ON CONFLICT DO UPDATE` overwrites existing rows and is an `UPDATE` in disguise — it should reach PG009 (IRREVERSIBLE), not this row. |

---

## The organizing principle I used, for you to accept or reject

Three of the proposals above (#13 `REVOKE`, #26 `SET (...)`, #10 `CREATE OR REPLACE FUNCTION`)
follow one rule, and it is worth ruling on the rule rather than on each instance:

> **A statement that overwrites state the migration does not record is COSTLY, not REVERSIBLE.**
> The undo exists in principle and cannot be written from the changeset alone.

This is already how PG013 (`DROP CONSTRAINT`) and PG012 (`RENAME`) are classified, so the
proposal is to name the existing pattern rather than invent one. The awkward case is #14
(`COMMENT ON`), where the principle says COSTLY and I proposed REVERSIBLE on the ground that a
comment is not schema. That is the one place I knowingly departed from it, and it is flagged
rather than smoothed over.

`REVERSIBLE` here continues to mean **the undo is writable from this changeset**, not merely
that the database could theoretically be restored.

---

## What each row costs to implement, once approved

| Group | Work |
| --- | --- |
| #7, #8, #9, #11, and `CREATE EXTENSION` (#6) | **Table row and fixture only.** The parser already produces `KindCreateSchema`, `KindCreateSequence`, `KindCreateFunction`, `KindCreateTrigger`, `KindCreateExtension`; `classify` simply has no `case` for them, so they fall through to PG027. These five are the cheapest coverage in the list. |
| Everything else | A new `Kind`, a `pgquery.go` conversion, a table row, and a fixture pair. |
| D1, D2 | Reading `ViewStmt.Replace`, splitting `OBJECT_MATVIEW` out of `KindDropView`, two rows, two fixture pairs. Also a correction to the PG016 rationale text, which currently states something false. |

---

## What this proposal does not do

- **It does not weaken the fail-closed default.** Every row above moves a construct from
  "unrecognised, therefore UNKNOWN" to a stated classification. PG027 keeps its meaning and its
  severity, and the UNKNOWN rate falls only because the table covers more, which was the
  instruction.
- **It does not touch grade assembly, caps, or scoring.** No row proposes a new cap.
- **It does not reserve rule IDs.** PG028–PG057 are placeholders. Approved rows should be
  numbered in whatever order you approve them, since a retired or rejected ID is never reused.
- **It does not rank by corpus frequency**, for the reason at the top. The tiers are judgment.

---

## The SELECT investigation — findings, no classification proposed

You asked me to investigate before classifying, on the grounds that a bare `SELECT` in a
migration is either a health check or your file-splitting heuristic cutting a statement in half.
**Do not classify `SELECT` as a category.** Three findings, in order of how much they matter.

### 1. `SELECT` is a node type, not a semantic category

Measured, current binary:

| Statement | Today | What it actually does |
| --- | --- | --- |
| `SELECT 1;` | F UNKNOWN | nothing |
| `SELECT count(*) FROM orders;` | F UNKNOWN | nothing |
| `SELECT pg_advisory_lock(42);` | F UNKNOWN | **takes a lock** |
| `SELECT setval('orders_id_seq', 1000);` | F UNKNOWN | **resets a sequence — the PG010 hazard, IRREVERSIBLE** |
| `WITH d AS (DELETE FROM orders WHERE id < 5 RETURNING *) SELECT count(*) FROM d;` | F UNKNOWN | **deletes rows** |

The last two are the reason this cannot be one rule. A data-modifying CTE is a `DELETE` the
parser reports as a `SelectStmt`, and `setval` is PG010 wearing a function call. **Classifying
"SELECT" as REVERSIBLE would make both of those reversible**, which is the wrong-safe verdict
this engine exists to prevent — and it would be introduced by a rule that looks obviously
harmless. They fail closed today by luck rather than by design.

If you want the harmless case covered, it has to be narrower than the node type: a `SelectStmt`
with no CTE, no locking clause, and no call to a known side-effecting function. That is a real
piece of analysis, not a table row, and I would not write it without a ruling.

### 2. There is no file-splitting heuristic in this repository

I checked, because the hypothesis was worth eliminating before anything else. `PgQuery.Parse`
hands the **whole file** to `pg_query.Parse` and iterates `result.Stmts` — the splitting is done
by the real PostgreSQL grammar, not by a heuristic here. `classifyPath` only pairs up/down files
by name; it never splits their contents.

So a `SELECT` this engine reports is a real statement in the file as written. If your corpus
shows cut statements, the cut happened in the harness, upstream of this engine.

### 3. A cut statement has a specific, detectable signature

This is the useful part. **The two paths to PG027 already print different rationales**, and a
split statement produces a distinctive pair:

```console
# migrations/0001_cut.up.sql   ← the head of "CREATE TABLE archive AS SELECT * FROM orders;"
PG027 — This migration could not be parsed ... parse: syntax error at end of input.

# migrations/0002_tail.up.sql  ← the tail of the same statement
PG027 — this statement matches no rule in the PostgreSQL classification table
```

**`syntax error at end of input` is the signature.** A statement cut in half leaves a head that
is valid SQL as far as it goes and then simply stops — which is exactly the error the grammar
reports — paired with a tail that parses cleanly and classifies as nothing. A genuine typo
produces `syntax error at or near "<token>"` instead.

To test the hypothesis against the corpus in one pass:

```console
$ scripts/probe-dialect.sh --corpus path/to/corpus
```

It lists every file reaching PG027. Any file whose rationale says **at end of input** is a
truncation candidate; grep those and check whether the adjacent file starts mid-statement. If
that set is non-empty, it is a harness bug and it outranks this whole document, exactly as you
said.

I could not run this: the corpus is not on this machine.

---

## Open questions for the ruling

1. ~~**#12 `GRANT` vs #13 `REVOKE`**~~ **RULED: both REVERSIBLE.** Shipped as PG032, with the
   rationale noting on every finding that the engine does not verify the reverse is present.
2. ~~**#14 `COMMENT ON`**~~ **RULED: REVERSIBLE.** Shipped as PG033. The overwrite principle
   stops short of it deliberately; it is saved for PG028, where it bites.
3. **#24 `DISABLE ROW LEVEL SECURITY`** — still open. COSTLY by the data-loss test, and arguably
   worse than COSTLY by any security reading. This engine measures reversibility, not security
   posture, so I stayed with COSTLY; confirm that is the boundary you want.
4. **#30 `INSERT ... ON CONFLICT DO UPDATE`** — still open. Confirm it routes to PG009 rather
   than to the new INSERT row.
5. ~~**D1 and D2**~~ **RULED: handled as defects, ahead of the list.** Shipped as PG028 and
   PG029, with a build-time guard added so the D2 shape cannot recur.

Two new ones, raised by the work rather than by the ruling:

6. **`SELECT` — see the investigation above.** No classification proposed. The narrow rule that
   would be safe (a `SelectStmt` with no CTE, no locking clause, no side-effecting function
   call) is analysis rather than a table row, and I would want that scoped explicitly.
7. **PG029's lock hazard was mine, not yours.** You ruled the verdict COSTLY and did not name a
   lock; I chose EXCLUSIVE, on the grounds that `DROP MATERIALIZED VIEW` takes `ACCESS
   EXCLUSIVE` on an object that queries read, which is the same treatment PG014 gives a
   non-concurrent `DROP INDEX`. PG016's SHORT would be the alternative. Confirm or correct.
