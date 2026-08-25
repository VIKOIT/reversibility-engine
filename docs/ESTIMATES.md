# Estimates

Every number this engine derives from a production snapshot is an **estimate**. This document
says exactly how each one is computed, what it assumes, and where it will be wrong — because a
number a user learns to distrust is worse than no number at all, and the only way to earn trust
is to publish the arithmetic.

Nothing here affects a grade. Estimates are printed beside a finding; they never move it. See
[`CLAUDE.md` §11g](../CLAUDE.md).

---

## The two inputs, and why both are approximate

| Input | Source | Why it is approximate |
| --- | --- | --- |
| Row count | `pg_class.reltuples` | Maintained by `ANALYZE` and `VACUUM`, not continuously. It is the planner's estimate and can be stale by any amount, including `-1` for a relation never analyzed. |
| Size | `pg_relation_size`, `pg_total_relation_size` | Exact at the moment of collection, and immediately out of date. It includes dead tuples not yet vacuumed, so it can overstate live data considerably. |

Both are read from a snapshot that was taken at some point in the past. A snapshot older than
seven days is flagged on the certificate; one taken a minute ago is still describing a database
that has since moved on.

---

## The formulas

### Table rewrite — `PG006`, `PG007`

```
duration ≈ pg_total_relation_size / 50 MiB/s
```

An `ALTER COLUMN TYPE` rewrites the entire heap and rebuilds every index on the table, which is
why the estimate uses *total* relation size rather than the main fork alone.

**50 MiB/s** is a deliberately conservative figure for sustained write throughput plus index
build cost on network-attached block storage under production load. A quiet server on local NVMe
will beat it by an order of magnitude.

### Sequential scan — `PG017`, `PG021`

```
duration ≈ pg_relation_size / 200 MiB/s
```

`SET NOT NULL` and constraint validation read the main fork under lock without writing it.

**200 MiB/s** assumes the table is not in cache. A table already in shared buffers is far faster;
a table on cold storage under contention is slower.

### No estimate at all — `PG014`, `PG015`

Dropping an index is fast regardless of size. What matters is the lock and the rebuild cost, so
the index's size and scan count are reported and no duration is invented.

---

## Why the constants are constants

They are not configurable, and that is deliberate. A knob here would let somebody tune the
estimate until it said what they wanted it to say, and a number tuned to be reassuring is worse
than no number. Changing them is a deliberate act, in a commit, with an update to this file.

They err toward pessimism on purpose. An operator who plans for ten minutes and finishes in three
has lost nothing; the opposite mistake is an outage during a maintenance window that was sized
from this tool's output.

---

## How the numbers are printed

| Rule | Effect |
| --- | --- |
| Every duration carries a leading `~` | So it cannot be remembered as a measurement. `TestEstimateAlwaysMarksItselfAsAnEstimate` enforces this for every input. |
| Two significant figures at most | `~14m`, `~5.8h`, never `~14m37s`. |
| Under a second is `~<1s` | Rather than a suspiciously exact number of milliseconds. |
| Row counts are rounded | `212M`, not `212,481,993`. `reltuples` does not support the last six digits. |
| `-1` rows prints as "an unknown number of" | Postgres's marker for a relation that has never been analyzed. |
| A non-zero null fraction never prints as `0%` | It prints `<0.01%`. A column with one null in ten million still fails the migration, and rounding that to zero would be the most misleading thing this tool could print. |

---

## Where these will be wrong

- **A table that has never been `ANALYZE`d** reports `-1` rows. The size is still real.
- **A recently bloated table** overstates: `pg_relation_size` counts dead tuples until a vacuum
  reclaims them.
- **A partitioned table** is collected per partition. A migration naming the parent will not
  resolve to a single relation and gets no estimate rather than a wrong one.
- **An unqualified table name matching two schemas** gets no estimate. The engine does not know
  the `search_path` that will be in effect, and guessing would attach one table's size to another
  table's migration.
- **Any duration on hardware unlike the assumption.** The constants above are a starting point,
  not a measurement of your infrastructure.

The right way to use a lock estimate is as an order of magnitude: "seconds", "minutes", or
"long enough to need a maintenance window". It is not a countdown.
