// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
)

// realEngine wires the production analyzers. The engine package itself never imports them —
// only its tests and the delivery layer do — which is what keeps the orchestrator generic.
func realEngine() *engine.Engine {
	return engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})
}

func sql(path, content string) domain.ChangedFile {
	return domain.ChangedFile{Path: path, Status: domain.StatusAdded, Current: []byte(content)}
}

func certify(t *testing.T, files ...domain.ChangedFile) domain.ReversibilityCertificate {
	t.Helper()

	cert, err := realEngine().Certify(context.Background(), files)
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	return cert
}

// stubAnalyzer lets the orchestrator be tested without depending on what the real rules happen
// to say today.
type stubAnalyzer struct {
	name     string
	supports bool
	findings []domain.Finding
	err      error
	panics   any
}

func (s stubAnalyzer) Name() string         { return s.name }
func (s stubAnalyzer) Supports(string) bool { return s.supports }

func (s stubAnalyzer) Analyze(context.Context, []domain.ChangedFile) ([]domain.Finding, error) {
	if s.panics != nil {
		panic(s.panics)
	}
	return s.findings, s.err
}

func TestCertifyGradeA(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001_index.up.sql", "CREATE INDEX CONCURRENTLY idx_orders_status ON orders (status);"),
		sql("migrations/0001_index.down.sql", "DROP INDEX CONCURRENTLY idx_orders_status;"),
	)

	if cert.Grade != domain.GradeA {
		t.Errorf("Grade = %q, want A. Blockers: %v", cert.Grade, cert.Blockers)
	}
	if cert.AIGateStatus != domain.GatePass {
		t.Errorf("AIGateStatus = %q, want PASS", cert.AIGateStatus)
	}
	if !cert.Applicable {
		t.Error("Applicable = false for a changeset containing a migration")
	}
	if cert.SchemaVersion != domain.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", cert.SchemaVersion, domain.SchemaVersion)
	}
	if len(cert.UndoPlan) == 0 {
		t.Error("a reversible changeset produced no undo plan")
	}
}

func TestCertifyGradeF(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001_drop.up.sql", "DROP TABLE legacy_orders;"),
		sql("migrations/0001_drop.down.sql", "CREATE TABLE legacy_orders (id bigint);"),
	)

	if cert.Grade != domain.GradeF {
		t.Fatalf("Grade = %q, want F", cert.Grade)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q, want FAIL", cert.AIGateStatus)
	}
	if len(cert.Blockers) == 0 {
		t.Error("grade F with no blockers")
	}

	// The undo plan must say plainly that no complete undo exists, not list a partial script
	// that looks like one.
	if len(cert.UndoPlan) == 0 || !strings.Contains(string(cert.UndoPlan[0]), "NO COMPLETE UNDO") {
		t.Errorf("undo plan does not announce that no complete undo exists: %v", cert.UndoPlan)
	}
}

// The owner's ruling in §15.1, end to end: a missing down migration caps an otherwise perfect
// changeset at C.
func TestCertifyMissingDownMigrationCapsAtC(t *testing.T) {
	t.Parallel()

	cert := certify(t, sql("migrations/0001_add.up.sql", "ALTER TABLE orders ADD COLUMN notes text;"))

	if cert.Grade != domain.GradeC {
		t.Errorf("Grade = %q, want C. Findings: %d, DownMigrations: %+v", cert.Grade, len(cert.Findings), cert.DownMigrations)
	}
	if len(cert.DownMigrations) != 1 || cert.DownMigrations[0].Exists {
		t.Errorf("DownMigrations = %+v, want one entry with Exists false", cert.DownMigrations)
	}
}

// A changeset with nothing to analyze has nothing to say, and saying so is not the same as
// approving it. Grade A once meant both, which is the P0 this asserts against.
func TestCertifyNoCandidates(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		domain.ChangedFile{Path: "README.md", Status: domain.StatusModified, Current: []byte("# hi")},
		domain.ChangedFile{Path: "main.go", Status: domain.StatusModified, Current: []byte("package main")},
	)

	if cert.Grade != domain.GradeNotApplicable {
		t.Errorf("Grade = %q, want N/A. A means analyzed and found reversible, and nothing else", cert.Grade)
	}
	if cert.Outcome != domain.OutcomeNoCandidates {
		t.Errorf("Outcome = %q, want NO_CANDIDATES", cert.Outcome)
	}
	if cert.Applicable {
		t.Error("Applicable = true for a changeset no analyzer claims")
	}
	if cert.AIGateStatus != domain.GateNotApplicable {
		t.Errorf("AIGateStatus = %q, want NOT_APPLICABLE", cert.AIGateStatus)
	}
	if len(cert.Findings) != 0 {
		t.Errorf("got %d findings, want 0", len(cert.Findings))
	}
	if len(cert.Blockers) != 0 {
		t.Errorf("Blockers = %v; nothing is wrong with a docs-only change", cert.Blockers)
	}
}

// A changeset with no files at all must behave the same way.
func TestCertifyNoFiles(t *testing.T) {
	t.Parallel()

	cert := certify(t)

	if cert.Grade != domain.GradeNotApplicable || cert.Applicable {
		t.Errorf("Grade = %q, Applicable = %v; want N/A / false", cert.Grade, cert.Applicable)
	}
	if cert.Outcome != domain.OutcomeNoCandidates {
		t.Errorf("Outcome = %q, want NO_CANDIDATES", cert.Outcome)
	}
	if cert.InputDigest == "" {
		t.Error("an empty changeset still needs a digest")
	}
}

// The Django case, and the reason the P0 was filed. Thirteen files that plainly are migrations,
// no analyzer that can read one of them, and the engine must say so rather than grade it.
func TestCertifyUnsupportedContent(t *testing.T) {
	t.Parallel()

	var files []domain.ChangedFile
	for _, name := range []string{
		"0001_initial.py", "0002_alter_permission_name_max_length.py", "0003_alter_user_email_max_length.py",
	} {
		files = append(files, domain.ChangedFile{
			Path:    "django/contrib/auth/migrations/" + name,
			Status:  domain.StatusAdded,
			Current: []byte("from django.db import migrations\n"),
		})
	}

	cert := certify(t, files...)

	if cert.Grade != domain.GradeNotApplicable {
		t.Errorf("Grade = %q, want N/A", cert.Grade)
	}
	if cert.Outcome != domain.OutcomeUnsupportedContent {
		t.Errorf("Outcome = %q, want UNSUPPORTED_CONTENT", cert.Outcome)
	}
	if cert.AIGateStatus == domain.GatePass {
		t.Error("AIGateStatus = PASS for three unread migrations")
	}

	// The message has to name what it saw. A bare "not applicable" is what made this readable
	// as "nothing here" for as long as it shipped.
	if len(cert.Blockers) != 1 {
		t.Fatalf("Blockers = %v, want exactly one line naming the directory", cert.Blockers)
	}
	for _, want := range []string{"3 files", "django/contrib/auth/migrations", ".py", "not assessed"} {
		if !strings.Contains(cert.Blockers[0], want) {
			t.Errorf("blocker %q does not mention %q", cert.Blockers[0], want)
		}
	}
}

// One readable file makes the run ANALYZED, and the unreadable siblings make it PARTIAL. The
// two are separate axes and this is the test that says so.
func TestCertifyMixedContentIsAnalyzedAndPartial(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		domain.ChangedFile{
			Path:    "db/migrate/0001_add_index.sql",
			Status:  domain.StatusAdded,
			Current: []byte("CREATE INDEX CONCURRENTLY idx ON orders (status);\n"),
		},
		domain.ChangedFile{
			Path:    "app/migrations/0001_initial.py",
			Status:  domain.StatusAdded,
			Current: []byte("from django.db import migrations\n"),
		},
	)

	if cert.Outcome != domain.OutcomeAnalyzed {
		t.Errorf("Outcome = %q, want ANALYZED", cert.Outcome)
	}
	if !cert.Applicable {
		t.Error("Applicable = false despite a .sql migration being claimed")
	}
	if cert.Coverage != domain.CoveragePartial {
		t.Errorf("Coverage = %q, want PARTIAL", cert.Coverage)
	}

	// The list, not a count. A reviewer's next question is always "which ones".
	if len(cert.UnanalyzedFiles) != 1 {
		t.Fatalf("UnanalyzedFiles = %+v, want the one .py file", cert.UnanalyzedFiles)
	}
	if got := cert.UnanalyzedFiles[0]; got.Path != "app/migrations/0001_initial.py" || got.Reason == "" {
		t.Errorf("UnanalyzedFiles[0] = %+v, want the .py path with a reason", got)
	}

	// The gate closes even though the grade did not move.
	if cert.AIGateStatus == domain.GatePass {
		t.Error("AIGateStatus = PASS on a partially covered changeset; an agent must not merge what was only partly understood")
	}
}

// The ruling, stated as a test: PARTIAL never changes the grade.
//
// The same SQL is certified twice, once alone and once beside a migration the engine cannot
// read. Every measured field must be identical. Inventing severity from ignorance is the exact
// mirror of inventing safety from it, and it would be the easier of the two mistakes to defend.
func TestPartialCoverageNeverChangesTheGrade(t *testing.T) {
	t.Parallel()

	sql := []domain.ChangedFile{
		{
			Path:    "db/migrate/0001_add_index.up.sql",
			Status:  domain.StatusAdded,
			Current: []byte("CREATE INDEX CONCURRENTLY idx ON orders (status);\n"),
		},
		{
			Path:    "db/migrate/0001_add_index.down.sql",
			Status:  domain.StatusAdded,
			Current: []byte("DROP INDEX CONCURRENTLY idx;\n"),
		},
	}

	unreadable := domain.ChangedFile{
		Path:    "db/migrate/0002_backfill.rb",
		Status:  domain.StatusAdded,
		Current: []byte("class Backfill < ActiveRecord::Migration\nend\n"),
	}

	full := certify(t, sql...)
	partial := certify(t, append(append([]domain.ChangedFile{}, sql...), unreadable)...)

	if full.Coverage != domain.CoverageFull || partial.Coverage != domain.CoveragePartial {
		t.Fatalf("coverage = %q and %q, want FULL and PARTIAL — the test is not exercising what it claims",
			full.Coverage, partial.Coverage)
	}

	if partial.Grade != full.Grade {
		t.Errorf("Grade = %q with an unreadable sibling, %q without; coverage must not move the grade",
			partial.Grade, full.Grade)
	}
	if partial.EffectiveGrade != full.EffectiveGrade {
		t.Errorf("EffectiveGrade = %q with an unreadable sibling, %q without",
			partial.EffectiveGrade, full.EffectiveGrade)
	}
	if len(partial.Findings) != len(full.Findings) {
		t.Errorf("findings = %d with an unreadable sibling, %d without", len(partial.Findings), len(full.Findings))
	}
	if len(partial.Blockers) != len(full.Blockers) {
		t.Errorf("blockers = %v with an unreadable sibling, %v without", partial.Blockers, full.Blockers)
	}

	// And the gate is the one thing that does move.
	if full.AIGateStatus != domain.GatePass {
		t.Fatalf("the fully covered certificate gates %q, want PASS — the test is not exercising what it claims",
			full.AIGateStatus)
	}
	if partial.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q on partial coverage, want FAIL", partial.AIGateStatus)
	}
}

// THE PANIC BOUNDARY. A panic anywhere beneath the orchestrator becomes grade F with
// ENGINE_PANIC — never a pass, never a silent success, never a lost result.
func TestCertifyRecoversFromPanic(t *testing.T) {
	t.Parallel()

	e := engine.New([]analyzer.Analyzer{
		stubAnalyzer{name: "exploder", supports: true, panics: "deliberate test panic"},
	})

	cert, err := e.Certify(context.Background(), []domain.ChangedFile{sql("migrations/0001.up.sql", "SELECT 1;")})

	if err == nil {
		t.Fatal("a panic produced no error")
	}
	if !errors.Is(err, domain.ErrAnalyzerPanic) {
		t.Errorf("error = %v, want it to wrap ErrAnalyzerPanic", err)
	}

	if cert.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
	if cert.AIGateStatus != domain.GatePass && cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q is not a valid status", cert.AIGateStatus)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("a panic gated %q; it must always FAIL", cert.AIGateStatus)
	}
	if cert.SchemaVersion != domain.SchemaVersion {
		t.Errorf("the panic certificate is not well formed: SchemaVersion = %q", cert.SchemaVersion)
	}

	var found bool
	for _, f := range cert.Findings {
		if f.RuleID == domain.RuleEnginePanic {
			found = true
			if f.Reversibility != domain.ReversibilityUnknown {
				t.Errorf("ENGINE_PANIC reversibility = %q, want UNKNOWN", f.Reversibility)
			}
			if f.UndoStep != "" {
				t.Errorf("ENGINE_PANIC offers an undo step %q", f.UndoStep)
			}
		}
	}
	if !found {
		t.Errorf("no ENGINE_PANIC finding; findings were %+v", cert.Findings)
	}
	if len(cert.Blockers) == 0 {
		t.Error("a panic produced no blockers")
	}
}

// A panic must not be able to hide behind a well-behaved analyzer that found nothing.
func TestPanicOutranksACleanAnalyzer(t *testing.T) {
	t.Parallel()

	e := engine.New([]analyzer.Analyzer{
		stubAnalyzer{name: "aaa-clean", supports: true},
		stubAnalyzer{name: "zzz-exploder", supports: true, panics: errors.New("boom")},
	})

	cert, err := e.Certify(context.Background(), []domain.ChangedFile{sql("migrations/0001.up.sql", "SELECT 1;")})
	if err == nil {
		t.Fatal("expected an error")
	}
	if cert.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
}

// An analyzer error must force F even when every finding that did arrive looks safe.
func TestAnalyzerErrorForcesF(t *testing.T) {
	t.Parallel()

	safe := domain.Finding{
		RuleID: "PG020", File: "a.sql", Line: 1, Statement: "ALTER TABLE t ADD COLUMN c text",
		Reversibility: domain.ReversibilityReversible, LockHazard: domain.LockNone,
		Rationale: "a rationale long enough to be a sentence", UndoStep: "ALTER TABLE t DROP COLUMN c;",
	}

	e := engine.New([]analyzer.Analyzer{
		stubAnalyzer{name: "good", supports: true, findings: []domain.Finding{safe}},
		stubAnalyzer{name: "broken", supports: true, err: domain.ErrParserUnavailable},
	})

	cert, err := e.Certify(context.Background(), []domain.ChangedFile{sql("migrations/0001.up.sql", "SELECT 1;")})
	if err == nil {
		t.Fatal("an analyzer failure produced no error")
	}
	if cert.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q, want FAIL", cert.AIGateStatus)
	}
}

// A cancelled context must produce a failing certificate, never a passing empty one.
func TestCertifyRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cert, err := realEngine().Certify(ctx, []domain.ChangedFile{sql("migrations/0001.up.sql", "DROP TABLE t;")})
	if err == nil {
		t.Fatal("a cancelled context produced no error")
	}
	if cert.Grade != domain.GradeF || cert.AIGateStatus != domain.GateFail {
		t.Errorf("Grade = %q gate = %q, want F / FAIL", cert.Grade, cert.AIGateStatus)
	}
}

// Certificates are serialized by the renderers, where a nil slice becomes null and an empty one
// becomes []. Two runs with the same meaning must not serialize differently.
func TestCertificateSlicesAreNeverNil(t *testing.T) {
	t.Parallel()

	certs := []domain.ReversibilityCertificate{
		certify(t),
		certify(t, sql("migrations/0001.up.sql", "CREATE TABLE t (id bigint);"), sql("migrations/0001.down.sql", "DROP TABLE t;")),
		certify(t, sql("migrations/0001.up.sql", "DROP TABLE t;")),
	}

	for i, cert := range certs {
		if cert.Findings == nil {
			t.Errorf("certificate %d has nil Findings", i)
		}
		if cert.UndoPlan == nil {
			t.Errorf("certificate %d has nil UndoPlan", i)
		}
		if cert.Blockers == nil {
			t.Errorf("certificate %d has nil Blockers", i)
		}
		if cert.DownMigrations == nil {
			t.Errorf("certificate %d has nil DownMigrations", i)
		}
	}
}

// The undo plan unwinds the changeset, so it runs backwards through the findings.
func TestUndoPlanIsInReverseOrder(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001_a.up.sql", "CREATE TABLE first (id bigint);"),
		sql("migrations/0001_a.down.sql", "DROP TABLE first;"),
		sql("migrations/0002_b.up.sql", "CREATE TABLE second (id bigint);"),
		sql("migrations/0002_b.down.sql", "DROP TABLE second;"),
	)

	if cert.Grade != domain.GradeA {
		t.Fatalf("Grade = %q, want A. Blockers: %v", cert.Grade, cert.Blockers)
	}
	if len(cert.UndoPlan) != 2 {
		t.Fatalf("got %d undo steps, want 2: %v", len(cert.UndoPlan), cert.UndoPlan)
	}

	// The second migration applied is the first that has to come off.
	if !strings.Contains(string(cert.UndoPlan[0]), "second") {
		t.Errorf("undo plan does not start with the last change applied: %v", cert.UndoPlan)
	}
	if !strings.Contains(string(cert.UndoPlan[1]), "first") {
		t.Errorf("undo plan does not end with the first change applied: %v", cert.UndoPlan)
	}
}

// An UNKNOWN finding means a change nobody understood, so the plan must not look complete.
func TestUndoPlanRefusesCompletenessOnUnknown(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001_ok.up.sql", "CREATE TABLE t (id bigint);"),
		sql("migrations/0001_ok.down.sql", "DROP TABLE t;"),
		// Parses cleanly, matches no rule in the table. This held a GRANT until PG032
		// classified it; when CLUSTER gains a rule, swap in another uncovered construct rather
		// than dropping the case, because the path it exercises is the one that matters.
		sql("migrations/0002_weird.up.sql", "CLUSTER orders USING orders_pkey;"),
		sql("migrations/0002_weird.down.sql", "SELECT 1;"),
	)

	if cert.Grade != domain.GradeF {
		t.Fatalf("Grade = %q, want F", cert.Grade)
	}
	if len(cert.UndoPlan) == 0 || !strings.Contains(string(cert.UndoPlan[0]), "NO COMPLETE UNDO") {
		t.Errorf("undo plan claims completeness despite an UNKNOWN finding: %v", cert.UndoPlan)
	}
}

// Kubernetes and Postgres findings must appear in one certificate, sorted together.
func TestCertifyMixedChangeset(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001.up.sql", "CREATE INDEX CONCURRENTLY i ON t (c);"),
		sql("migrations/0001.down.sql", "DROP INDEX CONCURRENTLY i;"),
		domain.ChangedFile{
			Path:     "k8s/ns.yaml",
			Status:   domain.StatusRemoved,
			Previous: []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: legacy\n"),
		},
	)

	if cert.Grade != domain.GradeF {
		t.Fatalf("Grade = %q, want F (the namespace removal is irreversible)", cert.Grade)
	}

	kinds := map[string]bool{}
	for _, f := range cert.Findings {
		kinds[f.RuleID[:2]] = true
	}
	if !kinds["PG"] || !kinds["K8"] {
		t.Errorf("expected findings from both analyzers, got %+v", cert.Findings)
	}

	// Findings must be sorted by file, then line, then rule.
	for i := 1; i < len(cert.Findings); i++ {
		prev, cur := cert.Findings[i-1], cert.Findings[i]
		if prev.File > cur.File {
			t.Errorf("findings are not sorted by file: %q before %q", prev.File, cur.File)
		}
	}
}
