## Reversibility Certificate — Grade N/A

**Not assessed.** The engine did not analyze this change, so it is making no claim about whether it can be rolled back.

_This change contains files that may be migrations, and no analyzer in this engine can read them. **Reversibility was not assessed.** Do not merge this on the strength of this certificate._

| | |
| --- | --- |
| **Grade** | N/A |
| **AI merge gate** | ➖ NOT APPLICABLE |
| **Coverage** | ⚠️ PARTIAL — 3 files not analyzed |
| **Findings** | 0 |

### Not assessed

The engine cannot read the following, so it has measured nothing. An autonomous agent must not merge this change.

- found 3 files in django/contrib/auth/migrations that no analyzer supports (.py migrations). Reversibility was not assessed.

### Not analyzed

This engine could not read the following files. **They are not part of the grade above** — neither for it nor against it. The grade describes what was read; this list is what was not.

| File | Why |
| --- | --- |
| `django/contrib/auth/migrations/0001_initial.py` | no analyzer reads .py migrations |
| `django/contrib/auth/migrations/0002_alter_permission.py` | no analyzer reads .py migrations |
| `django/contrib/auth/migrations/0003_alter_email.py` | no analyzer reads .py migrations |

An autonomous agent will not merge this change: the AI merge gate requires full coverage. A human reviewer can see exactly what was skipped and decide.

---

<sub>Reversibility Engine · schema 1.6.0 · input digest `83afa88844d37b2c8f6b498136948560b925a9b1343756c3c2a9e2ce5cb72695`</sub>
