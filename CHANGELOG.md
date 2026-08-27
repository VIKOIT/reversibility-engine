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

### Added

**Coverage: a second axis for the part of a changeset the engine could not
read.** Certificate schema bumped to `1.6.0`.

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
Certificate schema bumped to `1.5.0`.

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

[Unreleased]: https://github.com/VIKOIT/reversibility-engine/compare/v1.1.2...HEAD
[v1.1.2]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v1.1.2
[0.1.0]: https://github.com/VIKOIT/reversibility-engine/releases/tag/v0.1.0
