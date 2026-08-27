## Reversibility Certificate — Grade A

Fully reversible. This change can be rolled back with no data loss.

| | |
| --- | --- |
| **Grade** | A |
| **AI merge gate** | ✅ PASS |
| **Findings** | 1 |

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🟢 | `PG024` | migrations/0001_index_status.up.sql:1 | REVERSIBLE | NONE | `CREATE INDEX CONCURRENTLY idx_orders_status ON orders (status)` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG024** at migrations/0001_index_status.up.sql:1 — Index idx_orders_status is built without blocking writes and is removed again by a single concurrent drop.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
DROP INDEX CONCURRENTLY idx_orders_status;
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_index_status` | ✅ | ✅ | ✅ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

---

<sub>Reversibility Engine · schema 1.6.0 · input digest `e497c2c4438058df95ab087614e62b35563e249aca35e841fe285dae85a8e561`</sub>
