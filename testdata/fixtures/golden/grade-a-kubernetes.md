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
| 🟢 | `K8S015` | deployment.yaml | REVERSIBLE | NONE | `Deployment web/api` |

<details>
<summary>Why each finding was classified this way</summary>

- **K8S015** at deployment.yaml — Container "api" in Deployment web/api moves to a digest-pinned image, so the previous digest identifies the exact bytes to roll back to.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
kubectl set image Deployment/api api=ghcr.io/acme/api@sha256:3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea
```

---

<sub>Reversibility Engine · schema 1.1.0 · input digest `b10cfa10960f1f66198e1ea86f4a7555ae112d13269f365cbf63ca311e4eeb96`</sub>
