// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// Engine orchestrates analyzers and turns their findings into a certificate.
//
// It holds no mutable state. The analyzer registry is fixed at construction, and everything
// else lives for the duration of a single Certify call, so one Engine is safe to share.
type Engine struct {
	analyzers []analyzer.Analyzer
	log       *slog.Logger
	policy    *policy.Policy
	today     time.Time
	context   *snapshot.Set
}

// Option configures an Engine.
type Option func(*Engine)

// RunOption carries a fact about one changeset into one Certify call.
//
// It is separate from Option because the Engine holds no mutable state and is safe to share:
// anything that varies per changeset has to arrive with the changeset, not be stored on the
// orchestrator between runs.
type RunOption func(*runConfig)

// runConfig is everything a single Certify call knows beyond the files themselves.
type runConfig struct {
	// enumerated is every path the provider found in the changeset, whether or not its content
	// was read.
	//
	// Coverage is a statement about what exists, so it is measured against this rather than
	// against the files. When it is absent the engine falls back to the paths of the files it
	// was given, which is the old behaviour and is strictly weaker: a file nobody read leaves no
	// trace in that list, which is how renaming a migration directory turned the check off.
	enumerated []string
	// ignoredByPolicy are candidate paths a policy excluded before the provider read them.
	//
	// The engine cannot discover these for itself. An ignored path is never read, which is the
	// property that makes an ignore list meaningful, so the only place that knows one existed
	// is the include predicate that rejected it.
	ignoredByPolicy []string
	// deadIgnores are `ignore:` patterns that matched no path in this changeset.
	//
	// The engine cannot discover these for the same reason it cannot discover the ignored
	// paths: selection happens before Certify is called. Dead waivers it does discover, because
	// applying waivers is its own job.
	deadIgnores []string
	// root is where the changeset's paths sit in the repository they were read from.
	//
	// A changeset path is relative to whatever the provider was pointed at, and for the
	// filesystem provider that is a directory the caller chose. `revctl check ./migrations`
	// therefore strips the one segment that says these files are migrations. Classification
	// asks a question about location, so it has to ask it of the location the file actually
	// has — see runConfig.locator, and docs/SPECIFICATION.md §16.10.
	//
	// Empty for every provider whose paths are already repository-relative, which is all of
	// them except the filesystem one, so the default is the behaviour that was always correct.
	root string
	// anchor names the marker that established the project root — `.git`, `.reversibility.yml` —
	// or "" when none was found and paths resolved absolutely.
	//
	// It reaches the certificate so a user can see which root a glob was resolved against. Without
	// it, a pattern that matches nothing is undebuggable: the answer depends on a walk up the
	// filesystem that the user cannot see and, in a monorepo, may not predict.
	anchor string
}

// locator returns the mapping from this run's changeset paths into the decision namespace.
//
// It is used for decisions and for nothing else. **Every path the certificate reports stays
// exactly as the caller named it** — a finding, an unanalyzed file, and an ignored path are all
// still addressable with the command the reader just ran. What moves is only the path the engine
// asks "is this plausibly a migration", "does an ignore glob cover this", "does a waiver cover
// this", and "does an analyzer claim this" about, because every one of those is a question about
// where the file sits and not about how it was named on a command line.
func (c runConfig) locator() domain.Locator { return domain.NewLocator(c.root) }

// anchoredPrefix is the prefix when it is safe to render, and "" otherwise.
//
// With no project marker the prefix is an absolute path. Reporting it would tell a reader exactly
// what they want to know and would also publish the analyst's home directory into a pull request
// comment, so the anchor being absent is itself the answer: paths resolved absolutely, and a
// project-relative glob cannot match. policyWarnings says that in words.
func (c runConfig) anchoredPrefix() string {
	if c.anchor == "" {
		return ""
	}
	return c.root
}

// IgnoredByPolicy records the candidate files a policy excluded from this run.
//
// They never counted against coverage — the engine was capable of reading them and was told
// not to — and they do close the merge gate, because an ignore is a human decision and a human
// decision never buys an agent a merge.
func IgnoredByPolicy(paths []string) RunOption {
	return func(c *runConfig) { c.ignoredByPolicy = append(c.ignoredByPolicy, paths...) }
}

// Enumerated records every path the provider found, read or not.
//
// This is the input coverage is measured against, and supplying it is what makes strict coverage
// strict. Without it the engine can only see the files it was handed, and a file that exists and
// was never read is invisible — see docs/SPECIFICATION.md §16.9.
func Enumerated(paths []string) RunOption {
	return func(c *runConfig) { c.enumerated = append(c.enumerated, paths...) }
}

// RootedAt records where in the repository the changeset's paths are rooted.
//
// It exists because candidate detection must not depend on how the analysis root was named, and
// without it, it did: `revctl check django/contrib/auth/migrations` reports its files as
// `0001_initial.py`, having stripped exactly the segment RULES.md §3 keys on, and reached
// NO_CANDIDATES and exit 0 where `revctl check django/contrib/auth` reached UNSUPPORTED_CONTENT
// and exit 2 over the same thirteen files. The permissive answer was the one the documented
// invocation found.
//
// Callers whose paths are already repository-relative — git, GitHub, the fake — pass nothing,
// which is the empty prefix and the behaviour they always had. provider.ResolveRoot computes it
// for the filesystem provider. See docs/SPECIFICATION.md §16.10.
func RootedAt(prefix, anchor string) RunOption {
	return func(c *runConfig) {
		c.root = prefix
		c.anchor = anchor
	}
}

// DeadIgnores records `ignore:` patterns that matched nothing in this changeset.
//
// A pattern that matches nothing is dead config, and **dead config in a safety tool reads as
// protection the user does not have.** It reaches the certificate rather than only a terminal,
// for the reason UnanalyzedFiles does: the reader must never have to infer what their
// configuration did.
func DeadIgnores(patterns []string) RunOption {
	return func(c *runConfig) { c.deadIgnores = append(c.deadIgnores, patterns...) }
}

// WithPolicy sets the resolved policy. A nil policy means none, which is the default and must
// behave exactly as it did before policies existed.
func WithPolicy(p *policy.Policy) Option {
	return func(e *Engine) { e.policy = p }
}

// WithToday fixes the day waiver expiry is measured against.
//
// It exists so that expiry is testable and so a past run can be reproduced. It defaults to the
// system date, which is the only value in this codebase that is not derived from the input —
// hence it is injected rather than read at the point of use, where it would be untestable and
// invisible.
func WithToday(t time.Time) Option {
	return func(e *Engine) { e.today = t }
}

// WithContext supplies production metadata collected by `revctl snapshot`.
//
// It is a value that has already been read from a file, never a connection. The engine does not
// talk to a database or a cluster during analysis, and this option is the shape of that
// promise: there is nothing here to connect with.
//
// A nil set means no context, which is the default and must behave exactly as it did before
// snapshots existed.
func WithContext(set *snapshot.Set) Option {
	return func(e *Engine) { e.context = set }
}

// WithLogger sets the logger. Logging is diagnostic only: nothing the engine logs affects a
// grade, and a certificate never contains anything that varies between runs.
func WithLogger(log *slog.Logger) Option {
	return func(e *Engine) {
		if log != nil {
			e.log = log
		}
	}
}

// New returns an Engine that runs the given analyzers.
//
// It accepts the analyzer.Analyzer interface rather than concrete types so that this package
// never imports an analyzer implementation, and so tests can substitute one freely.
func New(analyzers []analyzer.Analyzer, opts ...Option) *Engine {
	e := &Engine{
		analyzers: append([]analyzer.Analyzer(nil), analyzers...),
		log:       slog.New(slog.NewTextHandler(discard{}, nil)),
		today:     time.Now(),
	}
	for _, opt := range opts {
		opt(e)
	}

	// A stable registry order keeps findings from arriving in a different sequence between
	// runs before they are sorted, which keeps the panic path deterministic too.
	sort.SliceStable(e.analyzers, func(i, j int) bool {
		return e.analyzers[i].Name() < e.analyzers[j].Name()
	})

	return e
}

// Certify analyzes a changeset and returns its reversibility certificate.
//
// The returned certificate is ALWAYS valid and safe to act on, including when the error is
// non-nil: on any failure it is a fully populated grade F with the reason in Blockers. The error
// exists so operators can distinguish a broken toolchain from a dangerous migration, never so a
// caller can decide the certificate is missing.
//
// This method owns the single recover boundary in the codebase. A panic anywhere beneath it
// becomes grade F with RuleID ENGINE_PANIC — never a pass, never a silent success.
func (e *Engine) Certify(
	ctx context.Context,
	files []domain.ChangedFile,
	opts ...RunOption,
) (cert domain.ReversibilityCertificate, err error) {
	var run runConfig
	for _, opt := range opts {
		opt(&run)
	}
	sort.Strings(run.ignoredByPolicy)
	// Every path-keyed decision in this run is made in one namespace, and this is where the
	// changeset enters it. Stamping happens once, here, rather than at each decision site,
	// because a decision site that forgets is exactly the defect — four times over, see
	// domain.Located.
	//
	// It is stamped and not rewritten: ChangedFile.Path is untouched, so the digest below, every
	// finding, and every rendered path stay as the caller named them.
	locate := run.locator()
	files = located(files, locate)

	// The policy is an input to the verdict, so it is part of what the digest attributes the
	// verdict to. It is mixed in only when a policy exists, which keeps every digest ever
	// produced without one exactly as it was.
	digest := InputDigest(files)
	if e.policy != nil {
		digest = combineDigests(digest, e.policy.Digest)
	}
	if e.context != nil {
		digest = combineDigests(digest, e.context.Digest)
	}

	catalogVersion, catalogDigest := e.catalogs(files)
	if catalogDigest != "" {
		digest = combineDigests(digest, catalogDigest)
	}

	defer func() {
		r := recover()
		if r == nil {
			return
		}

		// A panic means the engine's own reasoning failed. Whatever it had concluded so far is
		// discarded, because a partial conclusion from a broken run is not evidence.
		err = fmt.Errorf("%w: %v", domain.ErrAnalyzerPanic, r)
		cert = panicCertificate(digest, r)

		e.log.Error("engine recovered from a panic", "panic", r, "digest", digest)
	}()

	if err := ctx.Err(); err != nil {
		wrapped := fmt.Errorf("engine: %w", err)
		return failedCertificate(digest, wrapped), wrapped
	}

	findings, downMigrations, analyzerErrors := e.run(ctx, files)

	domain.SortFindings(findings)

	// Enrichment runs before the policy so that a waived finding carries its production detail
	// too: a reader judging whether an accepted risk still holds needs the size of the table as
	// much as anyone else does.
	//
	// It cannot change a classification — see snapshot.Enrich — so it cannot change what the
	// policy then matches, and it cannot change a grade.
	findings = e.context.Enrich(findings)

	// A policy that cannot be resolved is a broken run, not a run without a policy. Continuing
	// would enforce something nobody configured.
	decision, err := e.policy.Apply(findings, e.today, locate)
	if err != nil {
		wrapped := fmt.Errorf("engine: %w", err)
		return failedCertificate(digest, wrapped), wrapped
	}

	// What the run was able to do at all, decided before anything is graded. A grade is a
	// statement about files that were read, so the question of whether any were comes first.
	//
	// The enumeration is what coverage is measured against. Falling back to the files' own paths
	// keeps every caller that has not been updated working, and is honestly weaker: a file that
	// exists and was not read cannot appear in that list at all.
	enumerated := run.enumerated
	if len(enumerated) == 0 {
		enumerated = pathsOf(files)
	}

	// Classification happens in the repository's namespace, not in the changeset's. A path here is
	// relative to whatever the provider was pointed at, and `revctl check ./migrations` therefore
	// strips the one segment that says these files are migrations — see runConfig.locator.
	outcome, unsupported := e.outcome(files, enumerated, locate)

	// Grade is computed from every finding, waived ones included. It states what the evidence
	// says about the change, and no configuration may move it: a waiver accepts a risk, it does
	// not make a DROP TABLE reversible.
	scored := score(scoreInput{
		findings:       decision.All,
		downMigrations: downMigrations,
		analyzerErrors: analyzerErrors,
		outcome:        outcome,
		unsupported:    unsupported,
	})
	grade, blockers := scored.grade, scored.blockers

	// EffectiveGrade is the same scoring with waived findings set aside. It is what a CI
	// threshold compares against, and it is deliberately NOT what AIGateStatus follows.
	effective := grade
	if len(decision.Waived) > 0 {
		effective = score(scoreInput{
			findings:       decision.Findings,
			downMigrations: downMigrations,
			analyzerErrors: analyzerErrors,
			outcome:        outcome,
			unsupported:    unsupported,
		}).grade
	}

	// Coverage is the second axis, and it is computed from the same candidate list the outcome
	// was. It never touches the grade above — see domain.Coverage — and reaches only the gate.
	coverage := domain.CoverageFull
	if len(unsupported) > 0 {
		coverage = domain.CoveragePartial
	}

	// Policy-ignored candidates are deliberately NOT part of coverage. The engine could have
	// read them; it was told not to, and coverage describes capability rather than permission.
	// They reach the gate instead, through GateConditions.
	// Only ignored *candidates* close the gate. §16.8's rule is that a human decision never
	// buys an agent a merge, and it is about files that might be migrations — ignoring a
	// README that happens to live beside them is not somebody accepting a reversibility risk,
	// it is somebody telling the engine what a README is.
	//
	// Every ignored path is still listed on the certificate. Transparency about what was
	// skipped and the gate's arithmetic are separate questions, and conflating them here would
	// make the config escape hatch unusable: the only way to satisfy strict coverage would
	// permanently close the gate.
	ignoredCandidates := 0
	for _, p := range run.ignoredByPolicy {
		if Candidate(locate(p)) {
			ignoredCandidates++
		}
	}

	conditions := domain.GateConditions{
		Coverage:      coverage,
		PolicyIgnored: ignoredCandidates,
	}

	cert = domain.ReversibilityCertificate{
		SchemaVersion:   domain.SchemaVersion,
		Grade:           grade,
		EffectiveGrade:  effective,
		AIGateStatus:    grade.Gate(conditions),
		Outcome:         outcome,
		Coverage:        coverage,
		UnanalyzedFiles: nonNilUnanalyzed(unanalyzedFiles(unsupported, locate)),
		IgnoredByPolicy: nonNilStrings(run.ignoredByPolicy),
		Applicable:      outcome.Certifies(),
		InputDigest:     digest,
		PolicyDigest:    e.policyDigest(),
		CatalogVersion:  catalogVersion,
		Findings:        nonNilFindings(decision.Findings),
		Waived:          nonNilWaived(decision.Waived),
		UndoPlan:        nonNilPlan(buildUndoPlan(decision.All)),
		Blockers:        nonNilStrings(blockers),
		GradeCauses:     nonNilStrings(scored.causes),
		ContextWarnings: e.contextWarnings(),
		// Not nonNilStrings: the field is omitempty, so an empty slice would not survive a JSON
		// round trip and the certificate would stop being its own fixed point.
		PolicyWarnings: policyWarnings(run.deadIgnores, decision.DeadWaivers, run.anchor),
		PathAnchor:     run.anchor,
		// Only alongside an anchor: without one the prefix is absolute, and §16.14 forbids a
		// machine-specific value in a rendered field.
		PathPrefix:     run.anchoredPrefix(),
		DownMigrations: nonNilStatuses(downMigrations),
	}

	if len(analyzerErrors) > 0 {
		return cert, fmt.Errorf("engine: %d analyzer(s) failed: %v", len(analyzerErrors), analyzerErrors)
	}
	return cert, nil
}

// run invokes every analyzer and collects findings, down-migration status, and failures.
//
// An analyzer that fails does not stop the others: the certificate should show everything that
// is wrong at once. The failure is recorded and forces grade F regardless of what the rest found.
func (e *Engine) run(ctx context.Context, files []domain.ChangedFile) (findings []domain.Finding, down []domain.DownMigrationStatus, failures []string) {
	for _, a := range e.analyzers {
		got, err := a.Analyze(ctx, files)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", a.Name(), err))
			continue
		}
		findings = append(findings, got...)

		// Down-migration status is an optional capability; analyzers that do not care about
		// migrations simply do not implement it.
		validator, ok := a.(analyzer.DownMigrationValidator)
		if !ok {
			continue
		}

		statuses, err := validator.ValidateDownMigrations(ctx, files)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s down-migration validation: %v", a.Name(), err))
			continue
		}
		down = append(down, statuses...)
	}

	sort.Strings(failures)
	sort.SliceStable(down, func(i, j int) bool { return down[i].Migration < down[j].Migration })

	return findings, down, failures
}

// Supports reports whether any registered analyzer claims the path.
//
// Providers use this to avoid reading files nobody will look at. It is the engine's answer
// rather than a list of extensions maintained separately, so a new analyzer widens the net
// automatically instead of requiring two places to be updated in step.
func (e *Engine) Supports(at domain.Located) bool {
	for _, a := range e.analyzers {
		if a.Supports(at) {
			return true
		}
	}
	return false
}

// panicCertificate is the verdict when the engine itself failed.
func panicCertificate(digest string, r any) domain.ReversibilityCertificate {
	finding := domain.Finding{
		RuleID: domain.RuleEnginePanic,

		// No file can be blamed: the failure is the engine's, not the changeset's.
		File:          "",
		Line:          0,
		Statement:     "",
		Reversibility: domain.ReversibilityUnknown,
		LockHazard:    domain.LockExclusive,
		Rationale:     "The engine panicked while analyzing this changeset, so no part of its verdict can be trusted.",
	}

	return domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		EffectiveGrade: domain.GradeF,
		AIGateStatus:   domain.GradeF.Gate(domain.GateConditions{Coverage: domain.CoverageFull}),

		// ANALYZED, and Applicable stays true: the engine was asked for an opinion and failed to
		// produce one, which is not the same as having nothing to say. Neither non-analyzed
		// outcome would be honest here — both mean "there was nothing to read", and there was.
		//
		// Coverage is FULL because no file was skipped: the run failed, it did not skip. What
		// went wrong is a grade F with a blocker, and overloading the coverage axis with it
		// would make PARTIAL mean two different things. FULL is safe here whatever it means —
		// a PASS needs grade A, and this is an F.
		Outcome:         domain.OutcomeAnalyzed,
		Coverage:        domain.CoverageFull,
		Applicable:      true,
		InputDigest:     digest,
		Findings:        []domain.Finding{finding},
		UndoPlan:        []domain.UndoStep{noCompleteUndo},
		Blockers:        []string{fmt.Sprintf("the engine panicked: %v", r)},
		DownMigrations:  []domain.DownMigrationStatus{},
		UnanalyzedFiles: []domain.UnanalyzedFile{},
		IgnoredByPolicy: []string{},
		GradeCauses:     []string{"graded F: the engine could not complete this run"},
	}
}

// failedCertificate is the verdict when the engine could not start.
func failedCertificate(digest string, err error) domain.ReversibilityCertificate {
	return domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		EffectiveGrade: domain.GradeF,
		AIGateStatus:   domain.GradeF.Gate(domain.GateConditions{Coverage: domain.CoverageFull}),

		// ANALYZED for the same reason as the panic certificate: the run engaged with a real
		// changeset and could not finish. That is an F, not an absence of subject matter, and
		// not a coverage gap either.
		Outcome:         domain.OutcomeAnalyzed,
		Coverage:        domain.CoverageFull,
		Applicable:      true,
		InputDigest:     digest,
		Findings:        []domain.Finding{},
		UndoPlan:        []domain.UndoStep{noCompleteUndo},
		Blockers:        []string{fmt.Sprintf("analysis did not run: %v", err)},
		DownMigrations:  []domain.DownMigrationStatus{},
		UnanalyzedFiles: []domain.UnanalyzedFile{},
		IgnoredByPolicy: []string{},
		GradeCauses:     []string{"graded F: the engine could not complete this run"},
	}
}

// UnavailableCertificate is the verdict when the changeset could not be obtained at all.
//
// It exists for the transport layer: a provider that fails — a rate limit, a network error, a
// file too large to fetch — never reaches Certify, so something has to turn that into a graded
// answer rather than a silent skip. Grading whatever files did arrive would be worse: a
// confident verdict on a change the engine only partly saw.
//
// ruleID names the failing subsystem, such as domain.RuleProviderError. The result is always
// grade F with a gate of FAIL.
func UnavailableCertificate(ruleID string, cause error) domain.ReversibilityCertificate {
	rationale := fmt.Sprintf(
		"The changeset could not be retrieved, so nothing about it could be analyzed: %v.", cause)

	return domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		EffectiveGrade: domain.GradeF,
		AIGateStatus:   domain.GradeF.Gate(domain.GateConditions{Coverage: domain.CoverageFull}),

		// Applicable stays true: the engine was asked for an opinion and could not form one,
		// which is not the same as having nothing to say. NO_CANDIDATES would be the exact
		// wrong answer — it would report "there was nothing here" about a changeset nobody
		// managed to look at. Coverage is FULL for the same reason it is on the other two
		// failure certificates: nothing was skipped, the fetch failed, and this is an F.
		Outcome:    domain.OutcomeAnalyzed,
		Coverage:   domain.CoverageFull,
		Applicable: true,

		// No digest: hashing a changeset that was never retrieved would attribute the
		// certificate to an input that does not exist.
		InputDigest: "",

		Findings: []domain.Finding{{
			RuleID:        ruleID,
			Reversibility: domain.ReversibilityUnknown,
			LockHazard:    domain.LockExclusive,
			Rationale:     rationale,
		}},
		UndoPlan:        []domain.UndoStep{noCompleteUndo},
		Blockers:        []string{rationale},
		DownMigrations:  []domain.DownMigrationStatus{},
		UnanalyzedFiles: []domain.UnanalyzedFile{},
		IgnoredByPolicy: []string{},
		GradeCauses:     []string{"graded F: the engine could not complete this run"},
	}
}

// The four helpers below normalize nil slices to empty ones.
//
// This is not cosmetic: encoding/json renders a nil slice as null and an empty slice as [], so a
// nil would make two certificates with identical meaning serialize differently. Determinism is a
// hard requirement, and it has to survive the renderers.

func nonNilFindings(in []domain.Finding) []domain.Finding {
	if in == nil {
		return []domain.Finding{}
	}
	return in
}

func nonNilPlan(in []domain.UndoStep) []domain.UndoStep {
	if in == nil {
		return []domain.UndoStep{}
	}
	return in
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilStatuses(in []domain.DownMigrationStatus) []domain.DownMigrationStatus {
	if in == nil {
		return []domain.DownMigrationStatus{}
	}
	return in
}

// pathsOf is the fallback enumeration: the paths of the files that were actually read.
func pathsOf(files []domain.ChangedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// policyWarnings turns dead configuration into lines a human can act on.
//
// **Worded as an observation, never an accusation.** A waiver written for a rule that did not
// fire on this pull request is doing exactly what it should, and a message implying otherwise
// would train people to ignore the whole category — which is the failure mode this is meant to
// prevent, arrived at from the other side.
//
// Order is the order the patterns appear in the policy file, so the certificate is deterministic
// and a reader can find the line. nil rather than an empty slice when there is nothing to say:
// the field is omitempty, and an empty one would not survive a JSON round trip.
func policyWarnings(deadIgnores, deadWaivers []string, anchor string) []string {
	if len(deadIgnores)+len(deadWaivers) == 0 {
		return nil
	}

	out := make([]string, 0, len(deadIgnores)+len(deadWaivers)+1)
	for _, pattern := range deadIgnores {
		out = append(out, "ignore pattern "+pattern+" matched no file in this changeset")
	}
	for _, waiver := range deadWaivers {
		out = append(out, "waiver "+waiver+" covered no finding in this changeset")
	}

	// The most likely explanation, when there is one, said at the point the reader meets the
	// problem. With no project root every path resolved absolutely, so a pattern written relative
	// to the project could not have matched whatever else is true of it — and that is a fact
	// about the tree, not about the pattern, which is not something a user would think to check.
	if anchor == "" {
		out = append(out, "no project root was found (no .git, .hg, .svn or "+
			policy.FileName+" above the analysis root), so paths were resolved absolutely and a "+
			"project-relative pattern cannot match")
	}

	return out
}

// located stamps each file with where it sits in the decision namespace.
//
// It copies rather than mutating in place: Certify is called with a slice the caller owns, and
// an orchestrator that writes into its argument is one shared Engine away from two runs
// corrupting each other. Path is never touched — the stamp is additional, so every digest,
// finding and rendered path is exactly what it was.
func located(files []domain.ChangedFile, locate domain.Locator) []domain.ChangedFile {
	out := make([]domain.ChangedFile, len(files))
	copy(out, files)

	for i := range out {
		out[i].At = locate(out[i].Path)
	}
	return out
}

func nonNilUnanalyzed(in []domain.UnanalyzedFile) []domain.UnanalyzedFile {
	if in == nil {
		return []domain.UnanalyzedFile{}
	}
	return in
}

// discard is a no-op io.Writer for the default logger, so that an engine constructed without
// WithLogger stays silent rather than writing to stderr from a library.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func nonNilWaived(in []domain.WaivedFinding) []domain.WaivedFinding {
	if in == nil {
		return []domain.WaivedFinding{}
	}
	return in
}

// policyDigest returns the digest of the policy in force, or "" when there is none.
func (e *Engine) policyDigest() string {
	if e.policy == nil {
		return ""
	}
	return e.policy.Digest
}

// contextWarnings reports what was wrong with the production snapshots supplied, if any.
func (e *Engine) contextWarnings() []string {
	if e.context == nil || len(e.context.Warnings) == 0 {
		return nil
	}
	return append([]string(nil), e.context.Warnings...)
}

// catalogs reports the identity of every data-table catalog that actually applied to this
// changeset.
//
// "Actually applied" is the whole point. A catalog is an input to a verdict only when the
// analyzer that owns it claimed a file, so the digest is mixed in only then — which keeps every
// digest ever produced for a changeset with no Terraform plan exactly as it was, and keeps a
// stored certificate comparable against a rerun.
func (e *Engine) catalogs(files []domain.ChangedFile) (version, digest string) {
	var versions, digests []string

	for _, a := range e.analyzers {
		versioner, ok := a.(analyzer.CatalogVersioner)
		if !ok {
			continue
		}

		claimed := false
		for _, f := range files {
			if a.Supports(f.Location()) {
				claimed = true
				break
			}
		}
		if !claimed {
			continue
		}

		versions = append(versions, versioner.CatalogVersion())
		digests = append(digests, versioner.CatalogDigest())
	}

	if len(versions) == 0 {
		return "", ""
	}

	// The registry is already sorted by analyzer name, so these are stable.
	return strings.Join(versions, ","), strings.Join(digests, ",")
}
