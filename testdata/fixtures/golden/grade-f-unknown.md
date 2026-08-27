## Reversibility Certificate — Grade F

**Not reversible.** Rolling this back would lose data, the engine could not determine what the change does, or the change will not apply at all.

| | |
| --- | --- |
| **Grade** | F |
| **AI merge gate** | ❌ FAIL |
| **Findings** | 2 |

**Why this grade**

- graded F by 2 blocking finding(s), listed above

### Blockers

This change cannot be merged by an autonomous agent. Each item below is a reason on its own.

- PG027 at migrations/0001_unparseable.up.sql:1: unknown — This migration could not be parsed, so nothing in it can be classified: parse: syntax error at or near "FLARBLE".
- PG027 at migrations/0002_unrecognized.up.sql:1: unknown — this statement matches no rule in the PostgreSQL classification table, and an unclassified change is treated as unsafe

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| ⚫ | `PG027` | migrations/0001_unparseable.up.sql:1 | UNKNOWN | EXCLUSIVE | `ALTER TABLE orders FLARBLE COLUMN quantity;` |
| ⚫ | `PG027` | migrations/0002_unrecognized.up.sql:1 | UNKNOWN | EXCLUSIVE | `CLUSTER orders USING orders_pkey` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG027** at migrations/0001_unparseable.up.sql:1 — This migration could not be parsed, so nothing in it can be classified: parse: syntax error at or near "FLARBLE".
- **PG027** at migrations/0002_unrecognized.up.sql:1 — this statement matches no rule in the PostgreSQL classification table, and an unclassified change is treated as unsafe

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
-- NO COMPLETE UNDO EXISTS. This changeset cannot be fully reversed.
-- The following changes have no reverse:
--   PG027 at migrations/0001_unparseable.up.sql:1: ALTER TABLE orders FLARBLE COLUMN quantity; — was not understood, so no undo can be written for it
--   PG027 at migrations/0002_unrecognized.up.sql:1: CLUSTER orders USING orders_pkey — was not understood, so no undo can be written for it
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_unparseable` | ✅ | ✅ | ❌ |
| `0002_unrecognized` | ✅ | ✅ | ✅ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

<details>
<summary>Down-migration notes</summary>

- `0001_unparseable`: symmetry could not be checked because the up migration does not parse

</details>

---

<sub>Reversibility Engine · schema 1.7.0 · input digest `2e500afeb4e2e473bbb4bcd64396b042dfa2947f19579cba3212072106ccb007`</sub>
