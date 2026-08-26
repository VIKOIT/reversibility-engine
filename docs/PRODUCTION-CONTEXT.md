# Production context

The engine classifies `ALTER COLUMN TYPE` as a table rewrite. Without knowing your database it
cannot say whether that is 200 milliseconds or 14 minutes. Production context closes that gap.

```
before:  PG006  ALTER COLUMN TYPE — TABLE_REWRITE, COSTLY
after:   PG006  ALTER COLUMN TYPE — full rewrite of 212M rows
                (~48 GB), est. ~16m ACCESS EXCLUSIVE lock
                band OUTAGE — grade capped at C
```

That last line is the part to read carefully. **Context can make a verdict worse. It can never
make one better.** See [What context changes, and what it never
changes](#what-context-changes-and-what-it-never-changes).

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

One command collects one thing. `--dsn` together with `--kube-context` is an error naming the
fix, rather than a run that quietly collects half of what you asked for.

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--dsn` | PostgreSQL | Connection string. Prefer a read replica, and prefer `$PGPASSWORD` or a `.pgpass` file over putting a password on the command line. |
| `--kube-context` | Kubernetes | kubeconfig context to collect from. Pass an empty `--kube-context=` to use the current one. |
| `--kubeconfig` | Kubernetes | Explicit path. Defaults to `$KUBECONFIG`, then `~/.kube/config`, then in-cluster credentials. |
| `--environment` | both | A label recorded in the file, such as `prod`. It appears in the message when two snapshots disagree about their source. |
| `--out`, `-o` | both | Required. A snapshot is a file; the analysis reads it later. |

Then, in CI:

```bash
revctl check ./migrations \
  --context .reversibility/pg.json \
  --context .reversibility/k8s.json
```

`--context` is repeatable and merges by kind.

**A path that does not exist is skipped, not an error** — context is an enhancement, and a
workflow that passes `--context` unconditionally has to keep working before the first snapshot is
ever collected. **A file that exists and cannot be read is exit 2**, and so is one that cannot be
decoded. The two are deliberately different: "you have not collected this yet" is an ordinary
state, while "this file is here and I cannot understand it" means the run does not know what it
was meant to enforce.

### On a schedule

The snapshot changes slowly. Daily is plenty:

```yaml
name: Refresh production context
on:
  schedule: [{cron: '0 4 * * *'}]

permissions:
  contents: write            # the job commits the refreshed snapshot

jobs:
  snapshot:
    runs-on: ubuntu-latest
    env:
      VERSION: v1.1.2        # pin it; this job runs unattended
    steps:
      - uses: actions/checkout@v4

      # Fetch revctl directly. Do NOT use the action here: it runs a full
      # certification rather than installing anything, and it never puts revctl
      # on PATH.
      - name: Install revctl
        run: |
          set -euo pipefail
          base="https://github.com/VIKOIT/reversibility-engine/releases/download/$VERSION"
          curl -fsSLO "$base/revctl_linux_amd64.tar.gz"
          curl -fsSLO "$base/checksums.txt"
          sha256sum --check --ignore-missing checksums.txt
          tar -xzf revctl_linux_amd64.tar.gz revctl
          sudo install revctl /usr/local/bin/

      - run: revctl snapshot --dsn "$REPLICA_DSN" --environment prod --out .reversibility/pg.json
        env:
          REPLICA_DSN: ${{ secrets.REPLICA_DSN }}

      - run: |
          git config user.name  'reversibility-bot'
          git config user.email 'bot@example.invalid'
          git add .reversibility/pg.json
          git diff --cached --quiet || git commit -m 'chore: refresh production context'
          git push
```

**The binary is downloaded and checksum-verified rather than obtained from the action, and that
is not a stylistic choice.** `VIKOIT/reversibility-engine@v1` is a composite action that grades a
changeset; running it does not install anything you can call afterwards. It never writes to
`$GITHUB_PATH`, so a later `run: revctl …` would fail with *command not found* — and before
reaching that step, the action would have run a certification of its own and, at the default
`gate: A`, could fail the job outright. Collecting a snapshot and gating a pull request are two
different jobs.

Committing the snapshot is the intended workflow: it is metadata, it contains no secret, and
having it in the repository is what lets a pull request be graded against production without the
pull request's CI holding any credential.

---

## What context changes, and what it never changes

**Context is a one-way ratchet. It may make a verdict worse; it may never make one better.**

The vocabulary, because it has been ambiguous in this project's own documents: *lowering* a grade
means making it **worse** (A → B → C → F) and is permitted. *Raising* one means making it better
and never happens. A property test runs every fixture in the repository with and without a
snapshot deliberately sized to trip every band, and asserts the graded-with-context result is
**never better** than the one without.

Mechanically, the ratchet is two rules in `snapshot.Enrich`:

- A classification whose severity **would drop** is discarded rather than applied. A snapshot
  cannot talk the engine out of a finding.
- The lock hazard is **restored unconditionally**. Context describes how long a lock is held,
  never which lock is taken.

**Exactly two things reach a verdict from a snapshot.** Everything else context produces is prose
and numbers printed beside a finding, which nothing reads back.

### 1. `WILL_FAIL` — the migration will not apply at all

A verdict distinct from `IRREVERSIBLE`, and the distinction matters: one says you cannot undo the
change, the other says it will never happen, so the fix belongs in the migration rather than in
the rollback plan. A reader who confuses them fixes the wrong thing, which is why it is reported
separately in the blockers, the undo plan, SARIF, and Markdown.

It ranks above every verdict an analyzer can produce from source alone and **always grades F**.
Today it is reached from one piece of evidence: `SET NOT NULL` against a column the snapshot
shows contains nulls. Postgres validates every existing row and a single violation aborts the
statement and rolls the transaction back — a certainty rather than a risk. No lock duration is
estimated for it, because the statement never gets far enough to hold one.

**A waiver cannot cover `WILL_FAIL`**, the same as `UNKNOWN`: a waiver accepts a trade-off, and
there is no trade-off in a statement that cannot apply. Waiving it would document a bug rather
than accept one, and the pipeline it unblocked would fail at deploy instead of at review.

### 2. Lock duration bands

With a snapshot, a lock hazard of `FULL_SCAN` or heavier is bucketed by how long it is expected
to be held:

| Band | Estimated duration | Effect on the grade |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | no better than **B** |
| `OUTAGE` | over 5m | no better than **C** |

**A `NEGLIGIBLE` band imposes nothing.** A small table does not turn a C into a B — the absence of
evidence of a problem is not evidence of safety. In practice `OUTAGE` is the band that actually
moves a grade, since any `FULL_SCAN` finding is already capped at B; both caps are implemented so
the two rules stay independent. Every formula behind every duration is in
[`ESTIMATES.md`](ESTIMATES.md).

**A band exists only where duration scales with size.** `PG014` takes an `EXCLUSIVE` lock, which
passes the "at least `FULL_SCAN`" gate, but dropping an index is not slower for being large and
no rate is defined for it. Applying a scan rate there would cap a grade at C for an operation
that finishes in milliseconds.

### What every rule gains

The rules that gain context — and this list is closed, enforced by
`TestOnlySpecifiedRulesGainContext`, which rejects any other:

| Rule | What context adds |
| --- | --- |
| `PG006`, `PG007` | Row count, total size, estimated rewrite duration, **and a band** — the one place a lock estimate routinely moves a grade |
| `PG017` | **Whether the migration will fail.** If `null_frac > 0`, `SET NOT NULL` rejects the statement — said before it runs, not after. The verdict becomes `WILL_FAIL` and the grade **F**. If `null_frac == 0`, a band instead |
| `PG014`, `PG015` | Index size and scan count. Zero scans since the last statistics reset means genuinely cheap to drop, and it says so. **No band and no duration** — an index drop is not slower for being large |
| `PG021` | Row count, estimated scan duration for the validation, and a band |
| `K8S003` | The cluster's actual `reclaimPolicy`, replacing the analyzer's guess |
| `K8S004` | The bound volume's actual capacity, which is what a shrink is measured against |

`K8S003` deserves a note. If the cluster reports `reclaimPolicy: Retain`, deleting the claim
releases the volume rather than erasing it — materially less severe. **The finding stands
anyway.** Recovery is a manual operation, and no snapshot should authorise a tool to grade data
loss as reversible on your behalf. The fact is recorded in the note; a human decides.

---

## Staleness and fingerprints

- A snapshot older than **7 days** produces a warning in `contextWarnings` on the certificate.
  **It is still used.** Silently falling back to no context would make the certificate quietly
  less informative at exactly the moment somebody stopped refreshing it.
- Two snapshots of the **same kind from different sources** are a configuration error. Merging
  two databases into one view would answer questions about a table that exists in one of them,
  with no way to tell which. The message names the offending file, the kind, and **both
  `--environment` labels alongside both fingerprints**:

  ```
  invalid production context: .reversibility/pg-staging.json is a postgres snapshot of a
  different source than the one already loaded ("staging", fingerprint 999999999999;
  expected "prod", fingerprint aaaa1111bbbb); snapshots of two environments cannot be merged
  ```

  The label is what makes that message actionable. The fingerprints are correct and
  unambiguous, and — read at the moment a pipeline broke — interchangeable; `"staging"` is the
  half that says which file to remove. Collect with `--environment` and it appears here. Without
  one, the fingerprint stands alone rather than printing empty quotes.
- A snapshot whose `schemaVersion` this build does not know, or that carries a field it does not
  recognise, is **refused** — `DisallowUnknownFields`, not a best-effort read. Context that is
  wrong is worse than context that is absent, because context is believed. The snapshot file
  format carries its own version, currently `1.0.0`, independent of the certificate schema.
- **A finding the snapshot does not describe simply gets no context.** An unqualified table name
  matching two schemas, a claim name matching two namespaces, a table absent from the file — all
  resolve to nothing rather than to a guess. Context that names the wrong object is worse than
  none.

### Why a re-collection does not change a certificate

**The context digest deliberately excludes `CollectedAt`.** It is the one field that moves on
every collection while the facts it accompanies usually do not, so hashing it would give an
identical database a different digest every night — and a digest that changes on its own stops
being evidence of anything.

The consequence is the useful one: re-running `revctl snapshot` against an unchanged database
produces a file that grades to a byte-identical certificate. A digest that moves means the
production facts moved, which is exactly the signal worth having. This is the same reasoning as
`policyDigest` covering the *resolved* policy rather than the file's bytes, so reformatting it
changes nothing.
