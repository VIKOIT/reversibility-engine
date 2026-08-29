# Proposal — the next places correctness rests on remembering

**STATUS: PROPOSED. Nothing here is implemented.** It is a survey and a recommendation, for the
owner to rule on before any of it is built.

`domain.Located` worked because the compiler enforced it. `engine.Candidate(f.Path)` stopped
compiling, and the compiler then found both remaining unqualified call sites during the change
rather than leaving them for a field audit. That is the property worth reproducing, so the
question is where else correctness currently rests on somebody remembering.

## The criterion this survey used

> **Where does an omitted, unset, or not-yet-applied value mean the permissive answer?**

Not "where is there a zero value" — `TestZeroValuesAreNeverSafe` already holds that line for the
enums, and it holds it well. The gap is one level out: **struct-literal inputs and multi-step
transformations, where the mistake is not writing a wrong value but not writing one at all.** An
unset `Reversibility` is invalid and fails closed. An unset *field* is a valid zero that means
"nothing to worry about".

Ordered by what the mistake costs.

---

## 1. `GateConditions.PolicyIgnored` — omitting the field opens the gate

```go
type GateConditions struct {
    Coverage      Coverage
    PolicyIgnored int
}
```

`Coverage` fails closed when unset: the zero value is `""`, `Full()` compares against
`CoverageFull`, and `""` is not it. **`PolicyIgnored` does the opposite.** Its zero is `0`, which
means "no candidate was excluded by policy", which is one of the three conditions for `PASS`. A
caller who assembles a `GateConditions` and does not think about policy ignores gets the
permissive answer, silently.

It is one production call site today and it is correct. The risk is structural, not live: the
struct's own doc comment says it is passed by value "so that a caller who has not thought about
them cannot compile" — and that is true of `Coverage` and false of `PolicyIgnored`.

**Proposed:** make the absence unrepresentable rather than zero-valued. Either a constructor
(`domain.NewGateConditions(coverage, ignored)`) that cannot be called with one argument, or give
the count a named type whose zero is invalid the way `Coverage`'s is — `PolicyIgnored` becomes
`IgnoredCandidates` with `None` and `Some(n)`. The constructor is the smaller change and gets
most of the value.

**Severity: highest of the four.** It is the only one on this list whose failure mode is a `PASS`.

## 2. `scoreInput` — four fields, three of which fail open if omitted

```go
score(scoreInput{findings: …, downMigrations: …, analyzerErrors: …, outcome: …, unsupported: …})
```

- `outcome` unset → the `default:` branch grades **F**. Correct, and deliberately so.
- `analyzerErrors` unset → no errors → permissive.
- `unsupported` unset → coverage FULL → permissive.
- `downMigrations` unset → `downMigrationsAreSound(nil)` → no cap → permissive.

Three of five omissions are the safe-looking answer. `outcome` is the one that was thought about,
because it is the one the P0 taught, and the others have not had their incident yet.

This is assembled in exactly two places, both inside `Certify`, and both correct. But `score` is
the function a future contributor will call from a third place — a "score just these findings"
helper, a preview mode, a test — and the type will not stop them.

**Proposed:** `scoreInput` is unexported, so the cheapest correct fix is a constructor in the same
package that takes every field positionally, plus a `go vet`-style test asserting no composite
literal of `scoreInput` appears outside it. Better, if the owner wants to spend more: make the
three permissive fields explicit sums — `analyzerErrors` as `Result[[]string]` is overkill in Go,
but `unsupported` could carry the coverage decision rather than being re-derived from `len()`.

## 3. `domain.ChangedFile.At` — an unstamped file silently classifies wrongly

`At` empty means "`Path` is already in the decision namespace", and `Location()` falls back to
`Path`. **That is the correct answer for three of the four providers**, which is exactly what
makes it dangerous: it is right so often that the one case where it is wrong looks like the
others.

The one case is the filesystem provider, and getting it wrong reinstates the Django P0. Today
`Certify` stamps every file itself, so no caller can get this wrong — but an analyzer invoked
directly, a future orchestrator, or a second entry point beside `Certify` all can.

**Proposed:** nothing yet, and this is the recommendation. The stamping choke point is one line in
one function and is currently airtight; a type that distinguishes stamped from unstamped
`ChangedFile` would touch every analyzer signature to close a hole nobody can reach. **Worth
revisiting the moment a second thing calls the analyzers.** Recorded here so that moment is
recognised rather than discovered.

## 4. The gate is re-derived in a test

`internal/engine/determinism_test.go` computes `cert.Grade.Gate(...)` and compares it to
`cert.AIGateStatus`. `Grade.Gate`'s own doc says: *"This is the single definition of the gate. No
caller may re-derive it, because a second definition is a second chance to get it wrong in the
permissive direction."*

The test is legitimate — it is asserting the certificate is self-consistent, which is a real
property — but it re-derives the gate's *inputs*, and those it constructs by hand from certificate
fields. If a third condition is ever added to `GateConditions`, this call site will keep compiling
and keep passing while asserting a weaker property than it appears to.

**Proposed:** the assertion should compare against a recorded value rather than a recomputed one,
or `GateConditions` should be derivable from a certificate in one exported function that both the
engine and the test use. The second is better and is three lines.

---

## What is already enforced, and should not be revisited

Listed so a future survey does not re-open them:

| Invariant | Enforced by |
| --- | --- |
| An unset enum is never safe | `TestZeroValuesAreNeverSafe` |
| A path-keyed decision uses one namespace | `domain.Located`, a distinct type |
| Every classification has a table row | `TestEveryClassificationHasATableRow` |
| The drivers are unreachable from analysis | `internal/snapshot/architecture_test.go` |
| No documented number drifts from its authority | `TestDocumentedNumbersAreDerivedFromTheirAuthority` |
| No rendered field is machine-specific | `TestNoRenderedOutputCarriesAMachineSpecificValue` |

## Recommendation

Do **1** and **4**. They are small, and 1 is the only one that can produce a `PASS`.

Defer **2** until `score` has a third caller; the constructor is cheap but the test forbidding
composite literals is the kind of guard that annoys people more than it protects them while the
call sites are two and both correct.

Do not do **3** yet, and write down why, which this document is.

**The general form, for the next survey:** a zero value that means "nothing is wrong" is a
default, and a default is a decision nobody wrote down. The enums were fixed by making the zero
invalid. The remaining cases are all fields where the zero is *valid* and permissive, and the fix
for those is to make the caller say it out loud.
