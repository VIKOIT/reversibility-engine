## Reversibility Certificate — Grade B

Reversible at a cost. Rolling back is possible but slow, disruptive, or only safe within a window.

| | |
| --- | --- |
| **Grade** | B |
| **AI merge gate** | ❌ FAIL |
| **Findings** | 2 |

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🟡 | `PG012` | migrations/0001_rename_orders.up.sql:1 | COSTLY | SHORT | `ALTER TABLE orders RENAME TO purchase_orders` |
| 🟡 | `PG012` | migrations/0001_rename_orders.up.sql:2 | COSTLY | SHORT | `ALTER TABLE customers RENAME COLUMN name TO full_name` |

<details>
<summary>Why each finding was classified this way</summary>

- **PG012** at migrations/0001_rename_orders.up.sql:1 — Renaming table orders to purchase_orders breaks the previous application version, so rolling the code back fails for as long as the schema stays renamed.
- **PG012** at migrations/0001_rename_orders.up.sql:2 — Renaming column customers.name to full_name breaks the previous application version, so rolling the code back fails for as long as the schema stays renamed.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
ALTER TABLE customers RENAME COLUMN full_name TO name;
ALTER TABLE purchase_orders RENAME TO orders;
```

### Down migrations

| Migration | Exists | Parses | Symmetric |
| --- | --- | --- | --- |
| `0001_rename_orders` | ✅ | ✅ | ✅ |

_Symmetry is a heuristic and is advisory only; it never affects the grade on its own._

---

<sub>Reversibility Engine · schema 1.2.0 · input digest `160d9f861d205a505ad372b8134720cd1f81f3794078c9e6369981b81e1fd690`</sub>
