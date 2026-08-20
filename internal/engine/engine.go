package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Engine orchestrates analyzers and turns their findings into a certificate.
//
// It holds no mutable state. The analyzer registry is fixed at construction, and everything
// else lives for the duration of a single Certify call, so one Engine is safe to share.
type Engine struct {
	analyzers []analyzer.Analyzer
	log       *slog.Logger
}

// Option configures an Engine.
type Option func(*Engine)

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
func (e *Engine) Certify(ctx context.Context, files []domain.ChangedFile) (cert domain.ReversibilityCertificate, err error) {
	digest := InputDigest(files)

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

	in := scoreInput{
		findings:       findings,
		downMigrations: downMigrations,
		analyzerErrors: analyzerErrors,
		applicable:     e.applicable(files),
	}

	grade, blockers := score(in)

	cert = domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          grade,
		AIGateStatus:   grade.Gate(),
		Applicable:     in.applicable,
		InputDigest:    digest,
		Findings:       nonNilFindings(findings),
		UndoPlan:       nonNilPlan(buildUndoPlan(findings)),
		Blockers:       nonNilStrings(blockers),
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
func (e *Engine) Supports(path string) bool {
	for _, a := range e.analyzers {
		if a.Supports(path) {
			return true
		}
	}
	return false
}

// applicable reports whether any analyzer claims any file in the changeset.
func (e *Engine) applicable(files []domain.ChangedFile) bool {
	for _, f := range files {
		if e.Supports(f.Path) {
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
		SchemaVersion: domain.SchemaVersion,
		Grade:         domain.GradeF,
		AIGateStatus:  domain.GradeF.Gate(),

		// Applicable stays true: the engine was asked for an opinion and failed to produce one,
		// which is not the same as having nothing to say.
		Applicable:     true,
		InputDigest:    digest,
		Findings:       []domain.Finding{finding},
		UndoPlan:       []domain.UndoStep{noCompleteUndo},
		Blockers:       []string{fmt.Sprintf("the engine panicked: %v", r)},
		DownMigrations: []domain.DownMigrationStatus{},
	}
}

// failedCertificate is the verdict when the engine could not start.
func failedCertificate(digest string, err error) domain.ReversibilityCertificate {
	return domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		AIGateStatus:   domain.GradeF.Gate(),
		Applicable:     true,
		InputDigest:    digest,
		Findings:       []domain.Finding{},
		UndoPlan:       []domain.UndoStep{noCompleteUndo},
		Blockers:       []string{fmt.Sprintf("analysis did not run: %v", err)},
		DownMigrations: []domain.DownMigrationStatus{},
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
		SchemaVersion: domain.SchemaVersion,
		Grade:         domain.GradeF,
		AIGateStatus:  domain.GradeF.Gate(),

		// Applicable stays true: the engine was asked for an opinion and could not form one,
		// which is not the same as having nothing to say.
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
		UndoPlan:       []domain.UndoStep{noCompleteUndo},
		Blockers:       []string{rationale},
		DownMigrations: []domain.DownMigrationStatus{},
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

// discard is a no-op io.Writer for the default logger, so that an engine constructed without
// WithLogger stays silent rather than writing to stderr from a library.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
