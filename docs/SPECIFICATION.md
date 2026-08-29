# Specification — Reversibility Engine

**This file is the contract.** A fresh session with zero memory of prior work must be able to
read this file alone and continue correctly. If something here conflicts with your instinct,
this file wins. If something is not here, do not invent it — ask.

---

## 1. What this is

A static-analysis engine that measures whether a code change can be safely rolled back,
**before it is merged**. It reads PostgreSQL migrations, rendered Kubernetes manifests, and
Terraform plans. It emits a `ReversibilityCertificate` (grade A/B/C/F plus an undo plan) and acts
as a merge gate.

Autonomous AI agents may merge **only on grade A**.

Two interfaces over one decoupled core:

| Binary | Purpose |
| --- | --- |
| `revctl` | Open-source CLI (cobra) |
| `revsrv` | GitHub App webhook server (stdlib `net/http`) |

## 2. The philosophy: fail-closed

**The product sells trust. A wrong "safe" verdict destroys it.**

- Unknown means unsafe.
- Never guess.
- An error, a panic, an unparseable file, or an unrecognized construct **must never** become a
  passing grade. Every one of those paths terminates in **F**.
- There is no "probably fine". There is `REVERSIBLE`, `COSTLY`, `IRREVERSIBLE`, `UNKNOWN` — and
  `UNKNOWN` fails.
- **A gate must prove it ran. No certificate produced means exit 2, never exit 0.** Absence of
  output is never success.
- **The engine never emits a passing grade for a changeset it did not analyze. Absence of
  analysis is not evidence of safety.**

The last two are newer than the rest and were learned the expensive way, so it is worth saying
why neither is implied by the others. Every rule above governs what happens once a change has
been read. None of them says anything about a run that read nothing at all — and that run had
the most permissive outcome in the system, because "no findings" and "no analysis" produced the
same green check. The `:v1` image incident (§11e) was exactly this: no rule misfired, no grade
was wrong, there was simply no grade, and no grade passed.

So the invariant is about the **shape of the run**, not the verdict:

- `revctl` with no arguments exits **2**, not 0. It prints help to stderr, because stdout is
  where a certificate goes.
- The action exits **2** if no certificate file was written, and again if the verdict cannot be
  read back out of one.
- An image whose no-argument invocation exits 0 is not published.
- Anything invoking a container names `--entrypoint` and its arguments explicitly. An inherited
  entrypoint is a silent dependency on a value somebody else can change.

Each of those is enforced by a test that fails when the gate stops gating, not by convention.

### The same failure one level in: a run that read files and analyzed none of them

Those rules all describe a run that produced no certificate. None of them said anything about a
run that produced one *over content no analyzer understands*, and that run had the same
permissive outcome for the same reason: the scoring rules mapped an empty changeset to grade
**A**, `Applicable: false`, gate **PASS**. A pull request of thirteen Django `.py` migrations
was therefore certified fully reversible by an engine that had not read one of them.

**This is the second occurrence of the class, not the same mistake twice.** The first was the
`:v1` image above. Both were specified deliberately, both were reasoned about in writing, and
both shipped — which is the evidence that the invariant was missing from the architecture
rather than that the reasoning slipped on two occasions. `Applicable: false` even had its
justification recorded: that a completed run with an empty answer is distinguishable from a
broken one *because a certificate exists to say so*. The justification was wrong, and it was
wrong in a way worth naming, because the next case will be defended the same way. The
certificate did exist. It said **A / PASS** — and no reader, no merge bot, and no branch
protection rule distinguishes that from a real A.

So the grade itself has to be able to say "no answer". **`A` means analyzed and found
reversible, and nothing else.** A changeset the engine did not analyze gets `Grade: N/A` and
`AIGateStatus: NOT_APPLICABLE` — never `A`, never `PASS`. The three-way outcome that replaces
the empty-changeset rule, and the exit code each outcome produces, live with the rest of the
scoring in [`docs/RULES.md` §3](RULES.md#3-scoring).

The general form, which is the part a future session should apply to a case not listed here:

> Every path that can reach a verdict must be asked what it returns when it reaches that
> verdict having examined nothing. If the answer is the permissive one, the verdict type is
> missing a value — and adding a branch to the permissive answer will not fix it, because the
> next path to that verdict will not have the branch.

### Enumeration and retrieval are separate concerns

> **A provider must be able to report what exists without fetching it.** Any check that describes
> the **shape** of a changeset depends on enumeration; any check that describes **content**
> depends on retrieval. Conflating them makes the first check unable to see what it is measuring.

`FileProvider` had one method, `ChangedFiles(ctx, ref)`, and took a `func(path string) bool` that
decided what to read. A path the predicate rejected was never read and left **no trace anywhere**
— so every question of the form *"what was in this changeset that we did not analyze"* was
unanswerable by construction. Three defects came out of that, each found and fixed separately
before the pattern was visible:

1. **The Django P0.** A docs-only pull request and thirteen unreadable `.py` migrations arrived
   at the engine as the same empty file list, because the predicate admitted neither. The engine
   graded both **A**.
2. **The coverage denominator.** Coverage counted only the files an analyzer wanted, which made
   its numerator and its denominator the same number and the check vacuous.
3. **The rename bypass.** Renaming `migrations/` to `db/sql/` turned strict coverage off, because
   *"read this README because a sibling is a `.sql`"* is not a decision a per-path predicate can
   express. One command, no trace on the certificate.

The interface is now two methods, `List` then `Read`, and **nothing new is computed**: the
filesystem walk already visited paths without reading them, `git diff --name-status` already
returned names before blobs, the GitHub comparison API already returned the listing in the same
response, and the fake always had the fixture directory. The old contract simply refused to
expose what all four already knew.

The choice of what to read moved from a per-path predicate to a `Select` that receives the
**complete listing**, which is what makes the second clause of "is this a migration directory"
enforceable at all.

**The cheap alternative was rejected, not overlooked.** Admitting every file and filtering inside
the engine would make `revctl check .` read the whole repository and the GitHub App fetch every
file in every touched directory. That is an API-quota decision, and it is not one to make as a
side effect of a coverage fix.

### Path-keyed decisions use one namespace

> **Path-keyed decisions use one namespace, and it is not the one the caller typed.** A decision
> that depends on how the analysis root was named is a decision that changes answer for the same
> files.

This is the general form of the rule below it, and it is stated separately because fixing the
specific case left the cause in place. Candidate detection was corrected to resolve paths against
the repository; `ignore:` globs, waiver `path:` globs and `--terraform-plan` went on matching
against the changeset's spelling. **Two namespaces in one decision surface is what produced the
P0, and keeping one of them was keeping the cause.**

The namespace is the **project**: the file's path relative to the root of the checkout or the
directory holding `.reversibility.yml`. That choice is not arbitrary — it is the namespace the
rule tables are written about (*"a path segment named `migrations`"* means in the repository) and
the one users write globs in (relative to the policy file, which sits at the root). Anchoring at
the filesystem root would be equally self-consistent and would make a checkout parked under
`~/migrate/` read every `.py` beneath it as a migration. See §16.10 for the anchor and §16.13 for
the rest.

### A fuzzy match is a review finding

> **Any suffix, prefix, contains, or bidirectional match on a path, identifier, or name requires a
> written justification. The default assumption is that it is compensating for two namespaces, and
> the fix is to unify them rather than to match loosely.**

This is a review criterion, not an observation, because it is the one thing that would have found
all four namespace defects from the outside without knowing anything about migrations.

The Terraform analyzer compared `--terraform-plan` to the changeset path by suffix, in both
directions, and **said so in a comment**: the two spellings "differ". That comment is the finding.
A fuzzy match is what a correct comparison looks like after somebody has noticed the two sides do
not agree and has decided to tolerate it rather than fix it — so the loose match is not the bug,
it is the *report* of the bug, filed in a code comment where no reviewer treats it as one.

It also fails in the permissive direction, which is why this is a rule and not a style note:
`a/plan.json` suffix-matched `b/a/plan.json`, so naming one plan could hand an analyzer a file
nobody named and fail a changeset on it.

**What a justification has to say**, when a loose match really is right: which two things are being
compared, why they are legitimately in different namespaces, and why unifying them is not
available. "Convenience" and "the paths differ" are the two answers that mean the finding stands.
A hostname suffix in a certificate check, a rule-ID prefix in a table lookup, a filename extension
— those are fine and take one line to say why.

**The caller's naming survives in exactly one place, and that place is output.** A qualified path
outside a project is an absolute path, so a certificate carrying one would stop being
byte-identical between machines — the same class of input as a timestamp or a hostname. So:

> **The qualified namespace classifies; it never renders.**

Enforced by the type, not by discipline: `domain.Located` is a distinct type, every path-keyed
function takes it, and handing one a raw changeset path is a compile error. That converts "somebody
forgot" into "somebody wrote a conversion", and a conversion is visible and greppable where a
forgotten qualification was nothing at all.

**Every place a path is compared, and what namespace it uses** — the audit §16.13 required,
recorded here so the next contributor extends it rather than repeating it:

| Where | Namespace | Why |
| --- | --- | --- |
| `engine.Candidate`, `InMigrationDir`, `migrationDirectories`, `outcome` | **Located** | The P0. Keys on a directory segment above the file. |
| `policy.Ignores` / `IgnoreMatcher` | **Located** | Globs are written against the project. |
| Waiver `path:` (`policy.waiverFor`) | **Located** | Same, and §16.13 changes S10 semantics here. |
| `analyzer.Supports` — all three | **Located** | Postgres and Kubernetes read the extension only and would be right either way; Terraform compares `--terraform-plan` against a file and would not. |
| `--terraform-plan` values | **Located**, via `provider.QualifyPath` | Named relative to the user's shell; resolved at the boundary, where I/O is allowed. |
| Down-migration pairing (`postgres/migration.go`) | changeset-relative, **invariant** | Pairs by `path.Dir` relative to each other. A uniform prefix shifts both sides equally. |
| `policy.IsPolicyFile` | base name, **invariant** | Compares the file name and nothing else. |
| Context-sibling lookup (`git.go`, `github.go`) | provider-internal, **invariant** | Both sides come from one listing, so it is self-consistent by construction. |
| `skipDir`, fake-provider skips | base name, **invariant** | Enumeration decisions. They define the namespace rather than deciding within it. |
| `RenderingRemedy`, `unsupportedContentBlockers`, every renderer | **as the caller named it** | Output. Deliberately not Located — see the determinism rule above. |
| Action `exclude` inputs | git pathspec, **invariant** | Resolved by git in the repository, which is the same namespace. |
| Snapshot enrichment | not a path at all | Matches on table and column names. |

### Candidate detection must not depend on how the analysis root was named

> **Candidate detection must not depend on how the analysis root was named.**
> `check ./migrations` and `check .` from its parent must reach the same outcome for the same
> files.

This is the P0's third appearance and it is worth being precise about why it is not a repetition.
The rules above govern a run that **read nothing**. This run read everything, classified it
correctly, and classified it over a path with a segment missing:

```
revctl check django/contrib/auth/migrations --gate  -> NO_CANDIDATES, exit 0
revctl check django/contrib/auth            --gate  -> UNSUPPORTED_CONTENT, exit 2
```

`check ./dir` reports paths relative to the named directory, and
[`docs/RULES.md` §3](RULES.md#3-scoring) keys the migration predicate on a path segment named
`migrations` — the exact segment relativisation had just removed. **The permissive answer was the
one the documented invocation reached**, which is why it survived a fix, a hardening pass, and a
property test: everything was asked whether the engine had read the files, and nothing was asked
whether it knew where they were.

The general form, for the case not listed here:

> A predicate over a path is a predicate over a **namespace**. If a caller can change the
> namespace by changing an argument, the predicate has an input nobody declared — and the value
> that argument takes in the documented invocation is the one that will ship.

The mechanism is `provider.RootPrefix` and `engine.RootedAt`, anchored at the repository, and the
reasoning for that anchor over the two alternatives is in §16.10. One consequence is load-bearing
and stated here because it constrains every future use: **the qualified namespace classifies and
never renders.** A path on a certificate is the path the caller named, or the certificate stops
being byte-identical across machines.

### All diagnostics go to stderr; stdout carries the certificate

> **All diagnostics, deprecations and warnings go to stderr. stdout carries the certificate and
> nothing else, in every format.**

The instance: a single `MarkDeprecated` call. pflag records the warning in cobra's
`flagErrorBuf`, cobra flushes it through the *out* writer, and the out writer is stdout — so
`revctl check --format json --require-full-coverage` printed a line of English ahead of the
certificate and stdout stopped being parseable JSON.

**The flag was kept accepted so that an upgrade would not become an unknown-flag error, and
keeping it turned the upgrade into a parse error instead.** That is the part to carry forward:
a compatibility measure has its own failure mode, and it is not automatically milder than the
failure it prevents. An unknown flag names itself; a JSON syntax error does not.

Note also what this rule does *not* say. Help asked for by name is output, not a diagnostic, and
it stays on stdout — which is why the fix was to stop using cobra's mechanism rather than to
redirect cobra's writer. See §16.11.

### Classification is by effect, never by statement type

> **Classification is by effect, never by statement type. A construct is classified by what it
> does to data and schema, not by the node the parser returns. Where a statement type can carry
> a destructive effect — data-modifying CTEs, function calls with side effects, dynamic SQL — it
> is classified by that effect or it is UNKNOWN.**

This one was found before it shipped, which is the only reason it is written here rather than in
an incident note. The dialect triage was about to propose a rule that looks obviously harmless —
*a `SELECT` changes nothing, so classify it REVERSIBLE* — and `SELECT` is a **node type, not an
effect**:

| Statement | The node | What it does |
| --- | --- | --- |
| `SELECT count(*) FROM orders` | `SelectStmt` | nothing |
| `SELECT setval('orders_id_seq', 1000)` | `SelectStmt` | resets a sequence — the PG010 hazard |
| `WITH d AS (DELETE FROM orders WHERE id < 5 RETURNING *) SELECT count(*) FROM d` | `SelectStmt` | **deletes rows** |

Both destructive rows graded F before the rule was proposed, and both graded F **by accident**:
they were unrecognised, and unrecognised is UNKNOWN. A rule keyed on the node type would have
converted two accidental correct answers into two deliberate wrong ones, and it would have been
merged, because on its face it says only that reading data is safe.

**Accident is not a safety property.** The two cases are now classified by their effect — see
[`docs/RULES.md` §1](RULES.md#1-postgresql--pg001-to-pg059) — and every other `SELECT` stays
UNKNOWN. **No permissive default was added**, because the generalisation that would justify one
is exactly the generalisation that is wrong.

The same trap exists wherever a node can carry an effect it is not named for: `INSERT ... ON
CONFLICT DO UPDATE` is an `InsertStmt` that overwrites rows, and a `SELECT` calling a volatile
function can do anything the function does. A future contributor will try to simplify these
cases into one. The rationale on each finding says why they are separate.

### The named principles, and why naming them is the work

**A named principle stops the next twenty questions before they are asked; a rule answers one.**
Every entry below started as several rules quietly following the same shape, and was named only
after the shape recurred — which is the trigger to look for, and the reason this index exists
rather than the principles living only where they were first needed.

| Principle | Where | What it settles |
| --- | --- | --- |
| Fail-closed: unknown means unsafe | §2 above | Any construct, error, or panic the engine does not understand |
| A gate must prove it ran | §2 above | Exit codes over absent output |
| No passing grade for a changeset it did not analyze | §2 above | `Outcome`, `N/A`, `NOT_APPLICABLE` |
| Classification is by effect, never by statement type | §2 above | Data-modifying CTEs, `setval`, `ON CONFLICT DO UPDATE` |
| The grade describes the evidence; the gate decides what to do about it | §2 below | Waivers, coverage, policy ignores |
| Any field that constrains the verdict appears in every rendered output | §2 above | `GradeCauses`, and what a certificate owes a reader |
| Every classification has a table row | §13 | The D2 shape: code claiming a construct the table omits |
| Enumeration and retrieval are separate concerns | §2 above | The Django P0, the coverage denominator, and the rename bypass — one root cause |
| Path-keyed decisions use one namespace | §2 above | Candidates, ignore globs, waiver paths, `Supports`, `--terraform-plan` — and what may be rendered |
| A fuzzy match is a review finding | §2 above | Any suffix, prefix or contains match on a path, identifier or name |
| Candidate detection must not depend on how the analysis root was named | §2 above | `check ./migrations` vs `check .`; the instance the general rule came from |
| Any number in documentation duplicating the specification is derived by test | §2 below | The rules badge, catalog counts, schema version, rule ID ranges |
| All diagnostics go to stderr; stdout carries the certificate | §2 above | Deprecations, warnings, gate reasons — and what `--help` is not |
| A refusal must name the way forward | §2 below | `UNSUPPORTED_CONTENT`, coverage-`PARTIAL`, and the ORM rendering path |
| The overwrite principle | [`RULES.md` §1](RULES.md#the-overwrite-principle) | PG012, PG013, PG028, PG043, PG050, PG054, PG057 |
| `CONCURRENTLY` changes the lock, never the verdict | [`RULES.md` §1](RULES.md#concurrently-changes-the-lock-and-never-the-verdict) | Four rule pairs, and which encoding to use next |
| Creation and destruction are not mirrors | [`RULES.md` §1](RULES.md#creation-and-destruction-are-not-mirrors) | Five pairs where symmetry reads as safety |
| An undo step must be safe to run, not merely correct | [`RULES.md` §1](RULES.md#an-undo-step-must-be-safe-to-run-not-merely-correct) | PG028, PG029, and any inverse that destroys on the way |
| The third clause of the discriminator | [`RULES.md` §5](RULES.md#the-discriminator) | TF004 and PG052 — one principle, two analyzers |

**Every named principle also names its exceptions.** PG033 is the deliberate exception to the
overwrite principle; PG032 is the deliberate exception to create/destroy asymmetry. An unmarked
exception is indistinguishable from a bug, and a principle with no exceptions listed is usually
one that has not met a hard case yet.

### The pattern: the grade describes the evidence, the gate decides what to do about it

**This is the project's answer to a whole class of question, and it has now been applied
twice.** When something that is not evidence about the change wants to influence the verdict, it
goes on a second axis and moves the gate. It never moves the grade.

| Session | The pressure | Where it went |
| --- | --- | --- |
| S10 | A team has accepted a risk and wants their pipeline unblocked. | `EffectiveGrade` and the exit code. `Grade` is untouched, and `AIGateStatus` follows `Grade`, so a waiver can never authorise an agent to merge. |
| P0 follow-up | Part of the changeset could not be read, and something should reflect that. | `Coverage` and `AIGateStatus`. `Grade` is untouched, and PARTIAL never moves it. |

The two push in opposite directions — a waiver argues for leniency, a coverage gap argues for
severity — and the answer is the same, which is what makes it a pattern rather than two ad-hoc
rulings. **A grade that configuration can improve stops meaning "reversibility". A grade that
ignorance can worsen stops meaning it just as thoroughly**, and it fails in the direction that is
harder to notice, because a tool that over-reports looks conscientious. Inventing severity from
ignorance is the mirror image of inventing safety from it, and this document has now been wrong
in the second direction once.

The next time something wants to change a grade and is not evidence about the change, the answer
is a new axis, not a new cap.

**A policy `ignore:` follows the waiver half of this pattern, not the coverage half** (§16.8).
Coverage stays FULL, because the engine was capable of reading the file and was told not to —
coverage describes capability, not permission. The ignore closes the merge gate instead, for the
reason a waiver does: **a human may accept risk with their name on it, and an agent may not
inherit it.** Which mechanism the human used does not change who is allowed to inherit.

### Any field that constrains the verdict must appear in every rendered output

> **Any field the engine computes that determines or constrains the verdict must appear in every
> rendered output, not only in JSON. A reader must never have to open the JSON to learn why they
> were blocked.**

This has the same authority as the two invariants above and it is the same failure one step
further out. Those are about the engine reaching a permissive answer; this is about the engine
reaching a correct answer and then failing to hand over the reason.

The instance that produced it: a capped grade was unexplainable from the certificate a reviewer
actually reads. A changeset with every finding REVERSIBLE could arrive at **C**, and nothing in
the markdown said which condition applied the ceiling — the reader's only recourse was to open
the rule tables and re-derive it, which is precisely the work the engine had already done and
then discarded. `GradeCauses` carries it now: the assignment, then every cap that lowered the
grade, each naming the rule, file, or condition responsible. Grade A says explicitly that
nothing capped it, because *"nothing capped this"* and *"nobody wrote down why"* must not render
identically.

`TestEveryCappedOrFailedGradeNamesItsCauseInMarkdown` holds it over every fixture, and it checks
each cause individually rather than the certificate as a whole — an earlier version checked the
whole and a mutation walked through it, because a vague cap line passed on the strength of a
specific assignment line beside it. **The vague line is the one the reader is stuck on, so it is
the one that has to be specific.**

### A refusal must name the way forward

> **A refusal a reader cannot act on is one they will route around.** Where the engine declines
> to certify because of something the reader can change, the message names the change. Where it
> declines because of a limit of the engine itself, the message names the supported way around
> that limit — or the honest fact that there is none.

This is the last field-report finding and it is the one that is not about correctness at all.
The audit's headline was that 85% of the corpus is un-gradeable and 100% of graded runs are
blocked at `--gate`, so a Django or Rails team has no path to a passing grade — the engine will
never parse `.py` or `.rb`.

**That verdict is correct and it is not weakened anywhere in this document.** What follows from it
is a second fact, about people rather than about migrations: **a gate with no path to green gets
uninstalled, and a gate nobody runs protects nothing.** Strictness that ends in the tool being
removed is a net loss against the strictness it was bought to buy, and it fails silently, because
an uninstalled gate reports nothing at all.

So the path is documented rather than discovered. Every ORM can print the SQL it is about to run;
rendering it and pointing the engine at the output is a supported workflow, named in the blocker
itself (`engine.RenderToSQL`), in the action's warning, and in the README with a worked example.
Note the two disciplines the rule imposes in the same breath:

- **Name a real command or none.** `.js` and `.ts` migrations get the general sentence and no
  tool-specific line, because node-pg-migrate, Knex, TypeORM and Prisma each spell it differently
  and a command that does not exist is worse than no command.
- **Do not let the remedy soften the verdict.** Rendering plus `ignore:` on the ORM sources exits
  0 and still fails the AI merge gate, because §16.8 is unchanged: a human may accept a risk with
  their name on it and an agent may not inherit it. The README states that outcome beside the
  green one rather than leaving a team to find it after they trusted a passing build.

### A documented number that duplicates the specification is derived by test

> **Any number in documentation that duplicates a fact stated in the specification must be derived
> by test, not maintained by hand.**

The README's rules badge said `27 PG` while the table defined 59. It had been wrong for
thirty-two rules and nothing noticed, because nothing checked it — every other claim about the
tables is verified against them, and the number on the front page was the one claim exempt.

The fix is not a corrected number, it is a test that computes it. `TestTheReadmeBadgeMatchesTheRuleTables`
derives the badge from `docs/RULES.md` and prints the corrected line on failure;
`TestDocumentedNumbersAreDerivedFromTheirAuthority` does the same for the Terraform catalog counts,
the schema version, and the release-target count; `TestDocumentedRuleIDRangesMatchTheTables` does it
for every `PG001–PG059`-style range in prose and in anchors.

Two properties of the mechanism are load-bearing:

- **A claim that has moved fails rather than passes.** If the wording changes so the pattern no
  longer matches, the test fails and says so. A guard that quietly stops guarding is how the
  badge drifted in the first place.
- **The failure states the authority and prints the corrected text**, so a failure is a fix and
  not a research task.

Adding a documented number means adding a row to that table. Anything a reader could act on that
is stated twice — a count, a version, a range, a supported-platform list — belongs in it.

## 3. Scope

**In:** static analysis only. PostgreSQL `.sql` migrations. Rendered Kubernetes manifests
(`.yaml`). Terraform plan JSON (`*.tfplan.json`), added in S12 — **plans only, never
`terraform.tfstate`**, which holds provider credentials and attribute values in plaintext.

**Out — do not build, do not stub, do not leave a TODO for:** live DB rehearsal, Helm chart
templating, Kustomize rendering, `.tf` source parsing, cost estimation, web UI, auth/billing,
AI/LLM calls of any kind, telemetry of any kind.

Two things moved out of this list and the moves are recorded rather than silently applied, so a
future session does not re-litigate either:

| Was out | Now | Ruling |
| --- | --- | --- |
| Terraform | **In**, as of S12 | Bounded to plan JSON and to *destruction only*. A created or updated-in-place resource has a reverse by construction, which is what keeps the resource catalog finite. `.tf` source parsing stays out — it is a different analyzer with a different failure surface. See §11h. |
| Production database access | **In**, as of S11, and only in `revctl snapshot` | The engine still never connects to anything **during analysis**. A separate command writes a snapshot file; the analysis reads the file. Enforced by an architecture test, not by discipline. See §11g. |

Note the shape both exceptions share: the capability was admitted, and then fenced by a test that
fails if it escapes its package. That is the bar for admitting anything else from the Out list.

## 4. Session plan

Execute **one session at a time**. Stop at the end of each and report. Do not start session
N+1 until the owner approves.

| Session | Deliverable | Status |
| --- | --- | --- |
| S0 | Repo bootstrap, the specification, Makefile, CI workflow, module layout with empty packages. No logic. | **DONE** |
| S1 | `internal/domain` types + full test fixtures in `testdata/` + failing tests. No analyzer logic. | **DONE** |
| S2 | Postgres analyzer until all its fixtures pass. | **DONE** |
| S3 | Kubernetes analyzer until all its fixtures pass. | **DONE** |
| S4 | Scorer + certificate assembly + determinism tests. | **DONE** |
| S5 | Renderers (JSON, Markdown, SARIF) + Cobra CLI. | **DONE** |
| S6 | GitHub App webhook server. | **DONE** |
| S7 | Hardening: fuzz tests, panic-recovery boundary tests, golden-file determinism. | **DONE** |

The v0.2 plan continues the same way. Each session is written up in full in
`.claude/commands/s8.md` … `s12.md`, one file per session.

| Session | Deliverable | Status |
| --- | --- | --- |
| S8 | Git ref resolution: a `gitProvider` behind `--base` / `--head`. | **DONE** — shipped in v1.1.2 |
| S9 | GitHub Action (`action.yml`) + release workflow. | **DONE** — shipped in v1.1.2 |
| S10 | Policy file `.reversibility.yml` with expiring waivers. | **DONE** — shipped in v1.1.2 |
| S11 | Production context snapshots (`revctl snapshot`, `--context`). | **DONE** |
| S11-patch | `WILL_FAIL` verdict + lock duration bands. | **DONE** — shipped in v1.1.2 |
| S12 | Terraform plan analyzer. | **DONE** — shipped in v1.1.2 |

Update the Status column when a session is approved as complete. **Publishing a session's work in
a tagged release is that approval**, and is recorded as such above — the v0.2 sessions all shipped
in `v1.1.2` on 2026-08-26 and sat marked "awaiting approval" afterwards, which read as though the
release contained unapproved work.

The v0.2 plan is complete. There is no S13 and no session after it is planned; the next one is
whatever the owner scopes, written up in `.claude/commands/` first as every previous session was.

**Everything after S12 came from a field audit rather than a session, and shipped in `v1.2.0` on
2026-08-29:** the P0 (§16.10 and its two predecessors), 32 further PostgreSQL rules, strict
coverage (§16.7), the two-phase `FileProvider` (§16.9), and the four namespace defects (§16.13).

That release is itself worth recording as a rule, because the gap it closed was twelve commits
wide: **the P0 fix, the rules, and strict coverage all sat in `main` and in nobody's CI.** A tool
that improves faster than it releases is one nobody is using, and every one of those improvements
was worth exactly nothing to a consumer until the tag existed. **Ship before the next
investigation**, not after it.

## 5. Layout

```
cmd/revctl/                          CLI entrypoint (cobra)
cmd/revsrv/                          GitHub App server entrypoint
internal/domain/                     Types only. ZERO third-party imports.
internal/fixture/                    Fixture + expectation loader (test support; see note below)
internal/analyzer/                   Analyzer interface
internal/analyzer/postgres/
internal/analyzer/postgres/parser/   SQLParser interface, isolates cgo pg_query_go
internal/analyzer/kubernetes/
internal/analyzer/terraform/         Plan JSON. Reads plans only, never tfstate.
catalog/terraform/aws.yaml           Resource-type classifications, embedded (public path
                                     on purpose: contributors have to be able to find it)
internal/engine/                     Registry, orchestrator, scorer
internal/provider/                   FileProvider interface: fs, git, github, fake
internal/policy/                     .reversibility.yml: parse, validate, apply
internal/snapshot/                   Snapshot types, canonical JSON, enrichment. NO drivers.
internal/snapshot/collect/           pgx + client-go live here and ONLY here
internal/render/                     json, markdown, sarif
internal/delivery/cli/
internal/delivery/github/
pkg/certificate/                     Public, versioned certificate schema
testdata/fixtures/
```

**The shipping surface, which is part of the contract and not packaging detail.** The action is
what most consumers actually run, so a change here can break a gate as surely as a change to a
rule can:

```
action.yml                           COMPOSITE action at the repo root. Not a Docker action
                                     — see §11e. Names every input, including the deprecated
                                     aliases that are read but never merged.
action/install.sh                    Downloads the release binary for the runner's OS/arch and
                                     REFUSES to run anything failing its checksums.txt line.
action/resolve.sh                    Decides which build this run needs, the asset name and
                                     the cache key. Downloads NOTHING — separating the decision
                                     from the fetch is what lets the cache step sit between them.
action/certify.sh                    Detects the base ref from the event (a PR uses its base
                                     SHA, a push the commit it moved from), runs revctl, and
                                     reads the verdict back out of the JSON. Exit 2 with no
                                     certificate, and exit 2 if the grade cannot be read out of
                                     one. Checks for jq up front.
action/comment.sh                    Posts/updates the single marked pull request comment.
.github/workflows/release.yml        Four native targets, one runner each. No -ldflags stamping.
                                     A final job moves the v1 tag and reads it back.
.github/workflows/publish-image.yml  Immutable version image tags ONLY. Refuses to publish an
                                     image whose no-argument run exits 0.
.github/workflows/action-selftest.yml  Runs the action against this repo's fixtures and asserts
                                     a grade F actually fails the job.
.github/workflows/restore-image-tag.yml  Repairs an image alias from a runner, digest read back.
```

**`internal/delivery/` is a thin shell, but `action/` is not part of it and must not import
anything.** It is POSIX shell over the released binary's documented CLI contract — exit codes and
the JSON certificate — which is exactly why it can be a composite action at all. If a change
requires `action/` to know something the CLI does not expose, the CLI is what needs to change.

## 6. Dependency rules

**Hard rules — a violation is a bug, not a style preference:**

1. `internal/domain` imports **nothing** outside stdlib. Everything else depends on it; it
   depends on nothing.
2. Analyzers receive `[]domain.ChangedFile` and return `([]domain.Finding, error)`. They
   **never** touch network, disk, git, or GitHub. All I/O happens in a `FileProvider`.
3. Transport (`internal/delivery/`) is a thin shell. **Deleting `internal/delivery/` must not
   break engine tests.**
4. Accept interfaces, return structs.
5. Zero global mutable state.

**Allowed third-party dependencies — the complete list:**

| Module | Used by | Introduced in |
| --- | --- | --- |
| `github.com/spf13/cobra` | `internal/delivery/cli` | S5 (in go.mod) |
| `github.com/pganalyze/pg_query_go/v5` | `internal/analyzer/postgres/parser` | S2, **cgo** (in go.mod) |
| `github.com/google/go-github/v66` | `internal/delivery/github`, `internal/provider` | S6 (in go.mod) |
| `sigs.k8s.io/yaml` (JSON-typed decode) + `gopkg.in/yaml.v3` (stream decoder) | `internal/analyzer/kubernetes`; `sigs.k8s.io/yaml` alone in `internal/policy` and `internal/analyzer/terraform` (the resource catalog) | S3, S10, S12 |
| `github.com/jackc/pgx/v5` | `internal/snapshot/collect` **only** | S11 |
| `k8s.io/client-go` (+ `k8s.io/api`, `k8s.io/apimachinery`) | `internal/snapshot/collect` **only** | S11 |
| `github.com/google/go-cmp` | tests only | S1 |

**The two S11 drivers are quarantined by a test, not by convention.**
`internal/snapshot/architecture_test.go` fails the build if `internal/domain`,
`internal/analyzer/...`, `internal/engine`, or `internal/snapshot` can reach either one through
any number of hops — and separately asserts that `internal/snapshot/collect` still does, so the
guard cannot become vacuous. client-go is heavy: it took the module graph from 25 to 92. That
cost buys the `--kube-context` collector and is paid by the `snapshot` command alone; analysis
links none of it.

Versions are pinned to the newest that still declare `go 1.22` — `pgx v5.6.0` and
`client-go v0.31.13`. A plain `go get …@latest` silently rewrites the module's own go directive
to 1.25 and breaks `golang:1.22-bookworm`; if that happens, pin back rather than bumping the
toolchain without a decision.

Adding anything else requires the owner's approval. `go.mod` is intentionally dependency-free
until the session that first needs a given module — do not pre-add them.

## 7. Core interfaces

```go
type Analyzer interface {
    Name() string
    Supports(path string) bool
    Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error)
}

type FileProvider interface {
    ChangedFiles(ctx context.Context, ref domain.ChangeRef) ([]domain.ChangedFile, error)
}
```

`FileProvider` is implemented **four** times: `fsProvider` (local dir/diff), `gitProvider`
(two refs, S8), `githubProvider`, `fakeProvider` (reads `testdata/`). Never write "simulated" or
placeholder fetch code — use `fakeProvider`.

## 8. Domain types

```go
type Reversibility   string // REVERSIBLE, COSTLY, IRREVERSIBLE, UNKNOWN, WILL_FAIL
type LockHazard      string // NONE, SHORT, FULL_SCAN, TABLE_REWRITE, EXCLUSIVE
type Grade           string // A, B, C, F, N/A
type AnalysisOutcome string // ANALYZED, NO_CANDIDATES, UNSUPPORTED_CONTENT
type Coverage        string // FULL, PARTIAL — a second axis; never folded into Grade

type Finding struct {
    RuleID        string        // stable, e.g. "PG001"
    File          string
    Line          int
    Statement     string        // normalized, truncated to 200 chars
    Reversibility Reversibility
    LockHazard    LockHazard
    Rationale     string        // why, in one sentence
    UndoStep      string        // "" if none possible
}

type ReversibilityCertificate struct {
    SchemaVersion  string    // "1.5.0" — bump on any breaking field change
    Grade          Grade     // the measurement; nothing may move it. N/A when nothing was analyzed
    EffectiveGrade Grade     // Grade minus waived findings; what CI compares (S10)
    AIGateStatus   string    // PASS | FAIL | NOT_APPLICABLE. PASS needs Grade A AND Coverage FULL
    Outcome        AnalysisOutcome  // what the run was able to do at all; see docs/RULES.md §3
    Coverage       Coverage         // how much of it was read; moves the gate, never the grade
    UnanalyzedFiles []UnanalyzedFile // every file not read, and why. Empty when Coverage is FULL
    Applicable     bool      // Outcome == ANALYZED, kept for consumers pinned to 1.4.0
    InputDigest    string    // SHA256 over sorted (path, content), plus the policy and
                             // the catalog when either was used
    PolicyDigest   string    // SHA256 over the resolved policy, "" if none (S10)
    CatalogVersion string    // catalog that produced a verdict, "" unless one was used (S12)
    Findings       []Finding // sorted by File, then Line, then RuleID
    Waived         []WaivedFinding // findings a live waiver accepted (S10)
    UndoPlan       []UndoStep
    Blockers       []string  // human-readable reasons for F
    ContextWarnings []string // stale snapshots and the like (S11)
    PolicyWarnings  []string // config that did nothing: a dead ignore glob or waiver
}
```

**The schema version is the number downstream gates pin against, so its history is recorded
here.** It lives in exactly one place, `domain.SchemaVersion`, re-exported as
`certificate.SchemaVersion`, and `revctl version` prints it.

| Version | Added | Session |
| --- | --- | --- |
| `1.0.0` | the original certificate | S4 |
| `1.1.0` | `EffectiveGrade`, `Waived`, `PolicyDigest` | S10 |
| `1.2.0` | `Finding.Context`, `ContextWarnings` | S11 |
| `1.3.0` | `WILL_FAIL` — a new value in an existing enum | S11-patch |
| `1.4.0` | `CatalogVersion` | S12 |
| `1.5.0` | `Outcome`, `Coverage`, `UnanalyzedFiles`, `IgnoredByPolicy`, `GradeCauses`, `PolicyWarnings`; `Grade` gained `N/A`; `AIGateStatus` gained `NOT_APPLICABLE`, and a `PASS` now also requires full coverage and no policy-ignored candidate | P0 and its follow-ups |

**`1.5.0` covers five separate changes and it is deliberately one version.** They were developed
as 1.5.0, 1.6.0 and 1.7.0, and collapsed before release because no consumer ever saw the
intermediates. Three version numbers that never meant anything outside this repository would
read, from a changelog, as three schemas a consumer might encounter. `PolicyWarnings`, `PathAnchor`
and `PathPrefix` were folded in later on the same reasoning and by the same rule below: while
1.5.0 was unreleased it was still a working note rather than a promise. **It shipped in `v1.2.0`
and is now frozen — the next change to the schema bumps it.**

The rule this settles, because it will come up again: **the honesty requirement applies to
versions a consumer can observe. An unreleased intermediate is a working note, not a promise.**
Once a version ships, it is frozen and the next change bumps.


Two of these warrant attention rather than a footnote, and they are the same kind of bump: a
consumer switching exhaustively on an enum gained a case it had not seen. `1.3.0` added
`WILL_FAIL` to `Reversibility`. `1.5.0` added `N/A` to `Grade` and `NOT_APPLICABLE` to
`AIGateStatus`, which is the more disruptive of the two — a gate written as `grade == "A"`
keeps working, but one written as `grade != "F"` starts passing a changeset nobody analyzed.
Consumers must switch on `Outcome`, or compare against `A` explicitly, and that migration note
is the reason the bump is not silent. Every other bump only added optional fields.

Findings gained two fields in S11: `Subject` (how a snapshot is matched to a finding, internal
only) and `Context` (`*FindingContext` — row estimate, size, estimated duration, band, note).
Both are optional and absent unless a snapshot was supplied.

**Reversibility severity ordering** — used for rule precedence and for the one-way ratchet on
enrichment. `WILL_FAIL` outranks everything an analyzer can produce from source alone:

```
REVERSIBLE < COSTLY < UNKNOWN < IRREVERSIBLE < WILL_FAIL
```

**LockHazard severity ordering** — required to evaluate `>=` and `<=` in the scoring rules.
Derived from those rules, not invented:

```
NONE < SHORT < FULL_SCAN < TABLE_REWRITE < EXCLUSIVE
```

**As built in S1, three fields differ from the sketch above.** All three are typing decisions,
not semantic ones; the JSON wire format is unchanged.

| Field | Sketch | As built | Why |
| --- | --- | --- | --- |
| `Finding.UndoStep` | `string` | `UndoStep` | `UndoPlan []UndoStep` is assembled directly from these fields, so they must share a type. |
| `AIGateStatus` | `string` | `GateStatus` | The gate is derived in exactly one place, `Grade.Gate()`. A named type means a typo'd `"Pass"` cannot compile. |
| — | — | `DownMigrations []DownMigrationStatus` | `docs/RULES.md` §1 requires recording which of the three validation levels passed, and the certificate is the only output artifact. See §16.1. |

The domain package also carries enum algebra: `Valid()`, `Severity()`, `LockHazard.AtLeast()`,
`Grade.Rank()`, `Grade.Cap()`, `Grade.Gate()`, and `SortFindings()`. These are properties of the
types, not policy — **no scoring, parsing, or I/O lives in `domain`**, and it remains
stdlib-only.

**Zero values are never safe.** An unset `Reversibility` is invalid, an unset `LockHazard` sorts
above `EXCLUSIVE`, an unset `Grade` ranks below `F` and gates `FAIL`, and an unset
`AnalysisOutcome` does not certify. A finding nobody classified, and a run nobody recorded the
shape of, must never read as harmless. `TestZeroValuesAreNeverSafe` enforces this.

**`N/A` is not a rank.** It is the absence of a measurement, so it is never compared against a
threshold: `Grade("N/A").Rank()` returns the same value an unset grade does, which is below
`F`, precisely so that a caller who forgets to branch on `Outcome` fails closed rather than
passing. The branch lives in exactly one place — `applyGate` — and the exit code it produces
comes from the outcome, never from the rank.

## 9-11, 15. Classification and scoring — moved

**The rule tables, the scoring procedure, and the owner rulings now live in
[`docs/RULES.md`](RULES.md).** They are the product specification and are read by
contributors who are not working through this file, so they were given a document of their own.

| Was | Now |
| --- | --- |
| §9 AUTHORITATIVE Classification — PostgreSQL | [`docs/RULES.md` §1](RULES.md#1-postgresql--pg001-to-pg059) |
| §10 AUTHORITATIVE Classification — Kubernetes | [`docs/RULES.md` §2](RULES.md#2-kubernetes--k8s001-to-k8s015) |
| §11 AUTHORITATIVE Scoring | [`docs/RULES.md` §3](RULES.md#3-scoring) |
| §15 Owner rulings | [`docs/RULES.md` §4](RULES.md#4-owner-rulings) |
| — (new in S12) | [`docs/RULES.md` §5](RULES.md#5-terraform--tf001-to-tf010) — Terraform, TF001–TF010 |

Nothing about them changed in the move except the section numbers, and every code comment that
cited the old numbering was rewritten in the same commit. **Those tables remain authoritative:
do not infer, extend, or soften them, and do not invent a rule that is not written there.**

The section numbers below are unchanged, so §12, §13, §14, §16, and §17 still mean what every
existing reference to them means.

## 11b. Renderers and CLI — as built in S5

- **`pkg/certificate` is the public schema and the JSON renderer emits it**, not the internal
  type. Go forbids importing `internal/`, so without this package no external consumer could read
  a certificate at all. The internal model may be refactored freely; this schema may not.
- **Renderers never re-derive a grade or a gate status.** They print what the engine decided. A
  second definition of the gate would be a second chance to get it wrong in the permissive
  direction.
- **Markdown escaping distinguishes code spans from prose.** Findings carry text from the
  analyzed repository, which is attacker-controlled in any pull request from a fork. Outside a
  code span, `<` and `>` become entities. *Inside* one they must stay raw — SQL is full of `<`,
  `>`, `<=`, `<>`, and entity-escaping would render `&lt;=` literally to the reviewer. Safety
  inside the span comes from neutralizing backticks, pipes, and newlines so the span cannot be
  closed early. `TestMarkdownEscapesUntrustedContent` and `TestMarkdownPreservesSQLOperators`
  hold both halves.
- **SARIF maps UNKNOWN to `error`, not `warning`.** Downgrading it would let a change nobody
  understood pass a code-scanning gate.
- **SARIF reports `executionSuccessful: true` even on grade F.** A correctly detected destructive
  migration is the tool working; reporting otherwise makes code scanning treat it as a broken
  upload.
- **The SARIF tool version is pinned to the certificate schema version, not a build stamp.** A
  version that moved with every build would break byte-identical output.
- **CLI exit codes are the contract with CI:** `0` success, `1` gate not met, `2` run did not
  complete. Keeping "unsafe change" distinct from "broken tool" is what stops a broken tool from
  being ignored. The certificate is still written when the gate fails — it is the artifact the
  user asked for.
- **`cli.Execute` returns an exit code; only `main` calls `os.Exit`.** Streams are injected, so
  the whole command tree is tested in-process.
- **The `fs` provider is given an `Include` predicate by the caller**, built from
  `Engine.Supports`. Knowing which extensions matter is the analyzers' business, and deriving it
  from the registry means a new analyzer widens the net without a second list to update.
- **Two-tree comparison (`--before`) returns unchanged files as MODIFIED with identical sides**,
  matching `fakeProvider`, because K8S003 and K8S009 need context the change did not touch
  (§16.4).
- ~~**Resolving a changeset from a git ref is not implemented.**~~ **RESOLVED in S8** — see §11d.
  The fs provider still compares paths, not revisions; `--base` selects the git provider instead.

## 11c. GitHub App — as built in S6

**Security.** Nothing is parsed, logged, or acted on before the signature is verified.

- **Only `X-Hub-Signature-256` is honoured.** GitHub also sends the SHA-1 `X-Hub-Signature`;
  accepting it would let an attacker downgrade the check by omitting the stronger header. There
  is no negotiation. **Never add a SHA-1 fallback.**
- Comparison uses `hmac.Equal` — constant time. A byte-by-byte compare leaks how much of a forged
  signature was correct, which is enough to recover the rest one byte at a time.
- The body is size-capped (`http.MaxBytesReader`, 25 MiB) *before* it is read, because the body
  is authenticated only after being read in full — an unbounded read is an unauthenticated
  attacker's memory-exhaustion primitive.
- A server with **no** secret rejects everything rather than accepting everyone.
- Rejection responses are uniform: missing, malformed, and wrong signatures return the same body.
  Each distinction is a hint toward a working forgery. Status is 401 for missing, 403 for invalid.

**Noise reduction.** The app owns exactly one comment per pull request, found by the invisible
HTML marker `<!-- reversibility-engine:certificate -->` and updated in place. Every page of
comments is searched, because on a long-running PR the app's comment gets buried and giving up
early would post a duplicate. Matching on a marker rather than on body text means the
certificate's wording can change freely without the app losing its own comment.

**Fail-closed network.** Any failure fetching the changeset — rate limit, 5xx, a file over
2 MiB, more than 500 changed files — produces `engine.UnavailableCertificate` with
`domain.RuleProviderError`: grade F, gate FAIL, and the reason in `Blockers`. The certificate is
still **posted**. Silence is the one unacceptable outcome, because a pull request with no
certificate looks identical to one that was never analyzed. Grading whichever files did arrive
is never an option.

**Context files (§16.4, resolved).** The provider lists each directory a changed file lives in,
at the **base** commit, and fetches the supported siblings the diff did not touch — returning
them as MODIFIED with identical sides, matching the fake and filesystem providers. This is what
makes K8S003 and K8S009 work in production. Scope is bounded to touched directories; a rule whose
context lies outside that still sees nothing and must return UNKNOWN.

**Other decisions.**

- Analysis runs **after** the HTTP response (202 Accepted). GitHub abandons a delivery that takes
  more than about ten seconds. `WithSynchronousProcessing()` exists for tests, where a background
  goroutine would make every assertion a race.
- Shutdown is graceful for the same reason: an abrupt exit drops certificates for deliveries
  GitHub already considers sent and will not retry.
- Only `opened`, `reopened`, `synchronize`, and `ready_for_review` are analyzed. Other actions
  cannot change the diff, so re-running would burn API budget to reach the same answer.
- Unsubscribed events and ignored actions return **200**, so GitHub stops retrying.
- An authenticated but incomplete payload (no base or head SHA) is a **400**. Comparing the wrong
  commits would certify a change nobody made.
- App authentication (JWT → per-installation token) is hand-rolled against `crypto/rsa` and
  `encoding/json` rather than adding a JWT dependency: the claim set is three fields and the
  signature is one RSA operation. Both PKCS#1 and PKCS#8 keys are accepted, because GitHub has
  issued both. Tokens are cached per installation and refreshed 5 minutes before expiry.
- `GITHUB_WEBHOOK_SECRET` **and** credentials are both required to start. A server that
  authenticates correctly and then cannot post is worse than no gate — the pull request looks
  reviewed.

## 11d. Git ref resolution — as built in S8

`--base <ref>` (with `--head`, defaulting to `HEAD`) resolves the changeset from git rather than
from two directories. Path arguments become git pathspecs, scoping the comparison to a subtree.

- **git is shelled out to; no git library is linked.** git's own answer to what a ref, a merge
  base, and a rename are is the definition of those words here. A second implementation would be
  a second thing that can disagree with the pull request the developer is looking at.
- **Blobs are read from the object database (`git show <sha>:<path>`), never from the working
  tree.** A dirty checkout is invisible: the certificate describes the refs it names, which is
  what lets someone else reproduce it. `TestCheckResolvesAGitRange` leaves uncommitted edits in
  place and asserts they do not appear.
- **The comparison is three-dot (`base...head`), and the previous side is read at the merge
  base.** Reading old content at the base ref instead would pair each old file with a newer
  sibling and describe a transition nobody proposed.
- **Refs are resolved to SHAs once, up front.** A branch that moves mid-run cannot then produce a
  changeset assembled from two different commits.
- **An ambiguous ref is an error, not a preference.** git resolves a branch and a tag of the same
  name by precedence and warns on stderr; accepting that would certify a comparison the user did
  not ask for.
- **A rename is reported as a REMOVED plus an ADDED, never as `StatusRenamed`.** The Kubernetes
  rules compare whole objects, and K8S003/K8S009 need to see the removal to ask what still
  depends on it. A copy (`C`) yields only the new path, since the source is untouched.
- **An unrecognised diff status is an error.** `U` (unmerged) and `X` (a bug in git, by git's own
  documentation) cannot be classified, and guessing a side would hand an analyzer a
  half-populated file.
- **Unchanged siblings in touched directories are returned as MODIFIED with identical sides**,
  matching the fake, fs, and GitHub providers (§16.4). Without this the same pull request would
  grade differently from the CLI than from the app, and the more permissive answer is the one a
  developer would see first.
- **Failures name the fix.** Not a repository, unknown ref, ambiguous ref, and — the common CI
  case — a shallow clone missing the base commit, which says `fetch-depth: 0` in the message.
  All of them reach the caller as a failure to fetch the changeset, which is grade F and exit 2.

## 11e. GitHub Action and releases — as built in S9

The action is a **composite** action at the repo root, with its shell in `action/`.

**There is one major line and it is v1. There is no v2 and there never was.** This is a ruling,
recorded here because the ambiguity already caused an incident and would otherwise regenerate:
several documents described the composite action as `@v2` while every tag ever cut was `v1.x`,
so the README told people to write a ref that did not exist and the frozen Docker action at
`v1.0.2` was left looking like the current one.

| Tag | What it is |
| --- | --- |
| `v1.0.0` – `v1.0.2` | **Docker container action.** Frozen. Pulls `ghcr.io/…:v1` and names no entrypoint. |
| `v1.1.0` onward | **Composite action.** Downloads a verified release binary. |
| `v1` | Moving major tag. Points at the newest non-prerelease `v1.x`. |

Consumers write `@v1` and always have. The transition from container to composite happened
*within* v1 and needed no new major, because every input kept working — which is the entire
reason the deprecated aliases exist. **Do not introduce a `v2` in prose, in an example, or as
an image tag.** If a real v2 is ever cut, this table is what it amends.

- **A released binary, verified, not a container and never `go install`.** A container pinned
  the action to Linux runners and paid an image pull per run; `go install` would need a Go
  toolchain and a C compiler on every consumer's runner, because of cgo. The action downloads
  the release asset for the runner's OS and architecture and **refuses to execute anything whose
  SHA-256 does not match the release's `checksums.txt`** — this binary decides whether changes
  merge, so an unverified one does not run. A missing checksum line is equally fatal: there is
  nothing to verify against.
- **`latest` is deliberately not cached.** An `actions/cache` key is immutable once written, so
  a key containing "latest" would pin the first binary it ever saw and keep serving it. Pinning
  a version is what earns the cache.
- **A moving major tag resolves to the latest release, not to itself.** `@v1` is the documented
  way to consume the action, and it has no release of its own — releases are cut as `v1.2.0` and
  the `v1` tag is repointed at them afterwards, so a download from `releases/download/v1/` 404s
  on precisely the tag everybody uses. Major-only refs therefore resolve to `latest`. The
  caveat, stated rather than hidden: should a second major line ever be maintained alongside
  this one, `@v1` would fetch a release from the newer line. Pin a full version to be immune.
- **The action analyzes a git range (`--base`), not a staged pair of trees.** v1 reconstructed
  two directories by copying changed files, which passed `--diff-filter=d` and therefore **never
  showed the analyzer a deleted file** — K8S003, K8S006, and every other removal rule could not
  fire on a real pull request. S8's provider removed the need for any of it.
- **Deprecated input names are kept but never merged.** `min-grade` → `gate`, `include` →
  `path`. Setting a name and its replacement together is an **error**, not a precedence
  decision: resolving it silently would pick one during exactly the upgrade that introduced the
  mistake. A gate that breaks on upgrade gets deleted rather than fixed, which is why the old
  names still work at all.
- **`config` is an error, not a no-op.** The policy file arrives in S10. An input that was read
  by nothing would leave a user believing their waivers applied — a failure in the permissive
  direction, which is the one this product exists to prevent.
- **The verdict is read back from the JSON certificate, never re-derived in shell.** A second
  definition of the grade is a second chance to get it wrong in the permissive direction.
- **`jq` is checked for up front.** Without the check, a runner lacking it yields an empty grade
  read from nowhere — the one shape of failure that can look like a pass.

**Releases** (`.github/workflows/release.yml`) build each target on a runner of its own
architecture. This is forced, not chosen: `CGO_ENABLED=1` is mandatory, cross-compiling cgo
needs a toolchain per target, and the darwin targets need the macOS SDK, which cannot be put on
a Linux runner. **goreleaser's OSS edition cannot split a build across runners** — that is a Pro
feature — so the matrix does it directly and one job merges the per-target checksums. Every
target verifies its own binary still classifies a `DROP TABLE` before it is packaged: a build
that silently lost the parser would grade every migration A for lack of findings.

**Every published artifact is built and verified on its native architecture. No exceptions.**

**The release matrix is four targets, and `darwin/amd64` is not one of them.** This is a *scope
decision* — a statement about which platforms this project ships — and explicitly **not** an
exception to the rule above, which stands unchanged.

| Target | Runner |
| --- | --- |
| `linux/amd64` | `ubuntu-latest` |
| `linux/arm64` | `ubuntu-24.04-arm` |
| `darwin/arm64` | `macos-14` |
| `windows/amd64` | `windows-latest` |

Three reasons, and the order matters:

1. **Intel Macs are a shrinking share of this tool's audience.** That is what makes dropping the
   target reasonable rather than merely convenient.
2. **`macos-13` is the only hosted Intel Mac runner and it queues indefinitely.** On the v1.1.1
   release it sat in the queue for over two hours and the run had to be cancelled by hand.
   `timeout-minutes` does not bound this: that clock starts when a job begins *executing*, so a
   job waiting for a runner that never arrives is unbounded by construction.
3. **Cross-compiling was considered and rejected.** It would produce the one artifact nobody
   could execution-test, and the per-target `DROP TABLE` check is the entire reason the matrix
   is built per architecture in the first place. In a tool that gates merges, an untested binary
   is worth less than no binary.

The path for Intel Mac users is documented in the README install section: build from source with
`CGO_ENABLED=1`, or run the Docker image. It is stated plainly and without apology, because it
is a supported path rather than a degraded one.

A future session must not restore `darwin/amd64` by cross-compiling it, and must not weaken the
native-build rule to accommodate it. Adding it back means a native Intel runner is available
again — nothing else qualifies.

No `-ldflags` version stamping anywhere. A build stamp reaching rendered output would break the
byte-identical guarantee (§11b).

**The major tag moves itself.** Consumers write `@v1` and expect the newest v1.x; nothing in git
moves that tag, and forgetting to do it by hand fails silently — everyone stays on the previous
release and the fix they were told to upgrade for never arrives. A final job repoints it through
the API and then reads it back, because a tag that quietly did not move is the same failure.
Prereleases are excluded: `v1.2.0-rc.1` must never become what `@v1` means.

`.github/workflows/action-selftest.yml` runs the action against this repository's own fixtures
and asserts the grade, the gate status, the finding count, **and that a grade F actually fails
the job**. An action that reports F and exits zero is not a gate.

**The `:v1` image incident — what actually went wrong, and what now prevents it.**

The v1.1.0 release published its image as `ghcr.io/…:v1` alongside the immutable version tags.
The frozen `v1.0.2` action is a Docker action whose `action.yml` reads
`image: 'docker://ghcr.io/vikoit/reversibility-engine:v1'` and sets **no `entrypoint:` and no
`args:`** — it runs whatever the image declares. At 1.0.2 that was `entrypoint.sh`, which did
the analysis. At 1.1.0 it was `revctl` itself, and **`revctl` with no arguments printed help and
exited 0**. Every existing `@v1` consumer's gate silently became a green check over nothing.

The moving tag was the vector. **The defect was the exit code**: the one invocation that
analyzes nothing was also the only one that could never fail.

Four things now stand in the way, and each is a test rather than a convention:

| Guard | Where |
| --- | --- |
| Bare `revctl` exits 2, help to stderr | `cli.go`; `TestBareInvocationIsNotAPass` |
| Asking for help still exits 0 | `TestHelpIsStillASuccess` — without this, users learn to ignore the exit code and the guard above stops protecting anything |
| An image whose no-argument run exits 0 is never published | `publish-image.yml`, and `analyzing-nothing-is-not-a-pass` in the selftest, which builds the image from the commit rather than pulling one |
| `--entrypoint` named explicitly at every call site | `publish-image.yml`, the selftest, the README example |

The action publishes **immutable version image tags only** — no `:v1`, no `:latest`. A moving
alias may come back when nothing consumes the image as an action entrypoint, and not before.
`.github/workflows/restore-image-tag.yml` repairs an alias from a runner rather than a laptop:
it copies a manifest with `imagetools create`, reads the digest back, and asserts the restored
`ENTRYPOINT` is the one the frozen action needs — because the digest proves which bytes, not
which behaviour.

The certificate post-condition in `certify.sh` is the same invariant one layer up: no
certificate on disk is exit 2, and a certificate whose grade cannot be read back is also exit 2.
A step that reports FAIL while passing the job has proved only that something was written.

## 11f. Policy file — as built in S10

`.reversibility.yml`, discovered by walking up from the analysis path and stopping at the
directory holding `.git`. `--config` names one explicitly; `--no-config` discards one.

**The one thing a policy may never do is improve the measurement.** §14 says configuration must
not improve a grade, and S10 says a waiver downgrades a finding to advisory. Both are honoured by
splitting the number in two, which is the owner's ruling:

| Field | Means | Moved by a policy? |
| --- | --- | --- |
| `Grade` | what the evidence says about the change | **never** |
| `EffectiveGrade` | `Grade` with waived findings set aside | yes — this is what CI compares |
| `AIGateStatus` | PASS iff `Grade` is A | **never** |

So a waiver unblocks a human's pipeline and **can never authorise an autonomous agent to merge**.
`EffectiveGrade` equals `Grade` whenever no policy applied, so a consumer always has exactly one
field to gate on and never has to know whether a policy existed.

- **A waiver requires `reason` and `expires`, and both are errors rather than warnings.** A
  warning in a CI log is not read, and the waiver would take effect anyway — which is the
  unexplained suppression this design exists to prevent. Exit code 2: a run that could not
  resolve its own configuration does not know what it was meant to enforce.
- **`expires` is a date, never a duration, capped at 180 days from the parse date.** "90d" is
  relative to a moment nobody records, so it renews on every read and never expires at all.
- **An expired waiver is inert.** The finding returns with no edit and no announcement.
- **A waiver is reported, never deleted.** Waived findings appear in the certificate's `Waived`
  section with their reason and expiry, in Markdown, JSON, and as SARIF `suppressions` — SARIF
  keeps the original level, because a waiver records acceptance, it does not reduce severity.
- **A waiver may not cover UNKNOWN, an unreadable verdict, or an analyzer error.** Accepting a
  risk nobody has characterised is not a decision anyone is in a position to make.
- **The undo plan still covers waived findings.** A waiver accepts a risk; it does not invent an
  undo. A plan that omitted the waived half would claim a completeness it does not have.
- **Overrides tighten only,** and they move `Grade` as well — tightening is the one direction
  configuration is allowed to push the measurement. An override to REVERSIBLE is refused at parse
  time; one that would weaken an actual finding is refused when applied.
- **Order is fixed: overrides tighten, then waivers match the result.** Waiving first would let a
  waiver written for a mild classification swallow a finding somebody separately marked severe.
- **`today` is injected** (`engine.WithToday`), defaulting to the system date. It is the only
  value in the engine not derived from the input, so it is injected rather than read where it
  would be untestable.
- **The policy is hashed into `InputDigest`, and exposed as `PolicyDigest`.** The digest covers
  the *resolved* policy, not the file's bytes, so reformatting or editing a comment does not
  change a certificate — and deliberately not which waivers are currently live, since a digest
  that changed overnight on its own would stop being evidence. It is mixed in **only when a
  policy exists**, so every digest ever produced without one is unchanged.
- **The engine does not analyze its own configuration.** `.reversibility.yml` is YAML, so the
  Kubernetes analyzer claimed it and reported K8S014/UNKNOWN — adopting a policy graded your
  repository F because of the file you adopted it with. `policy.IsPolicyFile` excludes it.

**Two bugs S10's tests found in earlier sessions' code**, both fixed here:

- The `fs` provider tested its `Include` predicate against the **absolute** path on disk, so
  `ignore: ["legacy/**"]` matched nothing. Extension checks never noticed because they only look
  at the suffix. The predicate now sees the path as it will appear in the changeset, which is
  what the git and GitHub providers already did.
- `.reversibility.yml` grading itself, above.

**A waiver's `path` matches `Finding.File` exactly as rendered**, which is repository-relative
under `--base` and the action, and relative to the named directory under `revctl check ./dir`. A
pattern matching nothing is inert rather than approximately applied: over-matching a waiver is
the one direction this must never fail in. Pinned by `TestWaiverPathMatchesTheFindingAsReported`.

Glob syntax in `ignore` and `waivers[].path` is `**` for whole segments, `*`/`?`/`[...]` within
one, delegating to `path.Match` per segment. **This is deliberately not git pathspec syntax** —
pathspecs are what the git provider hands to git, these are what the policy matches, and two
syntaxes under one name would be worse than two names.

## 11g. Production context — as built in S11

`revctl snapshot` writes a file; `revctl check --context` reads it. See
[`docs/PRODUCTION-CONTEXT.md`](PRODUCTION-CONTEXT.md) and
[`docs/ESTIMATES.md`](ESTIMATES.md).

**The engine never connects to anything during analysis.** This is the binding constraint of the
whole session, and it is enforced by the architecture test in §6, not by discipline. CI never
needs a production credential, determinism survives, and the analyzers stay pure.

- **Enrichment is a one-way ratchet.** `snapshot.Enrich` may make a finding *more* severe and
  never less: a classification whose severity would drop is discarded rather than applied, and
  the LockHazard is restored unconditionally because context describes how long a lock is held,
  never which lock is taken. The property test asserts the grade with context is **never better**
  than without, over every fixture, using a snapshot sized to trip every band.
- **The vocabulary, because it has been ambiguous.** *Lowering* a grade means making it worse
  (A → B → C → F) and is permitted. *Raising* one means making it better (C → B) and never
  happens.
- **What "absent" actually covers**, because the sentence here used to get this wrong. Exactly
  two things are treated as absent, and neither is ever read as a signal that the change is safe:
  a snapshot file that is not there, and an object the snapshot does not describe or describes
  ambiguously. **Everything else about unusable context is louder, not quieter:**

  | Situation | Outcome |
  | --- | --- |
  | The file is not there | context absent; **not an error** |
  | The object is not in the snapshot, or the subject is ambiguous | no context for that finding |
  | The snapshot is stale (older than `DefaultStaleAfter`, 7 days) | **used**, with a warning in `ContextWarnings` |
  | The file exists and cannot be read or decoded | **exit 2** |
  | Two snapshots of one kind from different sources | **exit 2**, naming both `--environment` labels and both fingerprints |

  **A fingerprint mismatch is the loudest outcome in the system, not the quietest.** The prose
  previously claimed it was "treated as absent", which was wrong twice over: it described a
  silent fallback that has never existed in `snapshot.Set.merge`, and it contradicted the "stale
  context is used and flagged" rule three bullets below. Anyone reasoning from it would expect a
  mismatched pair of snapshots to be ignored, when in fact the run stops.
- **Only two things reach a verdict from a snapshot,** both owner-specified in the S11 patch:
  `WILL_FAIL` for a `SET NOT NULL` that production proves will abort, and the lock duration
  bands. Everything else context produces is prose and numbers beside a finding.
- **`WILL_FAIL` is reported apart from `IRREVERSIBLE` everywhere** — blockers, undo plan, SARIF,
  Markdown. One means you cannot undo the change; the other means it will not happen at all, so
  the fix belongs in the migration. A reader who confuses them fixes the wrong thing.
- **A waiver cannot cover `WILL_FAIL`,** the same as `UNKNOWN` and for a related reason: a waiver
  accepts a trade-off, and there is no trade-off in a statement that cannot apply. Waiving it
  would document a bug rather than accept one, and the pipeline it unblocked would fail at deploy
  instead of at review. **This was not specified in the patch — it is a judgment call, and it is
  a one-line change in `waiverFor` to reverse.**
- **A band exists only where duration scales with size.** `PG014` takes an `EXCLUSIVE` lock,
  which passes the "at least FULL_SCAN" gate, but an index drop is not slower for being large and
  no rate is defined for it. Applying a scan rate there would cap a grade at C for an operation
  that finishes in milliseconds.
- **`DISRUPTIVE`'s cap is usually already satisfied.** Any `FULL_SCAN` finding is capped at B by
  the existing scoring rules, so in practice `OUTAGE` is the only band that moves a grade. Both
  caps are implemented anyway so the two rules stay independent.
- **`Finding.Subject` was added so enrichment is possible at all.** Matching context to a finding
  needs the table, column, or index name, and the only alternative was re-parsing `Statement` —
  truncated, normalized, and it would take a regex. Analyzers populate it verbatim from the parsed
  statement; what `Object` means depends on the rule, and the enricher already switches on rule ID.
  It is serialized in the internal model and deliberately **absent from `pkg/certificate`**: it is
  how the engine joins a finding to a snapshot, not a promise about how object names are spelled.
- **An ambiguous subject gets no context.** An unqualified table name matching two schemas, or a
  claim name matching two namespaces, resolves to nothing rather than to a guess. Context that
  names the wrong object is worse than none, because context is believed.
- **Only the six specified rule groups are enriched** — PG006/PG007, PG017, PG014/PG015, PG021,
  K8S003, K8S004. `TestOnlySpecifiedRulesGainContext` rejects any other.
- **K8S003 with `reclaimPolicy: Retain` records the fact and keeps the finding IRREVERSIBLE.**
  The brief's own instruction, and the no-downgrade rule doing its job: recovery is manual, and no
  snapshot should authorise a tool to grade data loss as reversible on somebody's behalf.
- **Stale context is used and flagged, never discarded.** Falling back to none would make the
  certificate quietly less informative exactly when somebody stopped refreshing the snapshot. A
  **missing** file is not an error at all; a file that exists and cannot be read is exit 2.
- **The context digest excludes `CollectedAt`.** It moves on every collection while the facts
  usually do not, and a digest that changed whenever somebody re-ran an unchanged collection would
  report a different certificate for an identical verdict.
- **Metadata only, and it is tested rather than asserted.** `TestSnapshotContainsNoUserData` seeds
  a throwaway database with passwords, API keys, and private-key material, runs the collector, and
  searches the output for every one. The DSN is used and discarded — never stored, never logged,
  never hashed into the fingerprint. Driver errors are scrubbed by `collect.Redact` because they
  quote connection strings into CI logs.
- **Read-only by construction:** `default_transaction_read_only=on` on the Postgres connection,
  `List` only against Kubernetes.
- **Estimates always carry a `~`.** The throughput constants are hard-coded, not configurable: a
  knob would let somebody tune the estimate until it was reassuring, and a number tuned to
  reassure is worse than no number.

## 11h. Terraform — as built in S12

`internal/analyzer/terraform/` reads `terraform show -json`. **It never reads
`terraform.tfstate`**: state holds provider credentials and attribute values in plaintext, a
plan does not, and there is no code path here that opens one. The authoritative table is
[`docs/RULES.md` §5](RULES.md#5-terraform--tf001-to-tf010).

**Only destruction is classified.** A created or updated-in-place resource has a reverse by
construction. That is what keeps the catalog finite: the problem was never "hundreds of AWS
resource types", it is "the types whose destruction hurts". TF004 is the one deliberate
exception and it is bounded to a closed list of named paths.

**The discriminator, in the owner's three clauses.** A resource change is IRREVERSIBLE if it
destroys data, destroys an identity that re-applying the same configuration cannot recreate, **or
destroys a recovery capability a future rollback would depend on.** The third clause is what
TF004 fires on, and it is also what makes TF001 on `aws_db_snapshot` coherent — deleting a
snapshot destroys no running system, it destroys the undo. Same family, and the rationale says so
in as many words, because a user reading an F on a one-line boolean change is owed that.

**TF003 is RETIRED and its number is never reused.** `prevent_destroy` is a `lifecycle`
meta-argument, and the JSON configuration representation does not carry lifecycle blocks. The
signal is also self-erasing: if `prevent_destroy` were still set, `terraform plan` would fail and
produce no plan at all, so any plan containing a delete already proves it is not set. Detecting
its *removal* needs the previous configuration, which a plan does not contain. Parsing `.tf`
files was considered and rejected — a different analyzer with a different failure surface, and
not worth a new dependency for a case TF001/TF002 already catch at the moment it matters. A
retired ID with its reason tells a contributor the case was considered; a gap in the sequence
reads as an oversight.

**The layers, in the order they run.**

1. **Evidence from the plan, before the catalog.** An attribute on the `before` object marks the
   resource STATEFUL whatever the catalog says. Evidence may only RAISE: its absence implies
   nothing at all, never "stateless".
2. **The embedded catalog.** `catalog/terraform/aws.yaml`, compiled in via `embed.FS`.
3. **User overrides** from `.reversibility.yml` `terraform_types`: classify an unknown type, or
   tighten a known one. Weakening is a configuration error — that path is a waiver, which carries
   a reason and an expiry and which per §11f changes the gate decision rather than the grade.
4. Nothing matched → **TF010/UNKNOWN → F.**

- **Presence means present AND meaningfully set.** null, `""`, `[]` and `{}` are not evidence;
  `false` and `0` are, because the attribute existing at all is the schema signal. This is what
  makes the `aws_instance` ruling work: `ephemeral_block_device` is present as `[]` on every EC2
  instance, so key-existence alone would raise all of them and undo the STATELESS classification.
  The same shape gives `aws_acm_certificate` its `private_key` key.
- **The type name is NEVER matched.** `aws_db_subnet_group` contains "db" and holds nothing.
  Names lie, and a regex over them would be a classification nobody could audit.
- **TF004 is a closed list of named paths, not recursion.** Eight paths, six top-level and two
  reaching exactly one level into a block (`versioning.enabled`,
  `point_in_time_recovery.enabled`). A named list is auditable against the table; recursion into
  a provider-defined object is not. Anything else is TF007/REVERSIBLE.
- **TF009 and TF010 are different findings with different remedies.** "I cannot read this file"
  versus "I read it and do not know this type". Both grade F; only TF010 carries the growth-loop
  snippet.
- **`*.tfplan.json` only, plus `--terraform-plan`.** Claiming `plan.json` would grade F on any
  repository that happens to have one, because a file the analyzer claims and cannot read is
  UNKNOWN. The flag is the escape hatch for a plan named otherwise; it matches by path suffix in
  either direction, because a flag is typed relative to a shell and `Supports` is asked about the
  changeset-relative path.
- **The catalog is an input to the verdict, and reaches the certificate only when it was used.**
  `CatalogVersion` on the certificate and the catalog digest in `InputDigest` — mixed in only when
  an analyzer implementing `analyzer.CatalogVersioner` actually claimed a file. Registering the
  analyzer therefore changed no existing digest and `verdicts.txt` did not move.

**STRICTLY NO TELEMETRY, and `check` never fetches.** Nothing in this codebase sends anything
anywhere. `revctl catalog update` is explicit and user-initiated; `revctl check` has no network
path at all — not on a cache miss, not when the catalog is old, not ever — and the embedded
catalog stays fully functional offline for the lifetime of the binary. The Layer 5 output is a
URL printed in a certificate the reader is already looking at; a human chooses to open it.

**`catalog scan` is a maintainer tool.** It shells out to `terraform providers schema -json`,
fails with a message naming what to install when terraform is absent, skips its tests cleanly on
a machine without it, and nothing in the check path depends on it. Its output is a proposal:
every candidate has an empty evidence field, and an entry without one fails the build, which is
what stops generated output being merged unread.

**Coverage is published honestly.** 92 of roughly 1,400 AWS resource types. The raw count and the
denominator both go in the docs — a project that knows its own limits reads better than one
quoting a flattering ratio. The STATELESS half is load-bearing rather than filler: an
unclassified deleted type grades F, so the network, IAM, load-balancing and compute groups are
what stand between a new user and an immediate failing gate.

## 12. Engineering standards

- Go 1.22+, `log/slog` for logging, stdlib `net/http` — **no web framework**.
- Explicit error returns. **No panic in library code.**
- A **single** `recover()` boundary in the engine orchestrator, converting a panic into grade
  **F** with `RuleID: "ENGINE_PANIC"` — never into a pass.
- All errors wrapped with `%w` and context. Sentinel errors live in `domain`.
- Every exported symbol documented. Comments explain **why**, never what.
- `context.Context` as the first parameter on anything that could block.
- Table-driven tests. Fixtures live in `testdata/`, **not** in Go string literals.
- `golangci-lint` clean; `go vet` clean; race detector on in CI.

## 13. Testing rules

- **Commit or stash before every mutation test.** A mutation is a deliberate corruption of
  working state, and **the undo must never be a command that also discards real work.**

  This is a process rule and it is here because it was broken. A mutation was reverted with
  `git checkout <file>`, which restored the file to the last commit and took an hour of
  uncommitted work in that file with it. The mutation had done its job — the property failed
  exactly as intended — and the cleanup cost more than the test was worth.

  The rule is not "be careful with `git checkout`". It is that a mutation test has two phases, and
  the second one runs *while you are thinking about the first*: you have just read a failure and
  confirmed a property holds, which is the worst moment to be composing a destructive command. So
  the safe state is established **before** the corruption, not recovered after it. `git stash` for
  a quick one, a commit for anything longer, and the revert is then `git stash pop` or
  `git restore --source=HEAD --staged --worktree` against a tree that has nothing to lose.

- **Before implementing an analyzer, write its fixtures and failing tests first.**
- **One fixture pair per rule ID** — all 59 Postgres rules, all 15 Kubernetes rules, and all
  9 active Terraform rules. **A rule with no fixture does not exist.**
  The one exception is a **retired** ID, declared in `internal/fixture/table_test.go`, which
  has no fixture and is never reused or renumbered. `TF003` is the only one; see
  [`docs/RULES.md` §5](RULES.md#5-terraform--tf001-to-tf010).
- **Every construct the code can classify must have a row in the authoritative table. A
  classification with no table row does not exist.**

  This is the sibling of the rule above and it was learned the same way. `convertDrop` folded
  materialized views into `KindDropView`, so `DROP MATERIALIZED VIEW` was graded by PG016 — a
  row that lists plain views, functions and triggers and says nothing about materialized views.
  The code was classifying a construct the authoritative table did not list, the rationale it
  printed asserted something false about the object, and every test in the repository passed.

  **A fixture test cannot catch that**, which is the point of adding a second one: PG016 had a
  fixture, and the fixture used a plain view. What was missing was the check in the other
  direction. `TestEveryClassificationHasATableRow` enumerates the rule IDs the analyzer sources
  can emit against the rows in `docs/RULES.md` and fails on either mismatch — a case with no
  row, or a row nothing emits.
- Golden-file tests for the Markdown and JSON renderers.
  As built: golden files exist for **all three** renderers across 8 scenarios spanning every
  grade plus the not-applicable case, in `testdata/fixtures/golden/`. Regenerate deliberately
  with `go test ./internal/render -update` and review the diff — a test that silently rewrote
  its own expectations would turn every rendering regression into a pass.
- Fuzz test on the SQL analyzer: it must never panic and **must never return `REVERSIBLE` for
  malformed input**.
- Property test: adding any statement to a changeset can only **lower or hold** the grade,
  never raise it.
- Minimum **85% coverage** on `internal/analyzer` and `internal/engine`.

### As built in S7 — hardening

**`internal/analyzer/postgres/parser/complexity.go` is load-bearing. Do not remove it.**

Fuzzing found that a chain of a few thousand operators — `SELECT 1+1+1+…`, or `NOT NOT NOT …` —
overflows the C parser's stack. That is a **hard process crash inside cgo**, which `recover()`
cannot catch, and on `revsrv` it made a ~10 KB pull request a remote denial of service. The guard
refuses structurally extreme input *before* it reaches cgo (100 nesting levels, 500 operators per
statement) and reports PG027/UNKNOWN, so it grades F. Word operators count as much as symbolic
ones: counting only punctuation left `NOT` chains able to crash the process. See ADR/0001.

**A verdict the domain does not recognise grades F.** An analyzer that returns an empty or
misspelled `Reversibility` used to merely fail the "all REVERSIBLE" test and cap at B. It is now
a blocker, because a classification nobody can read is exactly what UNKNOWN is for.

Fuzz targets, all runnable with `make fuzz`:

| Target | Package | Property |
| --- | --- | --- |
| `FuzzAnalyze` | `analyzer/postgres` | never panics; unparseable SQL is never anything but PG027/UNKNOWN |
| `FuzzValidateDownMigrations` | `analyzer/postgres` | the three levels stay ordered and always explain a failure |
| `FuzzParse` | `analyzer/postgres/parser` | the cgo seam survives arbitrary bytes; no statements returned beside an error |
| `FuzzAnalyze` / `FuzzAnalyzeAdded` | `analyzer/kubernetes` | never panics; lock hazard is always NONE |
| `FuzzCertify` | `engine` | **an IRREVERSIBLE or UNKNOWN finding can never sit in a passing certificate** |
| `FuzzCertifyIsDeterministic` | `engine` | identical input, identical bytes, whatever the input |
| `FuzzWebhookRejection` | `delivery/github` | nothing reaches the processor without a signature from the real secret |
| `FuzzAuthenticPayload` | `delivery/github` | an authentic but malformed body never 5xxs or dispatches a half-built job |

**Panic-boundary sweep** (`internal/engine/panic_test.go`): every panic point in the Analyzer
contract (`Analyze`, `Supports`, `Name`, `ValidateDownMigrations`) crossed with every kind of
panic value (string, error, struct, int, nil pointer, slice, `panic(nil)`), plus seven runtime
faults. All end in grade F with `ENGINE_PANIC`. Findings gathered before a panic are discarded
deliberately: a partial conclusion from a broken run is not evidence.

**Verdict snapshot** (`testdata/fixtures/golden/verdicts.txt`): the grade, gate, counts, and
input digest for all 46 fixtures in one file. Regenerate with `go test ./internal/engine -update`
and **review the diff** — a scoring change that quietly moves fixtures between grades shows up
here as changed lines. The digest column catches a fixture edited by accident even when its grade
does not move.

## 14. Do Not

- Do not invent classification rules or scoring weights not written above.
- Do not use regex for SQL parsing.
- Do not write placeholder or simulated data-fetching code.
- Do not add dependencies beyond the table in §6.
- Do not build anything listed under "Out of scope" (§3).
- Do not let any code path turn an error, a panic, or an unknown into a passing grade.
- Do not let any code path turn *no analysis* into a passing grade either (§2). A missing
  argument, a missing certificate, an unreadable verdict, and a lost entrypoint all exit 2.
- Do not publish a moving image tag, and do not inherit a container's entrypoint (§11e).
- Do not write `v2` — of the action, of the image, or in an example. There is one line and it
  is v1 (§11e).

## 16. Open questions — resolve with the owner, do not guess

A future session must **ask**, never decide alone. Items marked ASSUMED are encoded in the S1
fixtures so they are cheap to reverse — correcting one is a data edit, not a code change.

1. ~~**Down-migration reporting channel.**~~ **RESOLVED in S2.** `Analyze` returns only findings,
   so down-migration status travels through a separate exported function in the same package:

   ```go
   postgres.ValidateDownMigrations(ctx, p parser.SQLParser, files []domain.ChangedFile)
       ([]domain.DownMigrationStatus, error)
   ```

   It is stateless, so the analyzer holds no per-run state and stays safe to share. **S4's
   orchestrator must call it alongside `Analyze`** and put the result on the certificate — a
   scorer that only calls `Analyze` will silently lose the `down.sql` cap.
2. **Rule precedence when several rules match one statement.** ASSUMED in S1 fixtures: one
   finding per statement; `PG005` (CASCADE) overrides any base rule it overlays; otherwise the
   lowest rule number wins. `DROP TABLE x CASCADE` is therefore **one** PG005 finding, not
   PG001 + PG005.
3. **PG006 vs PG007 need the prior column type,** which a migration file does not contain.
   ASSUMED in S1 fixtures: the analyzer tracks types declared by `CREATE TABLE` / earlier
   `ALTER` statements *within the same changeset*. A type it cannot resolve is PG027/UNKNOWN.
   Confirm before S2, since a schema-baseline input would be a different design.
4. ~~**`ChangedFiles` must return unchanged context files, not only changed ones.**~~
   **RESOLVED in S6, and matched by every provider since.** K8S003 and K8S009 cannot be decided
   from the changed file alone: K8S003 needs the StorageClass that nobody edited, and K8S009
   needs the workload that still references the deleted ConfigMap. A GitHub pull-request diff
   contains neither.

   `ChangedFiles` returns them; `FileProvider` did **not** grow a second method. All four
   implementations return unchanged siblings as **MODIFIED with identical sides**, and the scope
   is bounded to the directories a changed file lives in, read at the **base** commit:

   | Provider | How | Session |
   | --- | --- | --- |
   | `fakeProvider` | reads them from the fixture directory | S1 |
   | `githubProvider` | lists each touched directory at the base commit and fetches the supported siblings | S6, §11c |
   | `fsProvider` | `--before` returns unchanged files with identical sides | S5, §11b |
   | `gitProvider` | same, scoped by pathspec | S8, §11d |

   All four agreeing is the point, not an implementation detail: without it the same pull request
   would grade differently from the CLI than from the app, and the more permissive answer is the
   one a developer would see first.

   **The fallback rule still stands and is not obsolete:** a rule whose context lies *outside* a
   touched directory still sees nothing, and **must return UNKNOWN, not REVERSIBLE.**
5. ~~**No Kubernetes rule covers a container image change.**~~ **RESOLVED: K8S015 is now in the
   `docs/RULES.md` §2 table**, constrained by the owner to digests only. A tag, however version-like, cannot be
   proven immutable by static analysis and stays K8S008/COSTLY.
6. **UNKNOWN findings also replace the undo plan.** S4 extended the replacement from
   IRREVERSIBLE to UNKNOWN, and the S11 patch extended it again to WILL_FAIL.

   The reason is §2: an UNKNOWN finding is a change nobody understood, so a plan that lists steps
   for everything *else* claims a completeness it does not have — a confident-looking script
   printed beside an unclassified change is the wrong-safe-verdict failure this product exists to
   prevent. It affects presentation only; the grade was already F either way.

   **Still open — confirm or reject.** What changed is only where it is written down.
   [`docs/RULES.md` §3](RULES.md#undoplan) previously said the plan is replaced "if any
   finding is IRREVERSIBLE", which made the shipped behaviour formally a spec violation in a
   document whose own rule is that the code is the bug where the two disagree. §3 now states all
   three verdicts and the distinct wording each produces, so the spec describes what v1.1.2 does.

   Rejecting it is therefore no longer a one-line change: it is `unreversibleFindings` **and** the
   table in `docs/RULES.md` §3. WILL_FAIL is not part of this question — a statement that aborts
   leaves nothing to undo, which is a fact about the transaction rather than a presentation
   choice.
7. ~~**Partial coverage — a changeset the engine analyzed only part of.**~~ **RESOLVED, twice.
   The second ruling reverses the first, and both are recorded because the argument matters more
   than the outcome.** Schema `1.5.0`.

   **The ruling in force: a partial pass is a bypass.** A security tool cannot vouch for a
   changeset it only partially understands.

   - **Coverage is measured over every file in a migration directory**, not over the files an
     analyzer happened to want. A `.md`, a `.gitkeep`, a helper script beside the migrations all
     count: the engine cannot tell from the outside which of them is inert, and a file it did
     not read is a file it cannot vouch for. Counting only the files an analyzer wanted would
     make the numerator and the denominator the same number and the check vacuous.
   - **A directory is a migration directory** if it is named for one (`migrations/`,
     `migration/`, `db/migrate/`) **or** it holds at least one file an analyzer claimed. The
     second clause matters: requiring the conventional name would let a rename defeat the check.
   - **Anything less than full coverage fails closed.** `Coverage: PARTIAL`, **grade F**,
     `AIGateStatus: FAIL`, and **exit 2** — unconditionally, with no flag and no threshold. Exit
     2 rather than 1 because the grade was not too low; part of the changeset was never measured,
     which is a run that did not complete.
   - **The message names the remedy**, since a refusal a reader cannot act on is one they will
     route around: *"Cannot guarantee reversibility. Unanalyzed files found in migration
     directories. Remove them or explicitly ignore them in the config."*
   - **`--require-full-coverage` is a deprecated no-op**, kept accepted so an upgrade does not
     turn a pipeline into an unknown-flag error.

   **What the first ruling said, and what changed it.** The first was: *PARTIAL never changes the
   grade — a file the engine cannot read is not evidence that the change is unsafe, and inventing
   severity from ignorance is the mirror of the bug the P0 fixed.* Coverage moved only the gate,
   and `--require-full-coverage` was opt-in on the reasoning that defaulting it on *"would fail
   every Django and Rails PR on day one, and a gate everyone disables protects nobody."*

   Both of those are real arguments and neither was wrong on its own terms. What overrides them:

   - **F does not mean "this change is dangerous". It means "this cannot be certified"** — which
     is already exactly what it means for an analyzer error and for PG027. Reading F as a
     severity claim is what made the mirror argument persuasive, and F has never been one.
   - **An analysis that read four of five migrations did not complete.** Grading it on the four
     is a verdict about a changeset that does not exist. That is not severity invented from
     ignorance; it is declining to publish a measurement that was not taken.
   - The gate-everyone-disables risk is answered by the escape hatch rather than by a softer
     default: `ignore:` in the policy, which is **explicit, digested into `PolicyDigest`, and
     printed on the certificate.** A recorded decision is not a bypass.

   **The escape hatch has to actually work, and that needed one correction.** §16.8 says a
   policy-ignored candidate closes the merge gate. Applied to *every* ignored path it would make
   strict coverage unusable — the only way to satisfy it would permanently deny an agent a merge.
   So only ignored **candidates** count against the gate: ignoring a `README.md` beside the
   migrations is somebody telling the engine what a README is, not somebody accepting a
   reversibility risk. Ignoring a `.rb` migration still closes the gate, which is what stops the
   hatch from being a bypass. Both directions are pinned by tests.

   **Coverage has therefore left the waiver pattern.** The grade-describes-evidence rule below
   still governs waivers; coverage no longer belongs to it, because an incomplete analysis is not
   a risk somebody accepted — it is a measurement that was not taken.

   The help text for `--gate` used to call it "the setting autonomous agents must use". That was
   already loose and is now wrong: an agent reads the certificate, never an exit status.
9. ~~**Strict coverage is complete for migration-named directories and not for the others.**~~
   **RESOLVED. The two-phase `FileProvider` contract shipped.**

   The principle graduated to §2 above, where it belongs now that the code satisfies it. What
   follows is the record of the change, kept because the sequencing is the part worth repeating.

   `List(ctx, ref) ([]Path, error)` and `Read(ctx, ref, paths) ([]ChangedFile, error)`. All four
   implementations, the CLI, and the GitHub App moved together. `Include` is gone; `Select`
   receives the complete listing.

   **`Read` takes the ref as well, and the GitHub provider is why.** Its comparison ref is
   per-call, where the filesystem and git providers get their revisions at construction. Storing
   it between phases would have made `Read` depend on `List` having run first, on the same value,
   in the same goroutine — state where the old contract at least had none.

   **The gate held.** `verdicts.txt` did not move: every grade, gate status, finding count, undo
   plan, blocker count, and input digest is byte-identical across the change, and so is every
   golden file. That was the point of sequencing the plumbing first.

   **Two defects surfaced immediately** when the enumeration widened, both in the CLI rather than
   in the providers, and both invisible under the old contract:

   - The **policy file itself** was counted against coverage. Every repository with a
     `.reversibility.yml` would have failed.
   - **Policy-ignored paths** were counted, which contradicts §16.8 directly: an ignore is a
     human decision, reported under `IgnoredByPolicy`, and not a coverage gap.

   Both now leave the enumeration before it reaches the engine. Neither could have been found
   before, because neither path was ever enumerated.

   **A third came from the providers agreeing.** `git`'s listing collapsed a rename into one
   entry while its reader emitted a delete plus an add, so `Read` was handed a path `List` had
   not produced. The two halves of one provider have to agree about the shape of a rename, which
   is the same class of requirement §16.4 places on the four providers agreeing with each other.

   **The rename bypass is closed.** `db/sql/notes.txt` beside a `.sql` file now enumerates,
   counts, and fails, exactly as `db/migrate/notes.txt` does. The harness catalogue entry in
   [`proposals/bypass-rename-migration-dir.md`](proposals/bypass-rename-migration-dir.md) stays
   filed — a catalogue that lists only open bypasses cannot show that one was closed.

   A directory is a migration directory two ways: it is **named** for one, or it **holds a file
   an analyzer claimed**. The second clause exists so a rename cannot defeat the check. Coverage
   is complete for the first and incomplete for the second, and the reason is structural:

   > **A provider decides whether to read a file from its path alone**, before it has seen the
   > rest of the changeset. `Include` is a `func(path string) bool`. "This directory holds a
   > `.sql` file, so fetch its `README` too" is not a decision a per-path predicate can make.

   So `db/migrate/notes.txt` is fetched, counted, and fails the changeset;
   `db/schema/notes.txt` beside a `.sql` file is never fetched and never counted. A repository
   that keeps migrations in an unconventionally named directory gets a weaker check than one that
   follows the convention, which is precisely backwards.

   **RULED: `FileProvider` becomes two-phase. Its own session, nothing else in it.** Not yet
   implemented — this records the decision so it is not re-litigated.

   ```go
   List(ctx, ref) ([]Path, error)     // enumerate — cheap, no content
   Read(ctx, paths) ([]File, error)   // fetch only what is needed
   ```

   **Nothing new is being computed.** All four implementations already have the list before they
   fetch: the filesystem walk visits every path without reading it, `git diff --name-status`
   returns names before blobs, the GitHub files API returns the listing in the same response, and
   the fake has the fixture directory. The contract is simply refusing to expose what the
   providers already know.

   The reason, which graduates to §2 when the contract lands — it is not written there yet
   because the code does not satisfy it, and in this document the code is the bug where the two
   disagree:

   > **Enumeration and retrieval are separate concerns.** A provider must be able to report what
   > exists without fetching it. Any check that describes the *shape* of a changeset depends on
   > enumeration; any check that describes *content* depends on retrieval. Conflating them makes
   > the first check unable to see what it is measuring.

   **Three defects, one root cause**, which is what moved this from "expensive, probably not
   worth it" to ruled:

   1. The **Django P0** — a docs-only pull request and thirteen unreadable migrations arrived at
      the engine as the same empty file list, because `Include` admitted neither.
   2. The **coverage denominator** collapsing onto the numerator — counting only files an
      analyzer wanted makes the check vacuous.
   3. **This** — the denominator is correct and the provider never supplies it.

   Each was found separately and fixed locally. The pattern was only visible on the third.

   **Sequence for that session:**

   1. The contract change and all four implementations, with `verdicts.txt` unchanged as the
      gate — no verdict may move while the plumbing is replaced.
   2. Then the coverage denominator moves onto the enumerated set.
   3. Then re-run all three coverage mutations, **including the one that caught nothing** until
      the property gained an independent oracle.

   **The cheap alternative is rejected, not deferred.** Admitting every file and filtering in the
   engine makes `revctl check .` read the whole repository and the GitHub App fetch every file in
   every touched directory. That is an API-quota decision, and it is not one to make as a side
   effect of a coverage fix.

   **The live consequence is catalogued rather than left implicit.** Until this lands, renaming
   `migrations/` to `db/sql/` turns strict coverage off with no trace on the certificate — see
   [`proposals/bypass-rename-migration-dir.md`](proposals/bypass-rename-migration-dir.md), filed
   for the harness Phase 5 bypass catalogue.
8. ~~**Does a policy `ignore:` count against coverage?**~~ **RESOLVED by the owner: no, and it
   closes the gate instead.** Schema `1.5.0`.

   An ignore is a human decision, exactly like a waiver, so it follows the waiver pattern rather
   than the coverage pattern:

   - **Coverage stays `FULL`.** The engine was capable of reading those files; it was told not
     to. Coverage describes capability, not permission.
   - **`IgnoredByPolicy` lists every candidate the policy excluded**, and the markdown renders
     it above the findings — the same reason `UnanalyzedFiles` is there. The reader must never
     infer what was skipped. The path is recorded and the content still never read, which is the
     property that makes an ignore list mean anything.
   - **A PASS requires zero policy-ignored candidates.** A human decision never buys an agent a
     merge. That is already the waiver rule, and it must not depend on which mechanism the human
     used.

   One principle now spans waivers, coverage and ignores: **humans may accept risk with their
   names on it; agents may not inherit it.**

   The original wording of the question follows, because the arguments on both sides were what
   the ruling turned on.

   `ignore: ["**/migrations/**"]` plus one `.sql` file grades **A** with `Coverage: FULL`, and
   the ignored migrations appear nowhere on the certificate. The same changeset without the
   policy is `PARTIAL`.

   The argument for leaving it: `ignore` means ignore, that is what the setting has meant since
   S10, and the decision is attributable — the policy is in the input digest and `PolicyDigest`
   is on the certificate. The argument against: the P0 was precisely that a green certificate
   said nothing about content nobody read, and "the user asked for it" is exactly what a
   misconfigured ignore list looks like from the inside.

   Not decided while implementing the coverage ruling, because it would change S10 semantics
   rather than extend them. Raised here so the next session does not have to rediscover it.

10. ~~**The Django P0 is fixed, and the documented invocation still reaches the permissive
    answer.**~~ **RESOLVED. Candidate detection no longer depends on how the analysis root was
    named.**

    ```
    revctl check django/contrib/auth/migrations --gate  -> NO_CANDIDATES, exit 0
    revctl check django/contrib/auth            --gate  -> UNSUPPORTED_CONTENT, exit 2
    ```

    Same files, same engine, opposite answers — and **the permissive one is what the documented
    invocation reached.** [`docs/RULES.md` §3](RULES.md#3-scoring) defines a plausible migration
    as a `.py`/`.rb`/`.js`/`.ts` file under a path segment named `migrations`; `check ./dir`
    reports paths relative to the named directory, which strips exactly that segment. The audit
    hit this 247 times across 60 repositories.

    **The invariant, which is the part that generalises:**

    > **Candidate detection must not depend on how the analysis root was named.**
    > `check ./migrations` and `check .` from its parent must reach the same outcome for the
    > same files.

    **What it is, and what it is not.** This is the P0's third appearance and it is not a
    repetition of it. §2's rule is that a run which examined nothing must not return the
    permissive answer, and this run *did* examine the files. It classified them correctly, over
    a path with a segment missing. The engine was not wrong about the changeset; it was wrong
    about **where** the changeset was — which is why no amount of hardening around "did anything
    get read" would have found it.

    **The fix.** `provider.RootPrefix` resolves the command-line roots to their
    repository-relative form and `engine.RootedAt` carries that prefix into `Certify`. Every
    question about *location* — `Candidate`, `InMigrationDir`, the migration-directory map, and
    the per-file reason — is asked of the qualified path. **Every path the certificate reports
    is left exactly as the caller named it**, so a finding stays addressable with the command the
    reader just ran, and `InputDigest` does not move.

    **The anchor is the repository, and choosing it was the whole design decision.** Two anchors
    are self-consistent and either satisfies the invariant; what fails is an anchor that moves
    with the argument.

    | Anchor | Satisfies the invariant | Cost |
    | --- | --- | --- |
    | The named root, as typed | **No** | `check migrations/versions` loses `migrations` — the same defect, one level in |
    | The filesystem root | Yes | A checkout parked under `~/migrate/` reads every `.py` beneath it as a migration: severity invented from where somebody keeps their source |
    | **The repository root** | **Yes** | Behaves differently inside and outside a checkout, which is stated rather than hidden |

    The repository is also the namespace `provider.Path` already documents and the one git and
    GitHub already report in, so the fix makes the filesystem provider agree with the other three
    rather than adding a fourth idea of where a file is. Outside a checkout there is no boundary
    to anchor at and the absolute path is used — consistent, which is the property being bought,
    and unqualified only because there is nothing to qualify it with.

    **The blocker messages deliberately do not use the qualified path.** A blocker is on the
    certificate, a certificate must be byte-identical for identical input, and a qualified path
    outside a checkout is an absolute one — the same class of input as a timestamp or a hostname,
    which the determinism rule excludes. **The qualified namespace classifies; it never renders.**

    Held by `TestOutcomeDoesNotDependOnHowTheAnalysisRootWasNamed`, which compares six invocation
    shapes over every fixture changeset in `testdata/` and needs no oracle because the shapes are
    compared against each other, and by `TestTheDjangoCaseFailsFromEveryDirectionItCanBeNamed`,
    which pins the answer as well as the agreement — six shapes agreeing on exit 0 would satisfy
    the property and reinstate the P0.

11. ~~**A deprecation notice on stdout makes JSON output unparseable.**~~ **RESOLVED. All
    diagnostics go to stderr; stdout carries the certificate and nothing else, in every format.**

    `revctl check --format json --require-full-coverage` wrote

    ```
    Flag --require-full-coverage has been deprecated, ...
    { "schemaVersion": "1.5.0", ...
    ```

    so the pipeline that was going to read a grade out of stdout read a syntax error instead.
    **The flag was kept accepted so that an upgrade would not become an unknown-flag error, and
    keeping it turned the upgrade into a parse error** — the same failure wearing a different
    coat, and a worse one, because an unknown flag names itself and a JSON syntax error does not.

    Nothing in this repository wrote that line. pflag records a flag's deprecation message into
    cobra's `flagErrorBuf` during `ParseFlags`, and cobra flushes the buffer through `Print`,
    which is `OutOrStderr()` — stdout, because `Execute` sets the out writer to stdout so that
    `--help` reaches it. **One call to `MarkDeprecated` is enough to corrupt every JSON
    certificate a command emits**, from a line that reads like documentation.

    > **All diagnostics, deprecations and warnings go to stderr. stdout carries the certificate
    > and nothing else, in every format.**

    `MarkDeprecated` is replaced by `MarkHidden` — the half of it that was wanted, keeping a dead
    flag out of the help — plus `warnAboutDeprecatedFlags`, a table in `check.go` written to
    `opts.Stderr`. The notice is still given: a pipeline carrying a dead flag has to hear about
    it, just not on the stream carrying the answer.

    **Redirecting cobra's writer was considered and rejected.** Cobra reaches for the same writer
    for help and for flag warnings, so pointing it at stderr moves `revctl check --help` there
    too, and `revctl snapshot --help` is the documented way of checking what the collector reads.
    **The output a user asked for is not a diagnostic.** Not using the mechanism is the narrower
    fix and the one that leaves nothing to be redirected.

    Held three ways: `TestStdoutIsAlwaysParseableJSON` over the flag space of `check`,
    `TestDeprecationNoticesGoToStderr` on the reported case, and
    `TestNoFlagIsDeprecatedThroughCobra`, which fails on the **mechanism** — it scans the
    package's non-test sources for `MarkDeprecated`, because the alternative is importing pflag
    directly to name the type its `VisitAll` callback takes, and §6's dependency list is complete.

12. ~~**A gate with no path to green.**~~ **RESOLVED, and the verdict behind it is unchanged.**

    The audit's headline number: 85% of the corpus is un-gradeable, 100% of graded runs are
    blocked at `--gate`, and a Django or Rails team has no path to a passing grade, ever, because
    the engine will never parse `.py` or `.rb` migrations.

    **That verdict stands and is not weakened.** What changed is that the path around it is
    documented rather than discovered, because **a gate with no path to green gets uninstalled,
    and a gate nobody runs protects nothing** — which is a worse outcome than the one the
    strictness was bought to prevent.

    - `UNSUPPORTED_CONTENT` and coverage-`PARTIAL` blockers name the way forward when the cause is
      a framework the engine cannot parse: `engine.RenderToSQL` plus the concrete command per
      format. Django and Alembic are both named for `.py` rather than guessed between; `.js` and
      `.ts` get the general sentence and no invented command, because **a command that does not
      exist is worse than no command**.
    - The README documents rendering as a supported path with a worked Django example, and says
      in one plain line that the engine assesses SQL, Kubernetes manifests and Terraform plans,
      and that ORM-native migration files must be rendered first.

    **The three outcomes are documented together, because the middle one surprises people:**

    | What the engine sees | Grade | `AIGateStatus` | Exit under a gate |
    | --- | --- | --- | --- |
    | The rendered SQL alone | **A** | **PASS** | `0` |
    | The whole repository, ORM sources under `ignore:` | **A** | FAIL | `0` |
    | The whole repository, nothing ignored | **F**, `PARTIAL` | FAIL | `2` |

    The middle row is §16.8 applied unchanged: a human may accept a risk with their name on it,
    and an agent may not inherit it. So an agent-mergeable repository is one that has **moved** to
    SQL-first migrations, not one that has learned to look away from the Python ones — and saying
    that plainly is the honest form of "there is a path".

13. ~~**Ignore globs are matched against the root-relative path while candidate detection uses the
    qualified one.**~~ **RESOLVED as the same defect, not a second one. Every path-keyed decision
    now resolves against the project-qualified namespace.**

    §16.10 fixed candidate detection and left `ignore:` globs, waiver `path:` globs and
    `--terraform-plan` matching against the changeset's spelling. **Two path namespaces in one
    decision surface is what produced the P0; keeping one of them was keeping the cause.** The
    ruling, now in §2:

    > **Path-keyed decisions use one namespace, and it is not the one the caller typed.** A
    > decision that depends on how the analysis root was named is a decision that changes answer
    > for the same files. The caller's naming survives in exactly one place: what gets rendered.

    **What was live, in the order it was found.** The audit turned up a fourth instance, which is
    the reason the rule is stated generally rather than applied twice:

    | # | Decision | What it did | Consequence |
    | --- | --- | --- | --- |
    | 1 | Candidate detection | Classified `0001_initial.py` | The P0: `NO_CANDIDATES`, exit 0 |
    | 2 | `ignore:` globs | `django/**/migrations/*.py` matched nothing under `check ./migrations` | An ignore list that read as configured and was inert |
    | 3 | Waiver `path:` globs | Same | Worse: a waiver matching nothing looks identical to one that has not expired |
    | 4 | `--terraform-plan` | Bidirectional suffix match, with a comment saying the spellings "differ" | Over-claimed: `a/plan.json` also claimed `b/a/plan.json`, which graded **F** as UNKNOWN |

    The fourth had already met this problem and answered it with a workaround. That is the shape
    worth recognising: **a comparison that needs a fuzzy match is usually two namespaces wearing a
    disguise.**

    **Enforced by a type, not by discipline.** `domain.Located` is a distinct string type; every
    path-keyed function takes it; `domain.Locator` maps a changeset path into it and is built once
    per run, at the boundary. `engine.Candidate(f.Path)` no longer compiles. That converts
    "somebody forgot" — which is invisible — into "somebody wrote a conversion", which is visible
    and greppable. The compiler found both remaining call sites during the change, which is the
    argument for the type in one sentence.

    `domain.ChangedFile` carries `At`, stamped once in `Certify`, so an analyzer receives the
    value rather than computing it — an analyzer may not touch the filesystem, and resolving a
    root requires it. An empty `At` falls back to `Path`, and that is the correct answer rather
    than a permissive default: no root named means nothing was stripped.

    **The anchor moved from the checkout to the project, and that correction was forced by the
    globs.** §16.10 anchored at `.git`/`.hg`/`.svn` and fell back to the absolute path. Applied to
    user-authored globs that is fatal: in a tree with no checkout, every relative pattern resolves
    against an absolute path and matches nothing — the exact failure this namespace exists to
    remove, reappearing in the config. So `.reversibility.yml` is a project marker too. A policy
    file is as good evidence of "where this project starts" as a `.git` is, and **a tree with
    neither has no globs to break, because a glob comes from a policy file and a policy file would
    have been a marker.**

    **This changes S10 semantics and the change is announced rather than discovered.** A waiver
    written `path: "migrations/0001_*.sql"` — the way anybody writes one — matched nothing under
    `check ./migrations` and now applies. A config whose globs silently matched nothing may begin
    matching. **That is the correction, not a regression**, and it is in the changelog under its
    own heading because nobody should meet it as a changed grade.
    `TestWaiverPathIsWrittenAgainstTheProject` asserts the new rule where its predecessor asserted
    the old one.

    **Dead config is now reported, and that is the other half of the ruling.** A pattern that
    matches nothing is indistinguishable, from the outside, from a pattern that is protecting you
    — and **dead config in a safety tool reads as protection the user does not have.** Same
    principle as naming unanalyzed files: never let the reader infer. `PolicyWarnings` lists every
    `ignore:` that matched no path and every waiver that covered no finding, on the certificate
    and on stderr, worded as an observation. It is never an error and never moves a grade: a
    waiver written for a rule that did not fire on this pull request is doing exactly what it
    should.

    The complete audit — every place a path is compared, and whether it is namespace-sensitive or
    invariant — is the table in §2 rather than here, because it is a standing reference and not a
    record of one decision.

14. ~~**Nothing stops a machine-specific value reaching a rendered certificate.**~~ **RESOLVED.
    No rendered field, in any format, may contain an absolute path, a hostname, or a username.**

    This is the rule the §16.10 work nearly broke, and it is written down rather than remembered
    because it was caught by attention rather than by a test.

    Qualifying paths for classification also made them available for rendering, and the first
    version of the `UNSUPPORTED_CONTENT` message used the qualified directory — which outside a
    project is absolute. Two runs over the same tree unpacked in two places would have produced
    different certificates, and every one of them would have carried the analyst's home directory
    into a pull request comment.

    > **No rendered field, in any format, may contain an absolute filesystem path, a hostname, or
    > a username.**

    It belongs beside the no-timestamp rule and is enforced the same way, because the existing
    determinism tests could not have caught it: they compare two runs **on one machine**, where an
    absolute path is perfectly stable and perfectly wrong.

    Three tests hold it. `TestNoRenderedOutputCarriesAMachineSpecificValue` scans every format of
    every location-naming outcome for the shapes a leak takes.
    `TestTheSameTreeInTwoPlacesRendersIdentically` unpacks one tree twice and compares bytes,
    which catches a leak of any shape at all.
    `TestGoldenCertificatesCarryNoMachineSpecificValue` extends it to the committed fixtures,
    because a golden file is regenerated on somebody's machine and then asserted as correct
    forever.

15. ~~**Documented numbers are maintained by hand.**~~ **RESOLVED. A number in documentation that
    duplicates the specification is derived by test.**

    The principle graduated to §2. What belongs here is the reasoning about scope: the badge was
    the instance, and the generalisation is that **every claim about the tables was already
    checked against them except the ones a reader sees first.**

    Derived now: the rules badge, the counts in `docs/RULES.md`'s own table of contents, the
    Terraform catalog's resource-type and per-class counts, the certificate schema version, the
    release-target count, and every `PG001–PG059`-style range in prose and anchors.

    Two mechanisms are deliberate. **A claim whose wording moved fails rather than passes** — a
    guard that quietly stops guarding is how the badge drifted in the first place — and **the
    failure prints the corrected text**, so it is a fix rather than a research task.

16. ~~**Which project root a glob was resolved against is invisible.**~~ **RESOLVED. The nearest
    marker walking up wins, and the resolved anchor is on the certificate.**

    §16.13 anchored the decision namespace at the project — a VCS marker or `.reversibility.yml`
    — and left one case unstated. **A monorepo has a policy file per package and one `.git` at the
    top, and those disagree about where the project starts.**

    **Ruling: the nearest marker walking up wins.** A package's policy is written about that
    package — its globs say `db/migrate/**`, not `packages/api/db/migrate/**` — and a run about
    that package has to resolve them against paths of that shape. The implementation already did
    this, because `projectRoot` returns at the first level holding any marker; what was missing
    was the rule being written down and a test holding it.

    **And the resolved anchor is reported, which is the part that was actually missing.** The
    answer is not always what a user expects: an outer policy above a nearer `.git`, a submodule,
    a package that acquired a checkout of its own. **A user who cannot see which root a glob was
    resolved against cannot debug a pattern that matches nothing** — the same principle as
    `PolicyWarnings`, applied to the thing that explains them.

    `PathAnchor` names the marker and `PathPrefix` says where the analysis root sat inside that
    project, so a reader can see what a path looked like when a glob was tested:

    ```
    "pathAnchor": ".reversibility.yml",
    "pathPrefix": "db/migrate"
    ```

    **Three details are load-bearing.**

    - **The anchor is the marker's name, never its directory.** The directory is the more useful
      answer and it is a path on this machine, which §16.14 forbids a certificate from carrying.
    - **`PathPrefix` appears only alongside an anchor.** With no project root the prefix *is* an
      absolute path, so the same rule applies — and its absence is itself informative.
    - **When there is no anchor, `PolicyWarnings` says so in words**: *"no project root was found
      …, so paths were resolved absolutely and a project-relative pattern cannot match."* That is
      a fact about the tree rather than about the pattern, and it is not something a user would
      think to check.

    Several roots resolving into different projects report no anchor at all. Naming one of two
    disagreeing anchors would be worse than naming none: a reader would debug a glob against a
    project only half the changeset is in.

17. **Where else does correctness rest on remembering rather than on a type?** **SURVEYED, NOT
    IMPLEMENTED** — see [`docs/proposals/type-enforced-invariants.md`](proposals/type-enforced-invariants.md).

    `domain.Located` worked because the compiler enforced it, and the compiler then found the
    remaining call sites during the change rather than leaving them for an audit. The question is
    where else that property is available.

    The survey criterion, which is one level out from `TestZeroValuesAreNeverSafe`: **where does an
    omitted, unset, or not-yet-applied value mean the permissive answer?** Not a wrong value — an
    absent one. An unset `Reversibility` is invalid and fails closed; an unset *field* is a valid
    zero meaning "nothing to worry about".

    Four findings, in the proposal, with a recommendation on each:

    | # | Where | If omitted | Recommended |
    | --- | --- | --- | --- |
    | 1 | `GateConditions.PolicyIgnored` | `0` — one of the three conditions for **PASS** | **Do it.** The only one that can produce a pass. |
    | 2 | `scoreInput` — three of five fields | no errors, full coverage, no down-migration cap | Defer until `score` has a third caller. |
    | 3 | `ChangedFile.At` | falls back to `Path` — correct for three providers of four | **Not yet**, and the reason is recorded. Revisit when a second thing calls the analyzers. |
    | 4 | The gate re-derived in a determinism test | keeps passing while asserting less | **Do it.** Three lines. |

    The general form, for the next survey: **a zero value that means "nothing is wrong" is a
    default, and a default is a decision nobody wrote down.** The enums were fixed by making the
    zero invalid. What remains are fields whose zero is *valid* and permissive, and the fix for
    those is to make the caller say it out loud.

    Not implemented, per the owner's instruction to propose rather than build.

## 17. Fixture conventions

The `context/` group is a fourth shape, added by the S11 patch: a changeset plus a `context.json`
snapshot plus an `expected.json` that names **both** grades — `grade` with the snapshot and
`gradeWithoutContext` without it. Writing both down is what makes the direction of the rule
visible as data rather than only as prose, and the test asserts the first is never better than
the second. `TestEveryRuleHasAFixture` does not scan `context/`, so these are free to reuse rule
IDs.

**64 fixture directories under `testdata/fixtures/`**, in four groups:

| Group | Count | What it holds |
| --- | --- | --- |
| `postgres/` | 32 | 27 rule fixtures, 4 `DOWN*` for the three down-migration validation levels plus the directory form, and 1 `ENCODING*` pinning the decoding seam |
| `kubernetes/` | 15 | one per rule |
| `terraform/` | 9 | one per **active** rule; `TF003` is retired and correctly has none |
| `context/` | 8 | one per lock duration band, both `SET NOT NULL` cases, the size-fallback path, and an incomplete snapshot |

**`TestEveryRuleHasAFixture` scans `postgres/`, `kubernetes/` and `terraform/`**, with `TF003`
declared retired so its absence is an assertion rather than a hole. It does not scan `context/`.

The 47 that predate S11 are load-bearing as a set: a mandatory regression asserts all of them
grade identically when no snapshot is supplied, which is what proves context is optional rather
than merely usually-absent.

**Two fixtures may not claim the same rule** — `TestEveryRuleHasAFixture` rejects it, so a
fixture proving something *about* an existing rule takes a non-table rule ID, as `DOWN*` and
`ENCODING*` both do. A non-table rule ID additionally has to assert `downMigrations`, which is
what stops that escape hatch from becoming a way to assert nothing.

`ENCODING001_utf8_bom` carries a UTF-8 BOM on both its up and down migrations and asserts the
PG002 the statement deserves. Before the strip in `parser.PgQuery.Parse` it reported PG027
"could not be parsed": still F, so nothing unsafe merged, but the certificate named no rule and
described nothing the migration did. Fail-closed guarantees the grade, not that the grade is
explicable — and on Windows-authored migrations the inexplicable one was the common case.
Only a *leading* BOM is stripped; an interior one is corruption and still fails.

Three shapes, all resolved by `provider.Fake`:

```
<fixture>/migrations/...        every file is ADDED — what a migration PR looks like
<fixture>/old/... + new/...     the two trees are diffed by path; the old//new/ prefix is
                                stripped, so a finding's File is "deployment.yaml"
<fixture>/plan/...              every file is ADDED, holding *.tfplan.json — what a plan
                                committed for review looks like (S12)
```

Every fixture carries `expected.json`:

```json
{
  "rule": "PG001",
  "note": "why this fixture is shaped the way it is",
  "findings": [
    {"ruleId":"PG001","file":"...","line":1,
     "reversibility":"IRREVERSIBLE","lockHazard":"EXCLUSIVE","wantUndoStep":false}
  ],
  "downMigrations": [{"migration":"0001_x","exists":true,"parses":true,"symmetric":true}]
}
```

Rules for writing and changing fixtures:

- **Expectations pin classification only** — rule ID, file, line, reversibility, lock hazard,
  and whether an undo step exists. Rationale and undo-step *wording* are never pinned, so
  improving the prose a human reads cannot break the suite.
- **`wantUndoStep` is presence, not text.** An IRREVERSIBLE finding must carry no undo step;
  offering one would be a lie, and `TestFixtureAssertionsAreWellFormed` rejects it.
- **Every Kubernetes image is digest-pinned** except in `K8S008_image_not_pinned`. Otherwise
  K8S008 fires in every fixture and the expectations stop meaning anything.
- **Line 0 means file-level**, which is what all Kubernetes findings currently use. Whether S3
  can attribute a YAML line number is an implementation decision, not a spec one.
- Unknown JSON fields are rejected on load, so `lockHazzard` fails loudly instead of silently
  asserting the zero value.

`internal/fixture` is a **layout addition** beyond §5, made because the analyzer tests, the
engine tests (S4), and the renderer golden tests (S5) all read this format. Four copies of the
loader would drift.
