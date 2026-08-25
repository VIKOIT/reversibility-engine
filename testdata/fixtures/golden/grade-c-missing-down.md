## Reversibility Certificate — Grade C

Reversible with significant caveats. Review the findings before merging.

| | |
| --- | --- |
| **Grade** | C |
| **AI merge gate** | ❌ FAIL |
| **Findings** | 1 |

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🟢 | `PG020` | migrations/0001_add_notes.up.sql:1 | REVERSIBLE | NONE | `ALTER TABLE orders ADD COLUMN notes text` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG020** at migrations/0001_add_notes.up.sql:1 — Adding orders.notes is the safest schema change available: a constant default is stored in the catalog rather than written to every row.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
ALTER TABLE orders DROP COLUMN notes;
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_add_notes` | ❌ | ❌ | ❌ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

<details>
<summary>Down-migration notes</summary>

- `0001_add_notes`: no down migration accompanies this up migration

</details>

---

<sub>Reversibility Engine · schema 1.3.0 · input digest `900c272e7e264d22031f0b8b00deb44425ebe2f9290670f11c9067a69677ba12`</sub>
