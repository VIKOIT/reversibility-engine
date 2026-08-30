# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Two things carry their own compatibility promise and are called out whenever they
move:

- **The certificate schema** (`pkg/certificate`, `schemaVersion`) — downstream
  merge gates parse it, so a breaking field change bumps that version, not just
  the release version.
- **Rule IDs** (`PG001`, `K8S001`, …) — people suppress, alert on, and dashboard
  against them, so they are never renumbered or reused.

## [Unreleased]

### Changed — the release path is production code

**Three fail-opens have now reached consumers through the delivery path rather than the engine**:
the GHCR `:v1` image that analyzed nothing and exited 0, `--require-full-coverage` becoming a
no-op that still exited 0, and `v1.2.0` publishing over a failing CI. Three is a pattern, and the
pattern is that the engine was exhaustively defended and the thing that ships it was not.

`docs/SPECIFICATION.md` §3 now says so as scope:

> The release path is production code. Every invariant that governs the engine governs the
> workflows, the action, and the image: fail-closed, no silent success, no path that reaches
> "published" without proving what it published.

**Nothing is published over a red CI.** `release.yml` and `publish-image.yml` both require the
tagged commit to have a *completed success* conclusion from the CI workflow. Not "not failed" —
pending, cancelled, skipped and no-run-at-all all mean no, because each is absence of evidence.
**There is no override flag**: a release that must go out over a red CI is a human deciding to
make CI green first. It costs one API call and no build time, and it runs both before the build
matrix — so a red CI costs seconds, not thirty minutes — and again immediately before publishing.

### Fixed

**The image was pushed before it was verified.** `publish-image.yml` published to GHCR and *then*
checked whether the image still classified SQL and whether its no-argument run exits non-zero.
Those are the two checks written specifically to prevent the `:v1` incident, and they could not
prevent anything: a failure turned the workflow red and left the broken image published, and a red
workflow is not visible to somebody running `docker pull`.

> An artifact that is visible before it is verified has been published. The verification is then a
> report, not a gate.

The image is now built, loaded, verified, and only then pushed.

**The major tag could move backwards.** `@v1` is documented as the newest non-prerelease `v1.x`,
and nothing enforced it — the job force-updated the ref to whatever was being released. Tagging a
backport, or re-cutting an older version, would have moved every `@v1` consumer **down** to an
engine without whatever they upgraded for, silently, on a green run. It now refuses to go
backwards; equal is still allowed, so re-running a release stays idempotent.

Nobody triggered either of these. They are fixed because a fail-open that needs a particular tag
to be pushed is still a fail-open, and this project has now shipped three of them.

## [v1.2.1] - 2026-08-29

> **If you ran `v1.2.0` with `--terraform-plan` on Linux or macOS outside a checkout, the flag
> never matched: the plan you named went unanalyzed, and the run could report success without
> assessing anything. Those green builds were not assessments. Re-run them on `v1.2.1`.**

**A fail-open in `v1.2.0`, on POSIX only, found by CI twenty minutes after the tag.**

### Fixed

**`--terraform-plan` did not claim the file it named, on Linux and macOS, when the analyzed tree
was not inside a project.** The plan went unanalyzed; if it was the only thing in the changeset
the run reported `NO_CANDIDATES`, graded **N/A**, and **exited 0 under `--gate`**. A destructive
plan under an unconventional name passed a gate that should have graded it **F**.

| | |
| --- | --- |
| **Affected** | Linux and macOS. Windows was never affected. |
| **Condition** | No `.git`, `.hg`, `.svn` or `.reversibility.yml` at or above the analysis root. |
| **Not affected** | Any run inside a checkout — which is every CI run using `actions/checkout`, and the GitHub Action. |
| **Direction** | Fail-open. The gate passed where it should have failed. |

**The cause was two implementations of one namespace mapping** — the defect class this release
exists to remove, committed by the change that removed it. `ResolveRoot` split a path into
segments, dropped the empty one that a leading `/` produces, and rejoined: `/tmp/x` came back as
`tmp/x`. `QualifyPath` returned `/tmp/x`. The `--terraform-plan` comparison is exact, so the two
never matched.

An absolute Windows path opens with a drive letter and has no empty leading segment, so on the
development machine the two agreed and the full suite passed. **A test that only holds on the
platform the author uses is not holding anything.**

The two functions are now one: both call a single `locate`, so they cannot disagree. The fix is
not a more careful second implementation, because that is what was already there.

Held by `TestTheTwoSidesOfTheComparisonAgree`, which asserts the two sides return the same string
rather than asserting either value — agreement is checkable on every platform — and by
OS-independent unit tests over the path logic with POSIX inputs written out literally.

## [v1.2.0] - 2026-08-29

**The release the audit was for.** Everything below has been in `main` and in nobody's CI:
the P0 that graded thirteen unread Django migrations **A**, 32 new PostgreSQL rules, strict
coverage, and four namespace defects. A tool that improves faster than it releases is one nobody
is using.

> **Certificate schema `1.5.0`.** One version, covering everything below: `Outcome`,
> `Coverage`/`UnanalyzedFiles`, `IgnoredByPolicy`, `GradeCauses`, `N/A` on `Grade`,
> `NOT_APPLICABLE` on `AIGateStatus`, `PolicyWarnings`, `PathAnchor`/`PathPrefix`, and a `PASS`
> that now requires full coverage and no policy-ignored candidate.
>
> These were developed as five bumps and collapsed before this release. No consumer saw the
> intermediates, and five version numbers that never meant anything outside this repository
> would read as five schemas somebody might encounter. The honesty requirement applies to
> versions a consumer can observe; an unreleased intermediate is a working note. **`1.5.0` is
> now shipped and therefore frozen: the next change to the schema bumps it.**
>
> **Migrating from `1.4.0`:** a gate on `grade == "A"`, on `aiGateStatus`, or on the exit code
> is unaffected. A gate on `grade != "F"` now passes changesets nobody analyzed — switch it to
> `grade == "A"` or read `outcome`. A changeset that was only partly analyzed, or that a policy
> partly excluded, now reports `aiGateStatus: FAIL` while still exiting 0; that divergence is
> deliberate and the CLI announces it.

### Changed — read this if you have a `.reversibility.yml`

**Policy globs are now matched against the project-relative path, and a config whose globs
silently matched nothing may begin matching.**

`ignore:` patterns and waiver `path:` patterns were matched against the changeset's spelling of a
file, which is relative to whatever directory the run was pointed at. So a waiver written

```yaml
waivers:
  - rule: PG001
    path: "db/migrate/0001_*.sql"
```

matched nothing at all under `revctl check ./db/migrate`, because the engine saw that file as
`0001_drop.up.sql`. The operator had written a waiver, the file said a risk was accepted, and no
risk was accepted. The same held for `ignore:`.

Both are now resolved in the namespace every other path-keyed decision uses. **That is the
correction, not a regression** — the pattern always meant what it says, and now it does — but the
consequence runs in one direction worth stating plainly:

| Before | Now |
| --- | --- |
| A project-relative glob under a narrowed root matched nothing | It matches |
| A glob written against the stripped path matched | It no longer does |

So a waiver you believed was inert may now waive, and a waiver written against the stripped path
will stop. **Nobody should discover either from a changed grade**, which is why this is its own
heading: run `revctl check` once after upgrading and read the new `policyWarnings` on the
certificate, which names every pattern that matched nothing.

**Dead config is now reported**, on the certificate as `policyWarnings` and on stderr:

```
revctl: ignore pattern legacy/** matched no file in this changeset
revctl: waiver PG001 at db/migrate/0001_*.sql covered no finding in this changeset
```

A pattern that matches nothing is indistinguishable, from the outside, from a pattern that is
protecting you — and **dead config in a safety tool reads as protection the user does not have.**
It is never an error and it never moves a grade: a waiver written for a rule that did not fire on
this pull request is doing exactly what it should. Certificate schema `1.5.0` gains the field; it
was still unreleased when this landed, so it folded in rather than bumping.

### Fixed

**`--terraform-plan` could fail a changeset on a file nobody named.** This shipped in `v1.1.2` and
users are entitled to know it existed.

The flag was matched against the changeset by suffix, in both directions, so
`--terraform-plan a/plan.json` **also claimed `b/a/plan.json`** — any file whose path ended in the
same segments. The Terraform analyzer was then handed a document nobody had pointed it at, and a
file it claims and cannot read is `UNKNOWN`, which grades **F**. So naming one plan could fail a
pull request over an unrelated `plan.json` somewhere else in the tree, with a finding attributing
the failure to that file.

It is a wrong-direction defect: it invented severity rather than hiding it, so nothing about it was
silent — a team that hit it saw a failing gate and a finding pointing at a file they had not
mentioned. If you have ever passed `--terraform-plan` with a path whose last segments are not
unique in your repository, that is the explanation.

The comparison is exact now, both sides resolved into one namespace. See the namespace fix below;
this was its fourth instance, and the only one that failed a changeset rather than passing one.

**The Django P0's cause, rather than its fourth instance.**

Fixing candidate detection left `ignore:` globs, waiver `path:` globs and `--terraform-plan`
matching against the changeset's spelling. **Two path namespaces in one decision surface is what
produced the P0, and keeping one of them was keeping the cause.** The ruling:

> **Path-keyed decisions use one namespace, and it is not the one the caller typed.** A decision
> that depends on how the analysis root was named is a decision that changes answer for the same
> files. The caller's naming survives in exactly one place: what gets rendered.

An audit of every path comparison in the engine turned up a fourth live instance, which is the
reason the rule is now stated generally rather than applied twice:

| # | Decision | What it did | Consequence |
| --- | --- | --- | --- |
| 1 | Candidate detection | Classified `0001_initial.py` | The P0: `NO_CANDIDATES`, exit 0 |
| 2 | `ignore:` globs | Matched the stripped path | An ignore list that read as configured and was inert |
| 3 | Waiver `path:` globs | The same | Worse: a waiver matching nothing looks exactly like one that has not expired |
| 4 | `--terraform-plan` | Bidirectional suffix match | Over-claimed |

The fourth had already met this problem and answered it with a workaround — a comment in the
Terraform analyzer said the two spellings "differ", and matched by suffix in both directions. It
also **over-claimed**: `--terraform-plan a/plan.json` claimed `b/a/plan.json` too, handing the
analyzer a file nobody named, and a file it claims and cannot read is UNKNOWN, which grades **F**.
Both sides are resolved into one namespace now and the comparison is exact. The shape worth
recognising: **a comparison that needs a fuzzy match is usually two namespaces wearing a
disguise.**

**Enforced by a type rather than by discipline.** `domain.Located` is a distinct type; every
path-keyed function takes it; `engine.Candidate(f.Path)` no longer compiles. The compiler found
both remaining call sites during the change, which is the argument for the type in one sentence.

**The anchor moved from the checkout to the project, and the globs forced that correction.**
Anchoring only at `.git` meant that a tree without one resolved to absolute paths, and every
relative glob in it would have matched nothing — the exact failure being removed, reappearing in
the config. `.reversibility.yml` is a project marker too: a policy file is as good evidence of
where a project starts as a `.git` is, and a tree with neither has no globs to break, because a
glob comes from a policy file and a policy file would have been a marker.

### Added

**The project root a glob was resolved against is now on the certificate.**

A monorepo has a `.reversibility.yml` per package and one `.git` at the top, and those disagree
about where the project starts. **The nearest marker walking up wins** — a package's policy is
written about that package, so its globs say `db/migrate/**` and a run about that package must
resolve them against paths of that shape.

That answer is not always the expected one, which is why it is reported. `pathAnchor` names the
marker and `pathPrefix` says where the analysis root sat inside the project:

```json
"pathAnchor": ".reversibility.yml",
"pathPrefix": "db/migrate"
```

**A user who cannot see which root a glob was resolved against cannot debug a pattern that matches
nothing.** The markdown certificate prints it beside the dead-config list, and `revctl` prints it
on stderr — in both cases only when something matched nothing, because it is the one question a
reader cannot answer for themselves and noise on every other run.

The anchor is the marker's *name* and never its directory: a directory is a path on this machine,
and a certificate may not carry one. `pathPrefix` appears only alongside an anchor, for the same
reason — with no project root the prefix is absolute. When there is no anchor, the warnings say so
in words: *"no project root was found …, so paths were resolved absolutely and a project-relative
pattern cannot match."*


**No rendered field may carry a machine-specific value, and three tests hold it.**

Qualifying paths for classification also made them available for rendering, and the first version
of the `UNSUPPORTED_CONTENT` message used the qualified directory — which outside a project is
absolute. Two runs over the same tree unpacked in two places would have produced different
certificates, each carrying the analyst's home directory into a pull request comment. It was
caught by attention rather than by a test, so it is now a test:

> **No rendered field, in any format, may contain an absolute filesystem path, a hostname, or a
> username.**

It belongs beside the no-timestamp rule and could not have been caught by the existing determinism
tests, which compare two runs **on one machine** — where an absolute path is perfectly stable and
perfectly wrong. `TestNoRenderedOutputCarriesAMachineSpecificValue` scans every format of every
location-naming outcome; `TestTheSameTreeInTwoPlacesRendersIdentically` unpacks one tree twice and
compares bytes, catching a leak of any shape; `TestGoldenCertificatesCarryNoMachineSpecificValue`
extends it to the committed fixtures, which are regenerated on somebody's machine and then
asserted as correct forever.

**Documented numbers are derived from their authority by test, not maintained by hand.**

The badge fix generalises:

> **Any number in documentation that duplicates a fact stated in the specification must be derived
> by test.**

Now derived: the rules badge, the counts in `docs/RULES.md`'s own table of contents, the Terraform
catalog's resource-type and per-class counts, the certificate schema version, the release-target
count, and every `PG001–PG059`-style range in prose and in anchors. Two mechanisms are deliberate
— **a claim whose wording moved fails rather than passes**, because a guard that quietly stops
guarding is how the badge drifted in the first place, and **the failure prints the corrected
text**, so it is a fix rather than a research task.

**The source-scan deprecation test now states its limitation.** `TestNoFlagIsDeprecatedThroughCobra`
scans this repository's sources, so a deprecation arriving through a dependency's own mechanism
passes it untouched. That gap is covered only by `TestStdoutIsAlwaysParseableJSON`, which watches
the stream and does not care where a stray byte came from. The two are not redundant and the
comment now says which one actually holds.

### Fixed

**P0 — the Django case was still live in the invocation the README documents.**

```
revctl check django/contrib/auth/migrations --gate  ->  NO_CANDIDATES, exit 0
revctl check django/contrib/auth            --gate  ->  UNSUPPORTED_CONTENT, exit 2
```

Same files, same engine, opposite answers, and **the permissive one is the answer the documented
invocation reached.** A field audit hit it 247 times across 60 repositories.

`docs/RULES.md` §3 defines a plausible migration as a `.py`/`.rb`/`.js`/`.ts` file under a path
segment named `migrations` — and `check ./dir` reports paths relative to the named directory,
stripping exactly that segment. So `check ./migrations` handed the classifier `0001_initial.py`
and the classifier answered correctly about a path that was missing the only thing it keys on.

**This is the P0's third appearance and it is not a repetition of it.** The rule that governs the
first two is that a run which *examined nothing* must not return the permissive answer. This run
examined everything. The engine was not wrong about the changeset; it was wrong about **where**
the changeset was — which is why no amount of hardening around "did anything get read" would have
found it. The new invariant, now in `docs/SPECIFICATION.md` §2:

> **Candidate detection must not depend on how the analysis root was named.**
> `check ./migrations` and `check .` from its parent must reach the same outcome for the same
> files.

`provider.RootPrefix` resolves the command-line roots to their repository-relative form and
`engine.RootedAt` carries that prefix into `Certify`. Every question about *location* is asked of
the qualified path; **every path the certificate reports is left exactly as the caller named it**,
so a finding stays addressable with the command the reader just ran and `InputDigest` does not
move.

**Choosing the anchor was the whole design decision, and the two rejected options are recorded**
(§16.10) because the rejected ones are plausible. Anchoring at the root as typed loses
`migrations` from `check migrations/versions` — the same defect one level in. Anchoring at the
filesystem root would make a checkout parked under `~/migrate/` read every `.py` beneath it as a
migration, which is severity invented from where somebody keeps their source. The repository root
is both consistent and bounded, and it is the namespace `provider.Path` already documents and
that git and GitHub already report in — so the filesystem provider now agrees with the other
three rather than holding a fourth idea of where a file is.

`TestOutcomeDoesNotDependOnHowTheAnalysisRootWasNamed` compares six invocation shapes over every
fixture changeset in `testdata/` and needs no oracle, because the shapes are compared against each
other. `TestTheDjangoCaseFailsFromEveryDirectionItCanBeNamed` pins the answer as well as the
agreement: six shapes agreeing on exit 0 would satisfy the property and reinstate the P0.

**The deprecation notice broke JSON output, which is the compatibility measure failing in its own
way.**

`revctl check --format json --require-full-coverage` wrote `Flag --require-full-coverage has been
deprecated, …` to stdout, ahead of the certificate, so the output was no longer parseable JSON.
**The flag was kept accepted so that an upgrade would not become an unknown-flag error, and
keeping it turned the upgrade into a parse error instead** — the same failure wearing a different
coat, and a worse one, because an unknown flag names itself and a JSON syntax error does not.

Nothing here wrote that line: pflag records the message in cobra's `flagErrorBuf` during
`ParseFlags`, and cobra flushes the buffer through the *out* writer, which is stdout so that
`--help` reaches it. One call to `MarkDeprecated` was enough to corrupt every JSON certificate the
command emits.

> **All diagnostics, deprecations and warnings go to stderr. stdout carries the certificate and
> nothing else, in every format.**

`MarkDeprecated` is replaced by `MarkHidden` plus `warnAboutDeprecatedFlags`, a table written to
stderr. The notice is still given — a pipeline carrying a dead flag has to hear about it, just not
on the stream carrying the answer. Redirecting cobra's writer was rejected: cobra reaches for the
same writer for help, and `revctl snapshot --help` on stdout is the documented way to check what
the collector reads. **The output a user asked for is not a diagnostic.**

Three tests hold it, and the third holds the *mechanism*:
`TestStdoutIsAlwaysParseableJSON` over the flag space of `check`,
`TestDeprecationNoticesGoToStderr` on the reported case, and
`TestNoFlagIsDeprecatedThroughCobra`, which fails if anyone reaches for `MarkDeprecated` again.

**The README rules badge said `27 PG` where the table defines 59**, and had been wrong for
thirty-two rules because nothing checked it. It is the smallest item in the audit and it is fixed
with a test rather than a number: `TestTheReadmeBadgeMatchesTheRuleTables` derives the counts from
`docs/RULES.md` and prints the corrected badge on failure, and
`TestTheRulesTableOfContentsMatchesItsOwnTables` does the same for the counts in that file's own
table of contents. The rule tables are the specification and every other claim about them was
already checked against them; the number on the front page was the one claim exempt.

**The action's coverage warning recommended a switch that does nothing.** On a partially covered
run it told the reader to `set 'require-full-coverage: true' to fail the job here` — an input that
has been a deprecated no-op since coverage began failing closed. It now names rendering, which is
a remedy that exists.

### Added

**A blocked run now names the way forward, and the verdict behind it is unchanged.**

The audit's headline: 85% of the corpus is un-gradeable, 100% of graded runs are blocked at
`--gate`, and a Django or Rails team has no path to a passing grade, ever, because this engine
will never parse `.py` or `.rb` migrations.

**That verdict stands and is not weakened anywhere.** What changed is that the path around it is
documented rather than discovered, because **a gate with no path to green gets uninstalled, and a
gate nobody runs protects nothing** — a worse outcome than the one the strictness was bought to
prevent, and one that fails silently, since an uninstalled gate reports nothing at all.

- `UNSUPPORTED_CONTENT` and coverage-`PARTIAL` blockers now carry `engine.RenderToSQL` — *"Render
  these migrations to SQL and point the engine at the output"* — plus the concrete command per
  format: Django `manage.py sqlmigrate`, Alembic `alembic upgrade --sql`, Rails `db/structure.sql`.
  Both Python tools are named rather than guessed between. `.js` and `.ts` get the general
  sentence and no tool-specific line, because node-pg-migrate, Knex, TypeORM and Prisma each spell
  it differently and **a command that does not exist is worse than no command**. Files that are
  not migration-shaped get no remedy at all: telling the author of a `README.md` to render it to
  SQL is advice that does not fit the problem, which is how a reader learns to stop reading the
  messages.
- The README gains **ORM migrations — render them to SQL first**, a supported path with a worked
  Django example covering both directions (`sqlmigrate` and `--backwards`, because a migration
  with no usable down script still caps at C), and a CI snippet.
- The README says in one plain line, in the opening, that the engine assesses SQL, Kubernetes
  manifests and Terraform plans — and that ORM-native migration files must be rendered first.

**The three outcomes are documented together, because the middle one surprises people:**

| What the engine sees | Grade | `aiGateStatus` | Exit under `--gate` |
| --- | --- | --- | --- |
| The rendered SQL alone | **A** | **PASS** | `0` |
| The whole repository, ORM sources under `ignore:` | **A** | FAIL | `0` |
| The whole repository, nothing ignored | **F**, `PARTIAL` | FAIL | `2` |

The middle row is the §16.8 ruling applied unchanged: a human may accept a risk with their name on
it, and an agent may not inherit it. So an agent-mergeable repository is one that has **moved** to
SQL-first migrations, not one that has learned to look away from the Python ones — and saying that
plainly, beside the green row, is the honest form of "there is a path".

**Two new named principles** in `docs/SPECIFICATION.md` §2, both promoted from the fixes above
rather than invented: *candidate detection must not depend on how the analysis root was named*,
and *all diagnostics go to stderr; stdout carries the certificate*. A third, *a refusal must name
the way forward*, is the first entry in the index that is not about correctness at all.

### Added

**Four recurring shapes are now named principles rather than repeated reasoning.** Each had
several rules quietly following it before it was written down, which is the trigger: a named
principle settles the next twenty questions, where a rule settles one. `docs/SPECIFICATION.md` §2
now carries an index of all of them, and each names its own deliberate exceptions — an unmarked
exception is indistinguishable from a bug.

| Principle | Settles |
| --- | --- |
| The overwrite principle | PG012, PG013, PG028, PG043, PG050, PG054, PG057. Exception: PG033. |
| `CONCURRENTLY` changes the lock, never the verdict | Four rule pairs, and which of the two existing encodings to use next |
| Creation and destruction are not mirrors | Five pairs where reading them as symmetric is wrong in the permissive direction. Exception: PG032. |
| An undo step must be safe to run, not merely correct | PG028, PG029, and any inverse that destroys something on the way |

The recovery-capability clause of the discriminator was also promoted out of the Terraform
section: it governs PG052 and TF004 alike, and a clause that reached the same verdict by a
different route in each analyzer would be two rules that happen to agree.

**26 more PostgreSQL rules, PG034–PG059. The table went from 27 classified constructs to 59**,
and of 45 constructs probed the number reaching `PG027`/UNKNOWN fell from 39 to 11.

**The single highest-frequency fix is `SET` / `SET LOCAL` (PG034).** Rails, Sqitch and Flyway
emit it in their own transaction wrapper, so before this a repository using such a tool **could
not reach grade A at all** — the tool's own boilerplate failed the gate, and no change to the
migration would have fixed it.

Two rules deserve their reasoning read rather than their row:

**PG048 `ALTER TYPE ... ADD VALUE` is IRREVERSIBLE** because PostgreSQL provides no way to remove
an enum value. It graded F before, by falling through the fail-closed default — the right answer
with no reasoning behind it. That is the argument for table coverage over a soft default in one
line: **an accident is not a safety property.**

**PG052 `DISABLE ROW LEVEL SECURITY` is IRREVERSIBLE, and not by the data-loss test.** It
destroys no data and the setting is one line to restore. It is graded on the **third clause** of
the discriminator — *destroys a recovery capability a future rollback would depend on* — which is
the same clause TF004 fires on for deletion protection. For as long as RLS is off every row is
visible to every role, and no rollback un-reads what was read. The discriminator is now stated
as governing both analyzers rather than only Terraform: **one principle, two analyzers.**

The **overwrite principle** is named in `docs/RULES.md` for the first time, though PG012 and
PG013 already followed it: *a statement that overwrites state the migration does not record is
COSTLY, not REVERSIBLE.* PG028, PG043, PG050, PG054 and PG057 are the same shape. PG033
(`COMMENT ON`) is the one deliberate exception, flagged rather than smoothed over.

### Changed

**`FileProvider` is two-phase: `List` then `Read`. Enumeration and retrieval are separate
concerns.**

The interface had one method and took a `func(path string) bool` deciding what to read. A path
the predicate rejected was never read and **left no trace anywhere**, so every question of the
form *"what was in this changeset that we did not analyze"* was unanswerable by construction.
Three defects came out of that, each found and fixed separately before the pattern was visible:

1. **The Django P0** — a docs-only pull request and thirteen unreadable `.py` migrations reached
   the engine as the same empty file list. Both graded **A**.
2. **The coverage denominator** — counting only the files an analyzer wanted made the numerator
   and the denominator the same number.
3. **The rename bypass** — renaming `migrations/` to `db/sql/` turned strict coverage off, with
   no trace on the certificate. *"Read this README because a sibling is a `.sql`"* is not a
   decision a per-path predicate can express.

**Nothing new is computed.** The filesystem walk already visited paths without reading them,
`git diff --name-status` already returned names before blobs, the GitHub comparison API already
returned the listing in the same response, and the fake always had the fixture directory. The old
contract simply refused to expose what all four already knew.

`Include` is replaced by `Select`, which receives the **complete listing**. `Read` takes the ref
as well as the paths — the GitHub provider forced that, since its comparison ref is per-call
where the other two get revisions at construction, and storing it between phases would have made
`Read` depend on `List` having run first on the same value.

**The rename bypass is closed.** `db/sql/notes.txt` beside a `.sql` file now enumerates, counts,
and fails, exactly as `db/migrate/notes.txt` does.

**No verdict moved.** `verdicts.txt` and every golden file are byte-identical across the change —
that was the point of sequencing the plumbing before the denominator.

**Three defects surfaced while doing it**, none of which could have been found before, because
none of the paths involved were ever enumerated:

- The **policy file itself** was counted against coverage. Every repository with a
  `.reversibility.yml` would have failed.
- **Policy-ignored paths** were counted, contradicting the ignore ruling directly.
- **`git`'s two halves disagreed about renames** — the listing collapsed one into a single entry
  while the reader emitted a delete plus an add, so `Read` was handed a path `List` never
  produced. Now pinned by a test, because the two halves of one provider have to agree for the
  same reason the four providers have to agree with each other.

Mutation results, over 768 property cases: partial coverage not failing the grade fails 126, the
CLI not exiting 2 fails 126, reverting the denominator to candidates-only fails 126, and
collapsing the enumeration back to the files that were read fails 294. The third of those caught
**nothing** before this change and 84 after the oracle was given an independent denominator; it
is 126 now that the oracle no longer has to concede a boundary the contract could not cross.

**A partial pass is a bypass. Coverage now fails closed, and this reverses an earlier ruling in
the same unreleased cycle.**

The engine will not vouch for a changeset it only partially understands. If any file in a
migration directory went unread: `coverage: PARTIAL`, **grade F**, `aiGateStatus: FAIL`, and
**exit 2** — with no flag and no threshold.

```
Cannot guarantee reversibility. Unanalyzed files found in migration directories.
Remove them or explicitly ignore them in the config.
```

**The coverage math changed with it.** The denominator is now every file in a migration
directory, not every file an analyzer wanted — a `README.md`, a `.gitkeep`, a helper script all
count. Counting only the files the engine already understands made the numerator and the
denominator the same number, which is a check that always passes. A directory qualifies if it is
**named** for migrations or **holds a file an analyzer claimed**; the second clause stops a
rename from defeating the check.

**What it reverses, and why.** The earlier ruling was that `PARTIAL` never moves the grade — *a
file the engine cannot read is not evidence the change is unsafe, and inventing severity from
ignorance is the mirror of the P0* — with `--require-full-coverage` opt-in because defaulting it
on *"would fail every Django and Rails PR on day one, and a gate everyone disables protects
nobody."* Both are real arguments. What overrides them:

- **F does not mean "this change is dangerous". It means "this cannot be certified"** — already
  exactly what it means for an analyzer error and for PG027. Reading F as a severity claim is
  what made the mirror argument persuasive, and it never was one.
- **An analysis that read four of five migrations did not complete.** Grading it on the four is a
  verdict about a changeset that does not exist.
- The gate-everyone-disables risk is answered by the escape hatch rather than a softer default:
  `ignore:` in the policy is explicit, mixed into `policyDigest`, and printed on the certificate.
  A recorded decision is not a bypass.

**The escape hatch needed one correction to actually work.** §16.8 says a policy-ignored
candidate closes the merge gate. Applied to every ignored path it would make strict coverage
unusable — the only way to satisfy it would permanently deny an agent a merge. So only ignored
**candidates** count against the gate: ignoring a `README.md` beside the migrations is telling
the engine what a README is; ignoring a `.rb` migration is accepting a reversibility risk and
still closes it. Both directions are pinned by tests.

**Migration.** `--require-full-coverage`, and the action input of the same name, are deprecated
no-ops — kept accepted so an upgrade does not turn a pipeline into an unknown-flag error. Remove
them. A repository using a migration format this engine cannot read should list those paths under
`ignore:` once.

**A coverage failure never reads as an accusation.** Strict coverage means every Django, Rails,
Alembic and Ecto repository fails on its first run, so the first sentence those users read from
this tool matters more than any other line it prints. The generic grade-F summary opened with
*"Rolling this back would lose data"* — over a changeset the engine had declined to read. It now
says the true thing instead:

> **Not assessed.** Part of this change was not analyzed, so its reversibility could not be
> measured. **This is not a finding about your change** — it is the engine saying it could not
> read all of it.

The remedy follows immediately. A changeset that is *both* destructive and partly unread keeps
the data-loss wording, because there the claim is true, and both directions are pinned by tests —
the two messages differ by one branch, and a branch is one refactor from being lost.

**Known limitation, ruled and scheduled rather than discovered:** coverage is complete for
migration-*named* directories and not for directories identified only by holding an analyzable
file, because a provider decides whether to read a file from its path alone. Until the two-phase
`FileProvider` contract lands (`docs/SPECIFICATION.md` §16.9), **renaming `migrations/` to
`db/sql/` turns strict coverage off**, with no trace on the certificate. That evasion is filed
for the harness bypass catalogue in
`docs/proposals/bypass-rename-migration-dir.md`.

### Fixed

**`SELECT` is now classified by effect, and the near-miss is why there is a new invariant.**

The dialect triage was about to propose a rule that looks obviously harmless — *a `SELECT`
changes nothing, so classify it REVERSIBLE* — and `SELECT` is a node type, not an effect:

| Statement | The node | What it does |
| --- | --- | --- |
| `SELECT count(*) FROM orders` | `SelectStmt` | nothing |
| `SELECT setval('orders_id_seq', 1000)` | `SelectStmt` | resets a sequence — the PG010 hazard |
| `WITH d AS (DELETE FROM orders WHERE id < 5 RETURNING *) SELECT count(*) FROM d` | `SelectStmt` | **deletes rows** |

Both destructive rows graded F beforehand **by accident**: they were unrecognised, and
unrecognised is UNKNOWN. A rule keyed on the node type would have converted two accidental
correct answers into two deliberate wrong ones — and would have been merged, because on its face
it says only that reading data is safe.

A data-modifying CTE now carries the verdict of the DML it contains, `setval()` carries PG010's,
and **every other `SELECT` stays UNKNOWN**. No permissive default was added. New spec invariant:

> Classification is by effect, never by statement type. A construct is classified by what it does
> to data and schema, not by the node the parser returns. Where a statement type can carry a
> destructive effect — data-modifying CTEs, function calls with side effects, dynamic SQL — it is
> classified by that effect or it is UNKNOWN.

`EFFECT001` is a new shape of fixture that asserts the relationship between three statements
rather than one rule, since no single rule ID describes it. The fixture loader now recognises
that shape explicitly, because "does not look like a rule ID" is also what a typo looks like.

**Two constructs the engine classified wrongly, both in the permissive direction, both printing
an undo an operator would run mid-incident.**

**`CREATE OR REPLACE VIEW` graded A and its undo plan destroyed the view.** `ViewStmt.Replace`
was never read, so the statement was indistinguishable from `CREATE VIEW`, matched PG025, and
had `DROP VIEW x;` printed as its rollback. When the view already existed — the only reason
anyone writes `OR REPLACE` — that undo destroys an object that predates the migration. New
**PG028**, COSTLY, and the undo names what has to be recovered and warns explicitly against the
drop. An undo plan that destroys a pre-existing object is worse than no undo plan.

**`DROP MATERIALIZED VIEW` was graded as `DROP VIEW`, with a rationale asserting the opposite of
the truth.** `convertDrop` folded `OBJECT_MATVIEW` into `KindDropView`, so PG016 reported that
the object "holds no data of its own" — for a materialized view that is the entire distinction —
and emitted `CREATE VIEW`, the wrong object type. New **PG029**, COSTLY / EXCLUSIVE, whose undo
names `CREATE MATERIALIZED VIEW` plus `REFRESH`.

**The structural hole underneath D2 is closed.** PG016 in `docs/RULES.md` never mentioned
materialized views: the code was classifying a construct the authoritative table did not list,
and every test passed. A fixture test cannot catch that — PG016 had a fixture, and it used a
plain view. So there is now a second invariant beside it, with the same authority:

> Every construct the code can classify must have a row in the authoritative table. A
> classification with no table row does not exist.

`TestEveryClassificationHasATableRow` enumerates the rule IDs the analyzer sources can emit
against the rows in `docs/RULES.md` and fails on either mismatch. Verified by mutation in both
directions: deleting PG029's row fails it, and adding a row nothing emits fails it.

### Added

**PG030 and PG031 — the safe two-step patterns no longer grade worse than the unsafe ones.**
`ADD CONSTRAINT ... USING INDEX` promotes an index that already exists, so it neither scans nor
builds (REVERSIBLE / SHORT), and `VALIDATE CONSTRAINT` completes the `NOT VALID` sequence PG022
begins (REVERSIBLE / FULL_SCAN). Both reached PG027 and graded **F** before, which meant the
engine actively pushed users toward the one-step forms that grade better. A safety tool that
punishes the safe pattern teaches people to stop using it.

**PG032 `GRANT` / `REVOKE` and PG033 `COMMENT ON`**, both REVERSIBLE per the owner's ruling.
Privileges are not data and the opposite statement restores them exactly; the rationale says on
every finding that the engine does not verify the opposite statement is present.

**`IgnoredByPolicy` — a policy `ignore:` now closes the merge gate.** An ignore is a human
decision, exactly like a waiver, so it follows the waiver pattern rather than the coverage one:
coverage stays `FULL`, because the engine was capable of reading the file and was told not to,
and coverage describes capability rather than permission. The certificate lists every candidate
excluded and the markdown renders it above the findings. `AIGateStatus: PASS` now requires zero
policy-ignored candidates.

That completes one principle across all three mechanisms: **humans may accept risk with their
names on it; agents may not inherit it.**

**`GradeCauses` — every grade now carries its cause, in every rendered output.** A capped grade
used to be unexplainable from the certificate a reviewer actually reads: a changeset with every
finding REVERSIBLE could arrive at **C** and nothing outside the JSON said which condition
applied the ceiling.

```
**Why this grade**

- assigned A: every finding is REVERSIBLE
- capped at C: no usable down migration for 0031_add_users.up.sql
```

Grade A states that nothing capped it, because *"nothing capped this"* and *"nobody wrote down
why"* must not render identically. New spec invariant alongside the fail-closed ones:

> Any field the engine computes that determines or constrains the verdict must appear in every
> rendered output, not only in JSON. A reader must never have to open the JSON to learn why they
> were blocked.

`TestEveryCappedOrFailedGradeNamesItsCauseInMarkdown` holds it over every fixture. It checks each
cause individually rather than the certificate as a whole — the first version checked the whole,
and a mutation weakening a cap's reason to "a cap applied" walked straight through on the
strength of a specific assignment line beside it. The vague line is the one the reader is stuck
on, so it is the one that has to be specific.

**The exit code and `aiGateStatus` now say so when they diverge.** A run that exits 0 with the
merge gate closed prints one line naming the specific cause. Two signals in one certificate that
quietly point opposite ways is the disease this project has now fixed three times.

**An unrendered template says so.** A file containing `{{`, `{%` or `<%` that fails to parse is
still UNKNOWN and still grades F — an unrendered template is unexamined, not safe — but the
message now says it appears to be a template and was not rendered, rather than reporting a
syntax error at a brace and sending someone hunting for a typo in valid Go template syntax.

### Added

**Coverage: a second axis for the part of a changeset the engine could not
read.**

The P0 below fixed the case where *nothing* was analyzed. This is the case where
*some* of it was: one `.sql` migration beside three Django `.py` ones. That
graded on the file it read and could reach A / PASS with the rest unexamined —
the same disease in the form more likely to occur, since a real pull request
usually touches something the engine understands.

**Coverage is a fact about the changeset, not a penalty, and it is not folded
into the grade.**

| Field | Meaning |
| --- | --- |
| `coverage` | `FULL` or `PARTIAL`. A changeset with nothing claimable is vacuously `FULL` — nothing was skipped. |
| `unanalyzedFiles` | Every file the engine did not read, each with the reason. A list, never a count: a reviewer's next question is always "which ones". |

- **`PARTIAL` never changes the grade.** A file the engine cannot read is not
  evidence that the change is unsafe. `TestPartialCoverageNeverChangesTheGrade`
  certifies the same SQL with and without an unreadable sibling and compares every
  measured field.
- **`aiGateStatus: PASS` now requires grade A *and* `FULL` coverage.** An
  autonomous agent gets no merge on a changeset that was only partly understood.
  The enforcement lives in `Grade.Gate(Coverage)` — coverage is a parameter, not a
  field read elsewhere, so a caller who has not thought about coverage does not
  compile. Seven call sites had to be updated, which is the mechanism working.
- **The markdown certificate names every unanalyzed file, above the findings.** A
  list of what the engine *did* find, printed first, is exactly what makes an
  incomplete analysis look complete.
- **`--require-full-coverage`** (action input `require-full-coverage`) makes
  `PARTIAL` exit 2. Off by default.

**The exit code and `aiGateStatus` diverge here, deliberately.** Grade A with
partial coverage exits **0** under `--gate` and reports `aiGateStatus: FAIL`. The
exit code is the human pipeline's gate — it compares `effectiveGrade` and honours
waivers, per S10 — and a human can read the list of skipped files and judge. An
agent cannot, so the field it reads is stricter.
`TestGateExitCodeAndAgentGateDivergeOnPartialCoverage` pins it so the gap is not
closed by accident in either direction.

The help for `--gate` used to call it "the setting autonomous agents must use",
and the README said much the same. That was already loose and is now wrong: an
agent reads `gate-status`, never an exit code. Both are corrected.

**This is now the project's pattern for a whole class of question**, recorded in
`docs/SPECIFICATION.md` §2: *the grade describes the evidence, the gate decides
what to do about it.* S10 applied it to waivers, which argue for leniency; this
applies it to coverage, which argues for severity; the answer is the same either
way. A grade that configuration can improve stops meaning "reversibility", and a
grade that ignorance can worsen stops meaning it just as thoroughly — while
failing in the direction that is harder to notice, because a tool that
over-reports looks conscientious.

### Fixed

**A changeset the engine could not read graded A and passed the merge gate.**

A pull request of thirteen Django `.py` migrations produced grade **A**,
`aiGateStatus: PASS`, exit 0. No rule misfired. The scoring specification said
that an empty changeset with zero relevant files grades A with
`applicable: false` and gate PASS, and the code implemented that faithfully —
so "no findings" and "no analysis" were the same value, and the value was the
permissive one.

This is the **second** occurrence of the class. The first was the `:v1` image,
where `revctl` with no arguments exited 0. Two independent occurrences meant the
invariant was missing from the architecture, so it is now written down with the
same authority as fail-closed:

> The engine never emits a passing grade for a changeset it did not analyze.
> Absence of analysis is not evidence of safety.

Grading is now the *second* question a run answers. The first is what it was able
to do at all, and it has three answers, reported in the new `outcome` field:

| `outcome` | Reached when | Grade | Gate | Exit under a gate |
| --- | --- | --- | --- | --- |
| `ANALYZED` | An analyzer claimed at least one file. | graded normally | follows the grade | `0` / `1` by the grade |
| `NO_CANDIDATES` | Nothing here any analyzer could ever claim — a docs-only pull request, Go source alone. | **`N/A`** | `NOT_APPLICABLE` | **`0`** |
| `UNSUPPORTED_CONTENT` | Files that plausibly **are** migrations, and no analyzer claimed them. | **`N/A`** | `NOT_APPLICABLE` | **`2`** |

`A` now means analyzed and found reversible, and nothing else.

`UNSUPPORTED_CONTENT` names what it saw rather than reporting a bare "not
applicable", because a bare "not applicable" is what made the Django case read as
"nothing here":

```
found 13 files in django/contrib/auth/migrations that no analyzer supports
(.py migrations). Reversibility was not assessed.
```

Candidate files are now fetched and shown to the engine by both the CLI and the
GitHub App. Until this change a docs-only pull request and thirteen unreadable
migrations arrived at the engine as the same empty file list, and no amount of
scoring logic could have told them apart.

**Migration.** A gate written as `grade == 'A'`, `aiGateStatus == 'PASS'`, or on
the exit code is unaffected. A gate written as `grade != 'F'` now passes
changesets nobody analyzed — switch it to `grade == 'A'` or read `outcome`. The
`applicable` field still means exactly `outcome == 'ANALYZED'` and is retained
for consumers pinned to `1.4.0`.

**The certificate no longer contradicts itself.** The markdown certificate used to
print "the engine has no opinion on it" three lines under a green ✅ **PASS**, and
a reader resolves that in favour of the badge every time.
`TestProseAndFieldsNeverDisagree` now asserts that when the prose disclaims, no
field says PASS — driven through the real engine, because the disagreement was in
the certificate before any renderer saw it.

### Added

**A property test over the CLI surface, replacing the case list that missed both
bypasses.** `TestNoArgumentCombinationPassesAGateWithoutAnalysis` enumerates 672
combinations — 14 tree shapes × 6 gating modes × 8 modifiers, covering an empty
directory, an unreadable directory, unsupported extensions, a path matching
nothing, a glob that expanded to nothing, a config ignoring everything, and
`--before` pointing at an identical tree — and checks eleven properties against an
oracle that never asks the engine anything.

The oracle restates the extension conventions independently on purpose. Deriving
them from `Engine.Supports` would make the test agree with the engine by
construction, and whether the engine and the world agree about what was read is
the entire question.

Verified by mutation rather than by passing. Eight mutations, and the number of the
672 cases each one fails:

| Mutation | Cases failed |
| --- | --- |
| Non-analyzed outcomes grade A again (the P0 itself) | 324 |
| `UNSUPPORTED_CONTENT` exits 0 under a gate | 90 |
| Candidate detection disabled | 90 |
| The CLI stops showing candidates to the engine | 90 |
| The gate ignores coverage (PASS on grade A alone) | 42 |
| Coverage is always `FULL` | 168 |
| `--require-full-coverage` is a no-op | 6 |
| `unanalyzedFiles` emptied | 168 |

The first version of the file caught only the first of these. Disabling candidate
detection passed all 588 cases, because every Django tree simply became
`NO_CANDIDATES`, which the exit-0 branch permits — the invariant survived and the
product did not. Property 6 exists because that mutation found the hole.

### Fixed

**Four documents said a snapshot source mismatch was silently ignored. It stops
the run.** README.md, CLAUDE.md and the `v1.1.2` notes below all carried the same
sentence — "a missing snapshot, a stale one, or a fingerprint that does not match
is treated as absent" — and only the first third of it was true.

| Situation | What the docs said | What happens |
| --- | --- | --- |
| Snapshot file missing | treated as absent | ✅ treated as absent |
| Snapshot **stale** | treated as absent | ❌ **used**, with a warning |
| Sources **mismatch** | treated as absent | ❌ **exit 2** |

A source mismatch is the loudest outcome in the system, not the quietest — the
sentence described a silent fallback that has never existed in `snapshot.Set.merge`.
It also contradicted the "stale context is used and flagged, never discarded" rule
sitting a few lines below it in both README.md and CLAUDE.md.

`docs/RULES.md` §3 had the same error in its own words, saying a stale snapshot
"leaves the grade exactly where it was". A stale snapshot is used, so it still
produces a lock duration band and that band still caps the grade.

Each document now carries the five outcomes as a table rather than as one sentence
holding three claims, since compressing them is what produced the error. An audit
script checks all six documents against `internal/snapshot/load.go` on every run.

**A snapshot's `--environment` label never reached the message it exists for.**
`revctl snapshot --environment prod` records the label in the file, and the flag's
own help says it "appears in messages when two snapshots disagree about their
source" — but the mismatch error in `snapshot.Set.merge` printed only the two
fingerprints. The one part of that message a human could act on was the part it
left out.

Before:

```
… is a postgres snapshot of a different source than the one already loaded
(fingerprint 999999999999, expected aaaa1111bbbb)
```

After:

```
… is a postgres snapshot of a different source than the one already loaded
("staging", fingerprint 999999999999; expected "prod", fingerprint aaaa1111bbbb)
```

The fingerprints were always correct and unambiguous, and — read at the moment a
pipeline broke — interchangeable. `"staging"` is the half that says which file to
remove.

The label is quoted rather than interpolated bare: it is free text read out of a
file, so quoting keeps a label containing spaces readable and stops one containing
newlines from forging a second line of output. A snapshot collected without
`--environment` falls back to the fingerprint alone rather than printing empty
quotes, which would read as a bug in the tool rather than as a missing flag. Both
are pinned by tests, the second because it is the case nobody would notice.

**`docs/PRODUCTION-CONTEXT.md` and `docs/ESTIMATES.md` both told readers that
production context cannot change a grade.** That stopped being true when the
`WILL_FAIL` verdict and the lock duration bands shipped, and the claim failed in
the permissive direction — a reader was told the worst a snapshot could do was
annotate a finding.

`docs/ESTIMATES.md` contradicted itself on the point: its opening said "nothing
here affects a grade" while its own band table three sections later listed "cap at
B" and "cap at C". The opening now draws the distinction the rest of the document
depends on — a printed duration is presentation and nothing reads it back, while
the *band* that duration falls into is scored and may only make a grade worse.

`docs/PRODUCTION-CONTEXT.md` was never updated by the S11 patch at all, so it
described the pre-patch design. It now documents both mechanisms that reach a
verdict: `WILL_FAIL`, why it is reported apart from `IRREVERSIBLE`, and that no
waiver can cover it; and the four bands with their caps, why `NEGLIGIBLE` imposes
nothing, and why `PG014` gets no band despite an `EXCLUSIVE` lock. The one-way
ratchet is stated with the vocabulary spelled out, because "lower" and "raise" had
been used in both directions across these documents.

**A documented workflow could not have worked.** The scheduled-snapshot example
obtained `revctl` by invoking `VIKOIT/reversibility-engine@v1` with the comment
"for the binary", then called `revctl` in the next step. The action is a composite
action that grades a changeset: it never writes to `$GITHUB_PATH`, so the call
would have failed with *command not found* — and before reaching it, the action
would have run a certification of its own and, at the default `gate: A`, could
have failed the job first. The example now downloads the release asset and
verifies it against `checksums.txt`, and says why the action is the wrong tool for
that step.

Also corrected: `--context` on a file that exists but cannot be read or decoded is
exit 2, where the docs stated only the missing-file case; the `snapshot` flags
(`--kubeconfig`, `--environment`, and that `--dsn` with `--kube-context` is an
error) were undocumented; and the context digest's exclusion of `collectedAt` —
the reason re-collecting from an unchanged database still produces a
byte-identical certificate — was recorded nowhere.

**The README still described the project as it stood before `v1.1.2`.** It is the
first thing anybody reads and it was several radical changes out of date, in the
permissive direction more than once. Overhauled:

- **It said `No Terraform.`** under Limitations, in the same release that shipped
  nine Terraform rules and a 92-type catalog. A new *Terraform plans* section now
  covers the workflow, the three-clause discriminator, the classification layers,
  the catalog and its honest denominator, `revctl catalog show`, the offline/no-
  telemetry guarantee, and why `TF003` is retired. The example output is real
  `revctl` output rather than an invention.
- **The exit-code contract was one sentence.** A new *Fail-closed by construction*
  section states both invariants — unknown means unsafe, and **a gate must prove
  it ran** — with a table of every situation that reaches exit 2, and the `:v1`
  image incident written up as the reason bare `revctl` no longer exits 0.
- **The composite-action shift was a footnote.** It now has its own section
  contrasting the frozen `v1.0.x` Docker action with the composite one: any runner
  OS, one checksum-verified binary rather than an image pull, and why a name and
  its deprecated alias together is an error rather than a precedence decision.
- **`darwin/amd64` was absent from the platform story.** *Supported release
  targets* now carries the four-target matrix with the dropped target listed
  explicitly, the queue-time reason, why cross-compiling was rejected, and the two
  supported paths for Intel Mac users.
- **It claimed context "never changes a grade at all".** That stopped being true
  when `WILL_FAIL` and the lock duration bands landed. It now documents the
  one-way ratchet correctly — context may make a verdict *worse* and never better
  — with the band table, why a waiver cannot cover `WILL_FAIL`, and the
  vocabulary that has been ambiguous.
- Smaller alignments: the `TF001`–`TF010` row said ten rules where nine are
  active; the grades table gained `WILL_FAIL` and the verdict severity ordering;
  the rules-specification index gained §5; the policy example and its rules gained
  `terraform_types`; the CLI reference gained `snapshot`, `catalog`, `version`,
  `--terraform-plan`, `--config`/`--no-config` and `--context`; the architecture
  diagram gained the Terraform, policy and snapshot packages and the driver
  quarantine; the image example moved off `1.1.1`; and the action's `config` input
  now says that a root `.reversibility.yml` is still discovered, which the previous
  wording implied it was not.

`internal/policy/testdata/valid.yml` is the README's policy example verbatim, so
it and the flow-form mirror in `load_test.go` gained `terraform_types` alongside
it. If that file stops loading, every policy written from the README stops loading
with it.

**The README advertised `schemaVersion` `1.0.0`; it has been `1.4.0` since the
Terraform analyzer landed.** The same paragraph tells consumers to pin against the
schema rather than against the tool, so the one number a downstream gate is
directed to depend on was four bumps out of date. The status badge and status line
also still read `v1.0.0` and now read `v1.1.2`.

**The rules badge omitted Terraform**, reading `27 PG · 15 K8S` and undercounting
a whole analyzer that shipped in the same release. It now reads
`27 PG · 15 K8S · 9 TF` — nine rather than ten because `TF003` is retired and its
number is never reused. All three counts match the fixture directories, and a rule
with no fixture does not exist.

## [v1.1.2] - 2026-08-26

The first release since `0.1.0` to publish binaries. `v1.0.0`–`v1.1.0` were cut as
tags and are not removed, but their release runs did not complete, so everything
below ships here.

### Security — a gate that reported success having analyzed nothing

**If you consume `VIKOIT/reversibility-engine@v1`, your gate passed without
running between the v1.1.0 image publish and this release.** No grade was wrong.
There was no grade.

`revctl` invoked with no arguments printed help and exited **0**. The frozen
`v1.0.x` Docker action pulls `ghcr.io/vikoit/reversibility-engine:v1` and sets no
`entrypoint:` and no `args:`, so it runs whatever the image declares. That
entrypoint used to be a script that performed the analysis; from v1.1.0 it was
`revctl` itself. Publishing the v1.1.0 image under the moving `:v1` tag therefore
turned every `@v1` consumer's gate into a green check over nothing.

The moving tag was the vector. The defect was the exit code: **the one invocation
that analyzes nothing was also the only one that could never fail.**

Fixed at the source and at every layer above it:

- `revctl` with no arguments now exits **2** and prints help to stderr — stdout is
  where a certificate goes. `revctl --help` and `revctl help` still exit 0, so
  nothing learns to ignore this exit code.
- The action exits 2 if no certificate was written, and now also if the verdict
  cannot be read back out of one. A step reporting FAIL while passing the job has
  proved only that something was written.
- An image whose no-argument invocation exits 0 is no longer publishable, and the
  self-test builds the image from the commit and asserts it — so this is caught
  before a tag moves rather than after.
- Every `docker run` in this repository and in the README now names
  `--entrypoint` explicitly. An inherited entrypoint is a silent dependency on a
  value somebody else can change.
- **Immutable image tags only.** `:v1` is no longer published;
  `.github/workflows/restore-image-tag.yml` repoints an alias from a runner, with
  the digest read back and the restored `ENTRYPOINT` asserted.

New invariant in `CLAUDE.md` §2: **a gate must prove it ran. No certificate
produced means exit 2, never exit 0.**

### Fixed — documentation named a version that never existed

The README, `docs/PRODUCTION-CONTEXT.md`, and `CLAUDE.md` described the composite
action as `@v2` and its image as `:v2`. **Neither has ever existed.** Every tag cut
is `v1.x`: `v1.0.0`–`v1.0.2` are the Docker action, `v1.1.0` onward are the
composite, and the transition needed no new major because every input kept
working. Users following the README were told to write a ref that resolves to
nothing. All references now read `@v1`, and §11e records the one-line ruling so
the ambiguity cannot regenerate.

### Removed — prebuilt `darwin/amd64` binaries

**Releases no longer publish `revctl` or `revsrv` for Intel Macs.** The matrix is
four targets, all built and verified on their native architecture:
`linux/amd64`, `linux/arm64`, `darwin/arm64`, `windows/amd64`.

`macos-13` is the only hosted Intel Mac runner, and on the v1.1.1 release it sat
in the queue for over two hours before the run had to be cancelled by hand.
Nothing bounds that — `timeout-minutes` starts when a job begins *executing*, so a
job waiting for a runner that never arrives waits forever.

Cross-compiling from Apple Silicon was considered and rejected. It would have
shipped the one artifact nobody could execution-test, and the per-target check —
does this binary still classify a `DROP TABLE`? — is the whole reason builds are
native rather than central. In a tool that gates merges, an untested binary is
worth less than no binary.

**Intel Macs:** build from source (`CGO_ENABLED=1 go install
github.com/VIKOIT/reversibility-engine/cmd/revctl@latest`) or use the Docker
image. Prebuilt binaries target Apple Silicon.

The action now says this directly. An Intel Mac runner previously would have
404'd on a missing asset, which reads as a broken release rather than an
unsupported runner; it now fails naming the remedy.

### Added

**Terraform plan analyzer.** `revctl check` now classifies `terraform show -json`
output: 9 active rules (`TF001`–`TF010`, of which `TF003` is retired and never
reused) over a plan, backed by a catalog of AWS resource types.

**It never reads `terraform.tfstate`.** State holds provider credentials and
attribute values in plaintext; a plan does not, and there is no code path that
opens one.

**Only destruction is classified.** A created or updated-in-place resource has a
reverse by construction, which is what keeps the catalog finite — the problem was
never "hundreds of AWS resource types", it is the types whose destruction hurts.

The discriminator, in three clauses: a change is irreversible if it destroys data,
destroys an identity that re-applying the same configuration cannot recreate, **or
destroys a recovery capability a future rollback would depend on.** The third
clause is why `TF004` grades a one-line `deletion_protection = false` alongside
deleting a snapshot — both destroy the undo rather than the system.

Classification runs in layers: evidence in the plan first (an attribute like
`allocated_storage` marks a type stateful whatever the catalog says), then the
embedded catalog, then user `terraform_types` in `.reversibility.yml`, then
`TF010`/UNKNOWN. Evidence and overrides may only ever raise severity. **The type
name is never matched** — `aws_db_subnet_group` contains "db" and holds nothing.

**92 AWS resource types classified, of roughly 1,400.** The stateless half is
load-bearing rather than filler: an unclassified deleted type grades F, so the
network, IAM, load-balancing and compute entries are what stand between a new user
and an immediate failing gate.

**No telemetry, and `check` never fetches.** The catalog is compiled in and works
offline for the lifetime of the binary. When a plan destroys a type the catalog
does not know, the certificate prints **one** `.reversibility.yml` snippet and
**one** pre-filled issue link covering every unknown type — one, because six paste
operations is where somebody switches the gate off instead. Nothing is sent
anywhere; a human chooses to open the link.

`revctl catalog show` prints the catalog's version, digest and coverage.
`revctl catalog scan` is a maintainer tool that proposes candidates from a
provider schema — it needs terraform on PATH, says so clearly when it is missing,
and nothing in the check path depends on it.

`--terraform-plan <path>` analyzes a plan whatever it is named; the default
convention is `*.tfplan.json`, deliberately narrow so a stray `plan.json` in a
repository does not grade F.

**`TF003` is retired and its number will never be reused.** `prevent_destroy` is a
`lifecycle` meta-argument, which the JSON configuration representation does not
carry — and the signal is self-erasing, since a plan containing a delete already
proves `prevent_destroy` is not set. `TF001`/`TF002` catch the destroy itself.

**The certificate schema is now `1.4.0`** — `catalogVersion` was added, absent
unless a plan was analyzed.

### Added — the WILL_FAIL verdict

**`WILL_FAIL` — a new reversibility verdict.** It means the change will not apply
at all, as distinct from `IRREVERSIBLE`, which means it cannot be undone. One is a
risk to weigh; the other is a defect, and the fix belongs in the migration rather
than in the rollback plan. It ranks above `IRREVERSIBLE`, always grades **F**, and
is reported separately in the blockers, the undo plan, SARIF, and Markdown.

It is reached from evidence only. Today that means one thing: `SET NOT NULL`
against a column a production snapshot shows contains nulls. Postgres validates
every existing row and a single violation aborts the statement and rolls the
transaction back, so this is a certainty rather than a risk — and no lock duration
is estimated for it, because the statement never gets far enough to hold one.

**Lock duration bands.** With a snapshot, a lock hazard of `FULL_SCAN` or heavier
is bucketed by how long it is expected to be held:

| Band | Duration | Effect |
| --- | --- | --- |
| `NEGLIGIBLE` | under 1s | none |
| `NOTICEABLE` | 1s – 30s | none |
| `DISRUPTIVE` | 30s – 5m | grade no better than B |
| `OUTAGE` | over 5m | grade no better than C |

**A band may only lower a grade, never raise one** — lower meaning worse. A small
table does not turn a C into a B; the absence of evidence of a problem is not
evidence of safety. A missing snapshot, or a table the snapshot does not describe,
yields no band and therefore no cap — never reassurance. A **stale** snapshot is
used and flagged rather than ignored, so it still produces a band; two snapshots of
one kind from different sources stop the run rather than being quietly dropped.

When `pg_relation_size` is unavailable, size falls back to
`reltuples × Σ(avg_width across ALL columns of the table)`. `avg_width` is per
column and the sum matters: one column's width is not a row width. When neither is
available the engine does not guess — the context is treated as absent for that
finding.

New fixture group `testdata/fixtures/context/`, one fixture per band plus both
`SET NOT NULL` cases, the fallback path, and an incomplete snapshot. Each names the
grade it has with the snapshot and without it, so the direction of the rule is
checkable as data. A mandatory regression asserts all 47 existing fixtures grade
identically when no snapshot is supplied.

**The certificate schema is now `1.3.0`.** `WILL_FAIL` is a new value in an
existing enum, so a consumer that switches exhaustively on reversibility has a case
it has not seen — the one change here that warrants attention rather than a
footnote.

### Added — production context

**Production context — `revctl snapshot` and `revctl check --context`.** The
engine can now say that a rewrite covers 212M rows and roughly 48 GiB, that
dropping an index nothing has read is genuinely cheap, and — most usefully — that
a `SET NOT NULL` **will fail** because the column already contains nulls, before
it runs rather than after.

**The engine still never connects to anything during analysis.** A separate
command collects metadata into a file and the analysis reads the file. CI
therefore never needs a production credential, certificates stay byte-identical
between runs, and the analyzers stay pure functions over a changeset. An
architecture test fails the build if `pgx` or `client-go` can be reached from
`internal/domain`, `internal/analyzer`, `internal/engine`, or `internal/snapshot`
through any number of hops — and separately asserts the collector still reaches
them, so the guard cannot go vacuous.

**Metadata only, and tested rather than asserted.** Table sizes, row estimates,
index sizes and scan counts, column null fractions; storage classes with their
reclaim policies, claim capacities, workload replica counts. No row of user data,
no column values, no Secrets, no connection string. CI seeds a throwaway database
with passwords, API keys, and private-key material, runs the collector, and fails
if any of them reaches the output. The PostgreSQL connection is opened with
`default_transaction_read_only=on`; Kubernetes access is `List` only.

**Context never changes a grade.** Enrichment writes one optional field on a
finding and touches nothing else. A property test runs every fixture in the
repository twice — with and without a snapshot deliberately sized to trip any
size-sensitive rule — and asserts the grade, effective grade, gate status, and
every individual classification are identical. A stale snapshot (older than seven
days) is used and flagged rather than discarded; a **missing** snapshot is not an
error at all.

New dependencies, quarantined to `internal/snapshot/collect`:
`github.com/jackc/pgx/v5` and `k8s.io/client-go`, both pinned to the newest
versions that still declare `go 1.22`.

**The certificate schema is now `1.2.0`** — findings gained an optional `context`
object, and the certificate gained `contextWarnings`. Nothing was removed or
redefined.

Documentation: [`docs/PRODUCTION-CONTEXT.md`](docs/PRODUCTION-CONTEXT.md) for what
is collected and the grants it needs, [`docs/ESTIMATES.md`](docs/ESTIMATES.md) for
every formula and where it will be wrong.

### Added — the policy file

**Policy file — `.reversibility.yml`.** Discovered by walking up from the analysis
path, or named with `--config`, or ignored with `--no-config`. It carries a gate
threshold, `ignore` globs, expiring waivers, and tighten-only overrides.

A waiver requires a `reason` and an `expires` date; missing either is a
configuration error that exits 2, not a warning. `expires` is a calendar day, at
most 180 days out, and an expired waiver is inert — the finding returns with no
edit to the file. A waiver may not cover an `UNKNOWN` finding, and waived findings
are reported under a new `Waived` section rather than suppressed.

**A waiver never improves the measurement.** `grade` is what the evidence says and
no policy setting moves it; the new `effectiveGrade` is `grade` with waived
findings set aside and is what a CI threshold compares. `aiGateStatus` follows
`grade`, so a waiver can unblock a human's pipeline and can never authorise an
autonomous agent to merge. Without a policy, `effectiveGrade` equals `grade`.

The resolved policy is hashed into `inputDigest` and reported as `policyDigest`.
The hash covers the resolved configuration rather than the file's bytes, so
reformatting or editing a comment does not change a certificate.

**The certificate schema is now `1.1.0`** — `effectiveGrade`, `waived`, and
`policyDigest` were added. Nothing was removed or given a new meaning, so a
consumer written against `1.0.0` reads exactly what it read before.

### Fixed

- **The `fs` provider tested its include predicate against the absolute path on
  disk**, so a policy `ignore` of `legacy/**` matched nothing and the files it
  named were analyzed anyway. Extension checks never noticed, because they only
  look at a suffix. The predicate now sees the path as it appears in the
  changeset, which is what the git and GitHub providers already did.
- **The engine analyzed its own configuration.** `.reversibility.yml` is YAML, so
  the Kubernetes analyzer claimed it and correctly reported `K8S014`/UNKNOWN for a
  document with no kind — meaning adopting a policy graded your repository F
  because of the file you adopted it with.

### Added — earlier in this release

**Git ref resolution.** `revctl check --base <ref>` resolves a changeset from two
git refs instead of two directories, with `--head` defaulting to `HEAD` and path
arguments acting as pathspecs that scope the comparison to a subtree. The
comparison is three-dot (`base...head`), so it runs against the merge base — the
same range a pull request shows.

Content is read out of the object database, never out of the working tree: a
dirty checkout cannot change the certificate. Unchanged siblings in touched
directories come back as context, matching what the GitHub App already supplies,
so the CLI and the app grade the same pull request the same way.

Failures name their fix rather than repeating git's wording — not a repository,
unknown ref, ambiguous ref, and the common CI case of a shallow clone missing the
base commit, which says `fetch-depth: 0`. All of them are exit 2, a run that did
not complete, never a passing grade.

The certificate schema is unchanged.

**Prebuilt binaries.** Releases publish `revctl` and `revsrv` for `linux/amd64`,
`linux/arm64`, `darwin/arm64`, and `windows/amd64`, with a `checksums.txt` covering
all of them. Each target is compiled on a runner of its own architecture, because
`CGO_ENABLED=1` is mandatory and the darwin target needs an SDK that cannot be
put on a Linux runner. Every build verifies that it still classifies a
`DROP TABLE` before it is packaged — a binary that quietly lost the parser would
grade every migration A for lack of findings.

**Automatic major tag.** Publishing `v1.2.0` repoints `v1` at it, so `@v1` means
the newest v1.x without a manual step after every release. Prereleases are
excluded — `v1.2.0-rc.1` never becomes what `@v1` resolves to.

### Changed

**The GitHub Action is now a composite action, shipped in `v1.1.0`.** The Docker
container action at `v1.0.0`–`v1.0.2` is frozen, not removed.

The action downloads the released binary for whichever runner it is on and
verifies its SHA-256 against the release's `checksums.txt` before executing it. A
mismatch, or a missing checksum line, installs nothing and fails the job. The
practical gain is that the action is no longer restricted to Linux runners and no
longer pays a container pull per run.

It also analyzes a git range now rather than a reconstructed pair of directories.
That fixes a real gap: the v1 staging pass filtered deletions out of the changeset,
so `K8S003`, `K8S006`, and every other removal rule could not fire on a pull
request. They now do.

New inputs: `base`, `format`, `sarif-upload`, `version`, and `config` — the last
reserved for the policy file and currently an error rather than a no-op, because a
policy file that was silently ignored would leave a user believing their waivers
applied. New outputs: `gate-status`, `findings-count`, `certificate-path`.
Findings are additionally emitted as annotations and appear inline on the diff.

`min-grade` is now `gate` and `include` is now `path`. Both old names still work
and emit a deprecation warning. Setting a name together with its replacement is an
error rather than a silent choice between them.

### Removed

**`entrypoint.sh`.** It existed to drive the container action, which the composite
action replaces. The published image remains, with `revctl` itself as its
entrypoint, as the way to run the CLI without installing a toolchain.

## [0.1.0] - 2026-08-20 — Initial Release

First public release. Usable end to end; the certificate schema and CLI flags may
still change before 1.0.

### Added

**The engine.** Static analysis that grades whether a changeset can be rolled
back, emitting a `ReversibilityCertificate` with a grade of A, B, C, or F, a
concrete undo plan, and an explicit list of what cannot be undone. Fail-closed by
construction: an error, a panic, an unparseable file, or an unrecognized
construct all grade F.

**PostgreSQL analyzer** — 27 rules (`PG001`–`PG027`) classified over a real
PostgreSQL AST via `pg_query_go`, never regex. Distinguishes `DELETE` with and
without `WHERE`, narrowing from widening type changes, `CONCURRENTLY` from not,
and constraints added `NOT VALID`. Validates down migrations at three levels:
existence, parseability, and create/drop symmetry (advisory).

**Kubernetes analyzer** — 15 rules (`K8S001`–`K8S015`) over a structural manifest
diff, matched by apiVersion, kind, namespace, and name. Covers volume claim
templates, selector immutability, PVC storage and storage-class changes, digest
pinning, dangling ConfigMap and Secret references, and removed probes.

**Scoring and certificates** — grade assembly with assign-then-cap semantics,
undo-plan generation in reverse order of application, and a SHA-256 input digest
that attributes a certificate to an exact changeset.

**`revctl`** — CLI over the engine. Formats: `json` (the public schema),
`markdown` (pull request comments), `sarif` (GitHub code scanning). Exit codes
are the CI contract: `0` success, `1` gate not met, `2` run did not complete.

**`revsrv`** — GitHub App webhook server. Validates `X-Hub-Signature-256` with a
constant-time comparison, posts the certificate to the pull request and updates
its own previous comment rather than adding a new one, and fetches unchanged
context files from the base commit so the rules that need them are not blind.

**`pkg/certificate`** — the public, versioned wire schema at `1.0.0`, so external
consumers have a stable contract without importing anything internal.

**Determinism** — identical input produces a byte-identical certificate. No
timestamps, no run IDs, no hostnames, no map-iteration order. Verified by a test
that runs the engine 100× over every fixture.

### Security

- **Parser stack-overflow guard.** A chained SQL expression of a few thousand
  operators overflowed the C parser's stack — a hard process crash inside cgo
  that `recover()` cannot catch, making a ~10 KB pull request a remote denial of
  service against `revsrv`. Structurally extreme input is now refused before it
  reaches cgo and reported as `PG027`/`UNKNOWN`, which grades F. Found by
  fuzzing. See [ADR/0001](ADR/0001-parser-choice.md).
- **Multi-document manifest truncation.** `sigs.k8s.io/yaml` decodes only the
  first document of a stream and returns a nil error, which would have silently
  hidden every object after the first in rendered Helm output. Documents are now
  split by a real YAML stream decoder.
- **Fail-closed on network failure.** A rate limit or API error while fetching a
  changeset produces a grade F certificate naming the failure, and posts it.
  Analyzing whichever files happened to arrive is never an option.
- **Markdown injection.** Finding text is attacker-controlled in any pull request
  from a fork. Backticks, pipes, and newlines are neutralized so a statement
  cannot escape its code span, while `<`, `>`, and `<=` survive intact inside it
  so SQL stays readable.

### Fixed

- **Verdict determinism.** The Postgres schema tracker processed files in slice
  order, so the same `ALTER` in two files produced different rationales depending
  on arrival order — the same grade, different certificate bytes. Both analyzers
  now sort internally. Found by fuzzing; the minimized input is committed as a
  regression seed.
- **Unrecognized verdicts.** An analyzer returning an empty or misspelled
  `Reversibility` merely failed the "all REVERSIBLE" check and capped the grade at
  B. A classification nobody can read is what `UNKNOWN` is for; it now grades F.

### Known limitations

- Resolving a changeset from a git ref is not implemented. The filesystem
  provider compares paths, not revisions.
- Context-file lookup is bounded to directories the change already touches, so a
  rule whose context lies elsewhere returns `UNKNOWN` rather than guessing.
- No tagged binaries or container images are published yet; install from source.
- Kubernetes findings carry no line numbers — a structural diff has no single
  line to blame.

[Unreleased]: https://github.com/VIKOIT/reversibility-engine/compare/v1.2.1...HEAD
[v1.2.1]: https://github.com/VIKOIT/reversibility-engine/compare/v1.2.0...v1.2.1
[v1.2.0]: https://github.com/VIKOIT/reversibility-engine/compare/v1.1.2...v1.2.0
[v1.1.2]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v1.1.2
[0.1.0]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v0.1.0
