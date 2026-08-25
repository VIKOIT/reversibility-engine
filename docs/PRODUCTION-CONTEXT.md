# Production context

The engine classifies `ALTER COLUMN TYPE` as a table rewrite. Without knowing your database it
cannot say whether that is 200 milliseconds or 14 minutes. Production context closes that gap.

```
before:  PG006  ALTER COLUMN TYPE — TABLE_REWRITE, COSTLY
after:   PG006  ALTER COLUMN TYPE — full rewrite of 212M rows
                (~48 GB), est. ~16m ACCESS EXCLUSIVE lock
```

---

## The design: a file, not a connection

**The engine never connects to a database or a cluster during analysis.** A separate command
collects metadata into a file; `revctl check --context` reads the file. Three reasons, all
binding:

1. **CI never needs production credentials.** Enterprises will not hand a third-party binary a
   production DSN, and this design means they never have to. The snapshot is collected once, by
   whoever already has access, on a schedule.
2. **Determinism survives.** A live query returns different numbers on every run, which would
   destroy the byte-identical certificate the whole product rests on. A file is an input like any
   other, and it is hashed into `inputDigest`.
3. **Analyzers stay pure.** `internal/domain` and the analyzers keep their no-network rule
   intact.

This is enforced, not merely intended. `internal/snapshot/architecture_test.go` fails the build
if `internal/domain`, `internal/analyzer/...`, `internal/engine`, or `internal/snapshot` can
reach `pgx` or `client-go` through any number of hops. The drivers are reachable from exactly one
package: `internal/snapshot/collect`, which only the `snapshot` command calls.

---

## What is collected

**This list is exhaustive.** It is also printed by `revctl snapshot --help`, so it can be checked
without reading this file.

### PostgreSQL — catalog and statistics views only

| Object | Fields | Source |
| --- | --- | --- |
| Tables | schema, name, row estimate, size, total size | `pg_class`, `pg_relation_size`, `pg_total_relation_size` |
| Indexes | schema, table, name, size, scan count, statistics reset time | `pg_stat_user_indexes`, `pg_stat_database` |
| Columns | schema, table, name, null fraction, average width | `pg_stats` |

### Kubernetes — GET and LIST only

| Object | Fields |
| --- | --- |
| StorageClasses | name, `reclaimPolicy` |
| PersistentVolumeClaims | namespace, name, phase, storage class, **bound** capacity |
| Workloads | namespace, kind, name, replica count |

### What is never collected

- **No row of user data.** Not one. There is no `SELECT` against a user table anywhere in the
  collector.
- **No column values** — only statistics *about* columns.
- **No Secret, ConfigMap, or environment variable**, not even their names.
- **No connection string, no hostname, no credential.** The source is identified by a
  `sourceFingerprint`, which is a SHA-256 of the database's own system identifier (or the
  cluster's name and API server URL) — a value you cannot connect with.

This is tested, not asserted. `TestSnapshotContainsNoUserData` seeds a throwaway database with
passwords, API keys, and private-key material, runs the collector, and searches the produced file
for every one of them. CI runs it against a real PostgreSQL service container on every push.

---

## Read-only by construction

The PostgreSQL connection is opened with `default_transaction_read_only=on`. Every statement the
collector issues runs in a read-only transaction, so no future edit to that file — and no bug in
it — can write to the database it was pointed at. `TestCollectorConnectionIsReadOnly` proves a
write fails on a connection configured exactly as the collector's.

The Kubernetes collector issues only `List` calls. There is no create, update, patch, or delete
in the package.

---

## Required grants

### PostgreSQL

`pg_monitor` is sufficient and is the recommended grant:

```sql
CREATE ROLE reversibility_snapshot LOGIN PASSWORD '…';
GRANT pg_monitor TO reversibility_snapshot;
```

`pg_monitor` includes `pg_read_all_stats`, which is what `pg_stats` and `pg_stat_user_indexes`
need. Without it the collector still runs but sees only the tables the role owns.

**Point it at a read replica.** Nothing here writes, but a replica is one less thing to reason
about, and the statistics that matter are replicated.

If your provider revokes `pg_control_system()` — several managed services do — the collector
falls back to `cluster_name` plus the database name for its fingerprint. That is weaker: a
restored copy shares a fingerprint with its original. It is enough for the fingerprint's actual
job, which is catching "these two files are from different places".

### Kubernetes

A role that can list three resource types, and nothing else:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: reversibility-snapshot
rules:
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["list"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses"]
    verbs: ["list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets"]
    verbs: ["list"]
```

---

## Running it

```bash
revctl snapshot --dsn "$REPLICA_DSN"   --out .reversibility/pg.json
revctl snapshot --kube-context prod    --out .reversibility/k8s.json
```

Then, in CI:

```bash
revctl check ./migrations \
  --context .reversibility/pg.json \
  --context .reversibility/k8s.json
```

`--context` is repeatable and merges by kind. **A path that does not exist is skipped, not an
error** — context is an enhancement, and a workflow that passes `--context` unconditionally has
to keep working before the first snapshot is ever collected.

### On a schedule

The snapshot changes slowly. Daily is plenty:

```yaml
name: Refresh production context
on:
  schedule: [{cron: '0 4 * * *'}]
jobs:
  snapshot:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: VIKOIT/reversibility-engine@v2   # for the binary
      - run: revctl snapshot --dsn "$REPLICA_DSN" --out .reversibility/pg.json
        env:
          REPLICA_DSN: ${{ secrets.REPLICA_DSN }}
      - run: |
          git config user.name  'reversibility-bot'
          git config user.email 'bot@example.invalid'
          git add .reversibility/pg.json
          git diff --cached --quiet || git commit -m 'chore: refresh production context'
          git push
```

Committing the snapshot is the intended workflow: it is metadata, it contains no secret, and
having it in the repository is what lets a pull request be graded against production without the
pull request's CI holding any credential.

---

## What context changes, and what it never changes

**It never changes a grade.** Enrichment writes one optional field on a finding and touches
nothing else — not the reversibility, not the lock hazard, not the score. A property test runs
every fixture in the repository twice, with and without a snapshot deliberately sized to make any
size-sensitive rule fire, and asserts the grade, the effective grade, the gate status, and every
individual classification are identical.

The rules that gain context:

| Rule | What context adds |
| --- | --- |
| `PG006`, `PG007` | Row count, total size, estimated rewrite duration |
| `PG017` | **Whether the migration will fail.** If `null_frac > 0`, `SET NOT NULL` rejects the statement — said before it runs, not after |
| `PG014`, `PG015` | Index size and scan count. Zero scans since the last statistics reset means genuinely cheap to drop, and it says so |
| `PG021` | Row count and estimated scan duration for the validation |
| `K8S003` | The cluster's actual `reclaimPolicy`, replacing the analyzer's guess |
| `K8S004` | The bound volume's actual capacity, which is what a shrink is measured against |

`K8S003` deserves a note. If the cluster reports `reclaimPolicy: Retain`, deleting the claim
releases the volume rather than erasing it — materially less severe. **The finding stands
anyway.** Recovery is a manual operation, and no snapshot should authorise a tool to grade data
loss as reversible on your behalf. The fact is recorded in the note; a human decides.

---

## Staleness and fingerprints

- A snapshot older than **7 days** produces a warning on the certificate. It is still used.
  Silently falling back to no context would make the certificate quietly less informative at
  exactly the moment somebody stopped refreshing it.
- Two snapshots of the **same kind from different sources** are a configuration error. Merging
  two databases into one view would answer questions about a table that exists in one of them,
  with no way to tell which.
- A snapshot whose `schemaVersion` this build does not know, or that carries a field it does not
  recognise, is **refused**. Context that is wrong is worse than context that is absent, because
  context is believed.
