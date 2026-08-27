## Reversibility Certificate — Grade A

Fully reversible. This change can be rolled back with no data loss.

| | |
| --- | --- |
| **Grade** | A |
| **AI merge gate** | ❌ FAIL |
| **Coverage** | ⚠️ PARTIAL — 1 file not analyzed |
| **Findings** | 1 |

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

<sub>Reversibility Engine · schema 1.6.0 · input digest `d43eb2b67d6b3e10502f57e99a7c761c3011baad52059b135e1cf0f80e71f760`</sub>
