# Estimates

Every number this engine derives from a production snapshot is an **estimate**. This document
says exactly how each one is computed, what it assumes, and where it will be wrong — because a
number a user learns to distrust is worse than no number at all, and the only way to earn trust
is to publish the arithmetic.

**A number printed beside a finding never moves that finding. The *band* it falls into can.**
That distinction is the whole of how estimates interact with a grade, and it is worth stating
before any arithmetic:

- The estimated duration itself — `~16m` — is presentation. Nothing reads it back.
- The **band** that duration falls into is scored, and may only make a grade *worse*. See
  [Bands, and what they change](#bands-and-what-they-change) below, and
  [`docs/RULES.md` §3](RULES.md#3-scoring) for where it sits in the scoring procedure.

So an estimate that is wrong by 10% almost never changes a verdict, because it has to cross a
band boundary to change anything at all. An estimate that is wrong by an order of magnitude can
— which is why the fallback in this document errs small, and why the constants err large.

See also [`docs/SPECIFICATION.md` §11g](SPECIFICATION.md) for the design constraint behind all of
it: the engine never connects to anything during analysis.

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

## Bands, and what they change

An estimate is bucketed before it is scored. A band is the most precision the arithmetic
supports, and scoring against a bucket means a 10% error in the estimate almost never changes a
verdict.

| Band | Estimated duration | Effect on the grade |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | cap at B |
| `OUTAGE` | over 5m | cap at C |

A band is computed **only** when the lock hazard is at least `FULL_SCAN` **and** a snapshot
established a size. It may only lower a grade — A → B → C → F — and never raise one.

Two consequences worth stating plainly:

- **A small table changes nothing.** `NEGLIGIBLE` imposes no ceiling. The absence of evidence of
  a problem is not evidence of safety, so a C stays a C.
- **`DISRUPTIVE` rarely moves anything in practice.** Any finding with a `FULL_SCAN` or heavier
  lock is already capped at B by the scoring rules, so `DISRUPTIVE`'s own cap of B is usually
  already satisfied. `OUTAGE` is the band that actually changes a verdict. The cap is implemented
  anyway, so the two rules stay independent of each other.

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

### When `pg_relation_size` is missing — the fallback

```
size_bytes ≈ reltuples × Σ(pg_stats.avg_width across ALL columns of the table)
```

then divided by the same rate as above.

**`avg_width` is per column.** The widths of every column the snapshot knows about are summed to
make a row width. One column's width is not a row width, and using it as one would understate a
fourteen-column table by roughly fourteen times — which would silently drop an `OUTAGE` to
`NEGLIGIBLE`.

`pg_stats` only lists columns that have been analyzed, so the sum can still understate a wide
table. That is the safe direction: a smaller estimate produces a milder band, a milder band
imposes a weaker ceiling, and a weaker ceiling can only leave a grade where it already was.

**If neither a size nor any column width is available, the engine does not guess.** The context
is treated as absent for that finding: no band, no note, and exactly the grade the change would
have had with no snapshot at all.

### No estimate at all — `PG014`, `PG015`

Dropping an index is fast regardless of size. What matters is the lock and the rebuild cost, so
the index's size and scan count are reported and no duration is invented.

This is why a rate is defined for `TABLE_REWRITE` and `FULL_SCAN` and for nothing else. `PG014`
takes an `EXCLUSIVE` lock, which is more severe than `FULL_SCAN` and therefore passes the band
gate — but an index drop is not slower for being large, and applying a scan rate to it would
report a two-gigabyte index as an `OUTAGE` and cap the grade at C for an operation that finishes
in milliseconds. A band exists only where duration genuinely scales with size.

### No estimate at all — `PG017` when it will fail

When a snapshot proves the column contains nulls, `SET NOT NULL` aborts. The verdict becomes
`WILL_FAIL` and no duration is computed: the statement never holds a lock for any length of time,
and a number beside "this will not run" would be noise dressed as precision.

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
