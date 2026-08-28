## Reversibility Certificate — Grade F

**Not assessed.** Part of this change was not analyzed, so its reversibility could not be measured. **This is not a finding about your change** — it is the engine saying it could not read all of it.

| | |
| --- | --- |
| **Grade** | F |
| **AI merge gate** | ❌ FAIL |
| **Coverage** | ⚠️ PARTIAL — 1 file not analyzed |
| **Findings** | 1 |

**Why this grade**

- graded F: coverage is PARTIAL — 1 file(s) in migration directories were not analyzed

### Not assessed

The engine read part of this change and not the rest, so it cannot certify any of it. Nothing below is a defect in your change.

- Cannot guarantee reversibility. Unanalyzed files found in migration directories. Remove them or explicitly ignore them in the config.
- not analyzed: db/migrate/0002_backfill_status.rb

### Not analyzed

This engine could not read the following files. **They are not part of the grade above** — neither for it nor against it. The grade describes what was read; this list is what was not.

| File | Why |
| --- | --- |
| `db/migrate/0002_backfill_status.rb` | no analyzer reads .rb migrations |

An autonomous agent will not merge this change: the AI merge gate requires full coverage. A human reviewer can see exactly what was skipped and decide.

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🟢 | `PG024` | db/migrate/0001_add_index.up.sql:1 | REVERSIBLE | NONE | `CREATE INDEX CONCURRENTLY idx ON orders (status)` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG024** at db/migrate/0001_add_index.up.sql:1 — Index idx is built without blocking writes and is removed again by a single concurrent drop.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
DROP INDEX CONCURRENTLY idx;
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_add_index` | ✅ | ✅ | ✅ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

---

<sub>Reversibility Engine · schema 1.5.0 · input digest `d43eb2b67d6b3e10502f57e99a7c761c3011baad52059b135e1cf0f80e71f760`</sub>
