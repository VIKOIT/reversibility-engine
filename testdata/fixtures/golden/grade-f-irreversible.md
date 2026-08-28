## Reversibility Certificate — Grade F

**Not reversible.** Rolling this back would lose data, the engine could not determine what the change does, or the change will not apply at all.

| | |
| --- | --- |
| **Grade** | F |
| **AI merge gate** | ❌ FAIL |
| **Findings** | 1 |

**Why this grade**

- graded F by 1 blocking finding(s), listed above

### Blockers

This change cannot be merged by an autonomous agent. Each item below is a reason on its own.

- PG001 at migrations/0001_drop_legacy_orders.up.sql:1: irreversible — Dropping table legacy_orders destroys every row it holds; recreating the table restores the shape but not the data.

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🔴 | `PG001` | migrations/0001_drop_legacy_orders.up.sql:1 | IRREVERSIBLE | EXCLUSIVE | `DROP TABLE legacy_orders` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG001** at migrations/0001_drop_legacy_orders.up.sql:1 — Dropping table legacy_orders destroys every row it holds; recreating the table restores the shape but not the data.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
-- NO COMPLETE UNDO EXISTS. This changeset cannot be fully reversed.
-- The following changes have no reverse:
--   PG001 at migrations/0001_drop_legacy_orders.up.sql:1: DROP TABLE legacy_orders — cannot be undone
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_drop_legacy_orders` | ✅ | ✅ | ✅ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

---

<sub>Reversibility Engine · schema 1.5.0 · input digest `7404433e87050328312a537aed5f48e2f8e5f8fef4d3c185ed21f03904be2169`</sub>
