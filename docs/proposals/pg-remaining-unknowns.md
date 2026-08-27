# Proposal — the ten constructs still reaching PG027

**STATUS: PROPOSED. NOT AUTHORITATIVE. NOT IMPLEMENTED.**

Nothing here grades anything. [`docs/RULES.md`](../RULES.md) is the authoritative table. No code,
fixture, or rule ID has been created; the IDs below are **provisional** and are not reserved
until approved.

This is the follow-on to [`pg-dialect-triage.md`](pg-dialect-triage.md), which is now shipped as
PG028–PG059. These are the constructs that document never proposed, and they are the complete
remainder of what `scripts/probe-dialect.sh` still reports as `PG027`/UNKNOWN.

`Today` is measured against the current binary.

---

## 1. `CREATE TABLE ... AS SELECT` — the one that matters

**Today:** `PG027` UNKNOWN, grade F.
**Proposed:** **PG060** — `WITH DATA` (the default): **COSTLY / FULL_SCAN**, with an undo step
that is *not* a bare `DROP TABLE`. **PG061** — `WITH NO DATA`: **REVERSIBLE / NONE**, undo
`DROP TABLE`.

### Why the obvious answer is wrong

The obvious answer is PG025's. `CREATE TABLE` is REVERSIBLE with undo `DROP TABLE`, the table did
not exist before, and dropping it restores the prior schema **exactly**. By the strict
reversibility test — *does the undo return the database to its prior state?* — CTAS passes. It
would be graded **A**, and the undo plan would read:

```sql
DROP TABLE orders_backup;
```

Now look at what that statement is, in the context CTAS actually appears in. In a migration,
`CREATE TABLE ... AS SELECT` is overwhelmingly **a backup taken immediately before something
destructive**:

```sql
CREATE TABLE orders_backup AS SELECT * FROM orders;
DELETE FROM orders WHERE created_at < '2020-01-01';
```

The rows in `orders_backup` are the only surviving copy of what the `DELETE` removed. They are
**the recovery capability the rollback depends on** — and the generated undo plan's instruction
is to destroy them.

### Why the ordering does not save it

The undo plan is emitted in reverse order of application, so in the changeset above the plan
would be *[undo the DELETE]* then *[drop the backup]* — and since the `DELETE` is IRREVERSIBLE,
[`docs/RULES.md` §3](../RULES.md#undoplan) replaces the whole plan with the no-complete-undo
statement. The dangerous step is never printed. That is real, and it is not enough:

**the backup is very often taken in its own migration file.** `0031_backup_orders.sql` then
`0032_purge_old_orders.sql` is the normal shape, because operators want the backup committed
before the destructive step runs. Certify `0031` alone and there is no IRREVERSIBLE finding to
trigger the replacement. Grade **A**, gate **PASS**, undo plan `DROP TABLE orders_backup;` — and
`0032` has already run in production.

This is the D1 failure exactly: **an undo step that is a correct schema inverse and a destructive
operation to execute.**

### The verdict, and why COSTLY rather than REVERSIBLE

Two arguments, and they reach the same place from different directions:

1. **The mirror of PG029.** `DROP MATERIALIZED VIEW` is COSTLY because its rows are derived, a
   rebuild is possible, and the rebuild *will not reproduce the old contents if the sources have
   moved*. A CTAS table holds exactly the same kind of thing: a point-in-time snapshot of a query.
   Undoing the CTAS destroys that snapshot, and re-running the `SELECT` later produces whatever
   the sources say **now**. If the sources have not moved the snapshot was pointless; if they
   have, it is irreplaceable. Either way it is not a clean reversal.

2. **The third clause of the discriminator.** *Destroys a recovery capability that a future
   rollback would depend on.* That is what dropping a backup table is, and it is the same clause
   PG052 and TF004 fire on. It argues for IRREVERSIBLE rather than COSTLY — but the third clause
   describes what the **undo** does, not what the change does, and the change itself takes
   nothing away. **COSTLY is the honest reading**: the change is undoable, and the undo needs a
   human to confirm one thing first.

**The undo step must therefore not be a bare `DROP TABLE`.** Proposed wording, following PG028:

```sql
-- Dropping orders_backup destroys the rows this migration captured. They are a
-- point-in-time snapshot: re-running the SELECT will produce whatever the sources
-- say now, which is not the same thing.
-- Confirm nothing still depends on this table -- a later migration in this series
-- may have been rolled back using it -- and then:
-- DROP TABLE orders_backup;
```

The `DROP` is commented out deliberately. An operator pasting the plan under pressure gets the
warning and not the deletion.

### `WITH NO DATA` is a separate row and genuinely reversible

`CREATE TABLE x AS SELECT ... WITH NO DATA` creates the structure and copies nothing. There is no
snapshot to lose and no scan of the sources. **REVERSIBLE / NONE**, undo `DROP TABLE x;` — the
same split as PG045/PG046 for materialized views, for the same reason.

### Lock

`FULL_SCAN` for the `WITH DATA` form. It reads every row the `SELECT` selects, holding
`ACCESS SHARE` on the sources for the duration — it does not block writers, but the scan is real
and can be long. Consistent with PG045.

---

## 2. Identity columns — `DROP IDENTITY` is PG010 in disguise

| ID | Construct | Today | → Reversibility | → Lock |
| --- | --- | --- | --- | --- |
| PG062 | `ALTER COLUMN ... ADD GENERATED ... AS IDENTITY` | **F** UNKNOWN | COSTLY | SHORT |
| PG063 | `ALTER COLUMN ... DROP IDENTITY` | **F** UNKNOWN | **IRREVERSIBLE** | SHORT |

**PG063 is the finding here.** `DROP IDENTITY` does not merely detach a property: it **drops the
underlying sequence**, and with it the sequence's current position. Re-adding
`GENERATED ALWAYS AS IDENTITY` creates a *new* sequence starting at 1, and the next insert
collides with every existing key.

That is PG010's rationale word for word — *the prior position is recorded nowhere, so it cannot
be restored, and subsequent inserts will collide with existing keys* — reached through a
different statement. It is the classify-by-effect invariant applied again: the node says
`AT_DropIdentity`, the effect is `ALTER SEQUENCE ... RESTART` with no way back.

**PG062 is COSTLY, not REVERSIBLE**, and by PG012's reasoning rather than PG010's. `GENERATED
ALWAYS` **rejects** user-supplied values, so the previous application version — which inserts
explicit IDs — starts failing the moment this applies, and keeps failing until the code is rolled
forward. Same shape as a rename: reversible on the schema, broken for the deployed code in the
meantime. `GENERATED BY DEFAULT` does not have this problem and is arguably REVERSIBLE; I propose
grading both COSTLY rather than splitting, and flag it.

---

## 3. Roles

| ID | Construct | Today | → Reversibility | → Lock |
| --- | --- | --- | --- | --- |
| PG064 | `CREATE ROLE` | **F** UNKNOWN | REVERSIBLE | NONE |
| PG065 | `DROP ROLE` | **F** UNKNOWN | COSTLY | NONE |

Lock `NONE` for both: roles are cluster-wide catalog entries and no table lock is taken.

**The asymmetry with PG032 is the interesting part and it is deliberate.** I proposed, and you
ruled, that `GRANT`/`REVOKE` are both REVERSIBLE because *the opposite statement restores them
exactly*. `DROP ROLE` fails that test: `CREATE ROLE` does **not** restore the role's grants,
memberships, or owned objects, because those are separate catalog entries destroyed alongside it.
The inverse statement exists and is not an inverse. That is the overwrite principle, and it is
what separates PG065 from PG032 rather than a difference in how destructive the two feel.

Worth noting in the rationale: `DROP ROLE` **fails outright** if the role owns anything, so in
practice it is frequently a statement that cannot run at all. That is not a `WILL_FAIL` — the
engine has no evidence either way without a snapshot — but it is worth telling the reader.

---

## 4. Ownership and dependency

| ID | Construct | Today | → Reversibility | → Lock |
| --- | --- | --- | --- | --- |
| PG066 | `ALTER TABLE ... OWNER TO` | **F** UNKNOWN | COSTLY | SHORT |
| PG067 | `ALTER SEQUENCE ... OWNED BY` | **F** UNKNOWN | COSTLY | SHORT |

Both are the overwrite principle: the previous owner and the previous ownership link are not in
the changeset, so the undo cannot be written from it.

**PG067 carries a hazard worth naming in its rationale.** `OWNED BY` ties the sequence's lifetime
to a column: once set, dropping that column **drops the sequence too**. It creates a cascade that
did not exist, and the cascade fires in a later migration that will look innocent — a
`DROP COLUMN` graded by PG002 that also silently destroys a sequence. The engine cannot connect
those across changesets today; saying so on the finding is the most it can honestly do.

---

## 5. Maintenance

| ID | Construct | Today | → Reversibility | → Lock |
| --- | --- | --- | --- | --- |
| PG068 | `REINDEX` | **F** UNKNOWN | REVERSIBLE | EXCLUSIVE (`CONCURRENTLY`: NONE) |
| PG069 | `CLUSTER` | **F** UNKNOWN | REVERSIBLE | EXCLUSIVE |
| — | `SET STATISTICS` | **F** UNKNOWN | **extend PG054** | SHORT |

**`REINDEX` destroys nothing** — an index is derived from the table — so the lock is the whole
risk, and `CONCURRENTLY` is the entire difference between blocking writes and not.

**`SET STATISTICS` should extend PG054 rather than take a new ID.** PG054 is
`ALTER TABLE ... SET (...)`/`RESET (...)`, and a column statistics target is the same shape with
the same verdict: a planner setting whose previous value the changeset does not record. Extending
an existing rule needs the **row text updated to name the new construct** — that is exactly the
D2 failure otherwise, and `TestEveryClassificationHasATableRow` does not catch it, because the
rule ID is already in the table. Flagging that: the guard checks IDs, not constructs.

### `CLUSTER` is where I want a ruling, not a rubber stamp

I propose **REVERSIBLE**, and strict application of the overwrite principle says **COSTLY**.

`CLUSTER t USING i` physically reorders the table — harmless, row order is not data — and also
sets `indisclustered` in the catalog, overwriting whichever index was previously marked. That
previous marking is not in the changeset. By the letter of the principle, that is an overwrite
and the verdict is COSTLY.

I think that is the principle applied too literally, and the refinement is worth ruling on:

> The overwrite principle should govern state a rollback would depend on, not every catalog byte
> a statement touches.

`indisclustered` affects only what a future bare `CLUSTER t` would choose. Nothing depends on it;
no rollback is impaired by its loss. Grading a `CLUSTER` **COSTLY** on that basis would put it in
the same band as dropping a constraint, which is not a distinction worth having.

If you accept the refinement, PG069 is REVERSIBLE and the refinement goes in the spec beside the
principle. If you reject it, PG069 is COSTLY and the principle stays absolute — which is also
defensible, and simpler to apply consistently.

---

## Summary

| Provisional ID | Construct | → Reversibility | → Lock |
| --- | --- | --- | --- |
| PG060 | `CREATE TABLE ... AS SELECT` (`WITH DATA`) | COSTLY | FULL_SCAN |
| PG061 | `CREATE TABLE ... AS SELECT ... WITH NO DATA` | REVERSIBLE | NONE |
| PG062 | `ADD GENERATED ... AS IDENTITY` | COSTLY | SHORT |
| PG063 | `DROP IDENTITY` | **IRREVERSIBLE** | SHORT |
| PG064 | `CREATE ROLE` | REVERSIBLE | NONE |
| PG065 | `DROP ROLE` | COSTLY | NONE |
| PG066 | `ALTER TABLE ... OWNER TO` | COSTLY | SHORT |
| PG067 | `ALTER SEQUENCE ... OWNED BY` | COSTLY | SHORT |
| PG068 | `REINDEX` | REVERSIBLE | EXCLUSIVE / NONE |
| PG069 | `CLUSTER` | REVERSIBLE *(see above)* | EXCLUSIVE |
| *(extend PG054)* | `ALTER COLUMN ... SET STATISTICS` | COSTLY | SHORT |

Implementation cost, since every node type was checked against the parser:

| Group | Work |
| --- | --- |
| PG060, PG061 | `CreateTableAsStmt` already reaches the parser and is deliberately left `KindUnrecognized` for `OBJECT_TABLE`; the branch exists and returns early. Two kinds, two rows, two fixtures. |
| PG062, PG063, PG066, `SET STATISTICS` | `AlterTableStmt` subcommands: `AT_AddIdentity`, `AT_DropIdentity`, `AT_ChangeOwner`, `AT_SetStatistics`. One `case` each in the existing dispatch. |
| PG064, PG065, PG068, PG069 | New top-level nodes: `CreateRoleStmt`, `DropRoleStmt`, `ReindexStmt`, `ClusterStmt`. |
| PG067 | `AlterSeqStmt`, which the parser already handles for `RESTART` and deliberately leaves unrecognised otherwise. One branch. |

## Questions

1. **PG060's undo step** — I propose commenting out the `DROP TABLE` so a pasted plan warns
   rather than deletes. PG028 does the same. Confirm that is the pattern for every undo whose
   correct inverse is destructive to run.
2. **PG062** — `GENERATED BY DEFAULT` does not break the previous application version the way
   `GENERATED ALWAYS` does. Split into two rules, or grade both COSTLY?
3. **PG069 `CLUSTER`** — accept the refinement to the overwrite principle, or apply it
   absolutely and grade COSTLY?
4. **`SET STATISTICS`** — extend PG054, or a new ID? Extending is cleaner; it also means the
   table-row guard cannot catch a future construct silently joining an existing rule.
