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

- K8S006 at crd.yaml: irreversible — Deleting CustomResourceDefinition widgets.acme.io cascades to every custom resource of that kind, which is among the largest blast radii in Kubernetes and is not undone by re-applying the manifest.
- K8S006 at namespace.yaml: irreversible — Deleting Namespace legacy cascades to every object inside it, which is among the largest blast radii in Kubernetes and is not undone by re-applying the manifest.

### Findings

| | Rule | Location | Reversibility | Lock | Change |
| --- | --- | --- | --- | --- | --- |
| 🔴 | `K8S006` | crd.yaml | IRREVERSIBLE | NONE | `CustomResourceDefinition widgets.acme.io` |
| 🔴 | `K8S006` | namespace.yaml | IRREVERSIBLE | NONE | `Namespace legacy` |

<details>
<summary>Why each finding was classified this way</summary>

- **K8S006** at crd.yaml — Deleting CustomResourceDefinition widgets.acme.io cascades to every custom resource of that kind, which is among the largest blast radii in Kubernetes and is not undone by re-applying the manifest.
- **K8S006** at namespace.yaml — Deleting Namespace legacy cascades to every object inside it, which is among the largest blast radii in Kubernetes and is not undone by re-applying the manifest.

</details>

### Undo plan

Steps are in the order they must be run, unwinding the change from the last step applied.

```sql
-- NO COMPLETE UNDO EXISTS. This changeset cannot be fully reversed.
-- The following changes have no reverse:
--   K8S006 at crd.yaml: CustomResourceDefinition widgets.acme.io — cannot be undone
--   K8S006 at namespace.yaml: Namespace legacy — cannot be undone
```

---

<sub>Reversibility Engine · schema 1.5.0 · input digest `e83a6aad8b31bb8fb4ce922e1dddcccb1097283ce17935bf07dc58d96c807c17`</sub>
