# For the harness Phase 5 bypass catalogue — renaming the migration directory

**STATUS: FILED FOR THE OWNER TO ENTER IN THE HARNESS REPO.** Nothing here touches the harness;
this is the write-up handed over, per the instruction not to reach into that repository.

**Class:** gate evasion, available to anyone who reads the documentation.
**Severity:** the check it evades is the one added specifically to close the Django P0.
**Present in:** the current binary, and in every build since strict coverage landed.

---

## The bypass

Strict coverage fails a changeset when any file in a **migration directory** went unread. A
directory qualifies two ways — it is named for migrations (`migrations/`, `migration/`,
`db/migrate/`), or it holds at least one file an analyzer claimed.

Those two clauses are not enforced equally, and the weaker one is the one an attacker picks.

```console
$ revctl check ./db/migrate     # 0001_idx.up.sql, 0001_idx.down.sql, notes.txt
Cannot guarantee reversibility. Unanalyzed files found in migration directories.
exit 2

$ git mv db/migrate db/sql && revctl check ./db/sql    # byte-identical contents
grade A · coverage FULL · aiGateStatus PASS · exit 0
```

**Renaming the directory turns the check off.** The contents are identical; `notes.txt` is
neither read nor reported, and coverage is FULL because the engine was never shown a file it
would have had to count.

## Why it happens

Not a missing case in the coverage logic — the engine handles both clauses correctly. The file
never reaches the engine.

`FileProvider` decides what to read through `Include(path string) bool`, a **per-path** predicate
evaluated before any other path has been seen. `db/migrate/notes.txt` is admitted because
`db/migrate/` is a migration-named path. `db/sql/notes.txt` is not, because *"this directory also
holds a `.sql` file, so fetch its `notes.txt` too"* is not a decision a per-path predicate can
express.

So the engine's second clause identifies `db/sql/` as a migration directory correctly, and then
has nothing to count, because the provider already discarded it.

## The incentive is backwards

A repository that follows the naming convention gets a **stricter** check than one that does not.
That is the wrong way round for a safety tool, and it is worse than a false negative: it is a
documented, one-command evasion that leaves no trace on the certificate — coverage reads FULL, so
nothing indicates a check was skipped.

## Suggested harness cases

| Case | Expectation |
| --- | --- |
| `db/migrate/` with an unread file | exit 2, coverage PARTIAL |
| Same contents under `db/sql/` | **currently exit 0, coverage FULL — should be exit 2** |
| Same contents under `migrations/` | exit 2 |
| Same contents under `sql/`, `schema/`, `changesets/` | should all be exit 2 |
| Directory renamed between two runs | the verdict must not change |

The last is the sharpest regression form: certify a tree, rename only the directory, certify
again, and assert the two certificates agree on coverage. It fails today, it will pass once the
contract is fixed, and it does not depend on knowing which directory names the engine recognises.

## Status of the fix

The root cause is [`docs/SPECIFICATION.md` §16.9](../SPECIFICATION.md), **ruled and scheduled as
its own session**: `FileProvider` becomes two-phase, `List` then `Read`, so enumeration stops
depending on retrieval.

This entry belongs in the catalogue **regardless of when that lands**. The bypass is live now,
and a catalogue that only lists closed bypasses is a changelog.

## Note on the third occurrence

This is the third defect from one root cause, which is what moved the contract fix from "expensive
and probably not worth it" to ruled:

1. **The Django P0** — a docs-only pull request and thirteen unreadable migrations arrived at the
   engine as the same empty file list, because neither was admitted by `Include`.
2. **The coverage denominator** — counting only files an analyzer wanted made the numerator and
   the denominator the same number.
3. **This** — the denominator is right and the provider never supplies it.

Each was found separately and fixed locally. The pattern only became visible on the third.
