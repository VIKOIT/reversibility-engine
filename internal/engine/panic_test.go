package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/abdo-s1/reversibility-engine/internal/analyzer"
	"github.com/abdo-s1/reversibility-engine/internal/domain"
	"github.com/abdo-s1/reversibility-engine/internal/engine"
)

// panickingAnalyzer detonates at a chosen point in the analyzer contract.
type panickingAnalyzer struct {
	name       string
	panicIn    string
	value      any
	findings   []domain.Finding
	analyzeErr error
	downStatus []domain.DownMigrationStatus
}

func (p panickingAnalyzer) Name() string {
	if p.panicIn == "Name" {
		panic(p.value)
	}
	if p.name == "" {
		return "panicky"
	}
	return p.name
}

func (p panickingAnalyzer) Supports(string) bool {
	if p.panicIn == "Supports" {
		panic(p.value)
	}
	return true
}

func (p panickingAnalyzer) Analyze(context.Context, []domain.ChangedFile) ([]domain.Finding, error) {
	if p.panicIn == "Analyze" {
		panic(p.value)
	}
	return p.findings, p.analyzeErr
}

func (p panickingAnalyzer) ValidateDownMigrations(context.Context, []domain.ChangedFile) ([]domain.DownMigrationStatus, error) {
	if p.panicIn == "ValidateDownMigrations" {
		panic(p.value)
	}
	return p.downStatus, nil
}

// customPanic is a panic value that is neither a string nor an error, which is the case a naive
// recover handler formats badly or drops.
type customPanic struct{ detail string }

// THE PANIC BOUNDARY SWEEP.
//
// Every panic, from every point in the analyzer contract, with every kind of panic value, must
// end in grade F with ENGINE_PANIC. Not a crash, not a pass, not a partial result. The engine is
// the last thing standing between a destructive migration and a merge, so it does not get to
// fall over quietly.
func TestPanicBoundarySweep(t *testing.T) {
	t.Parallel()

	panicPoints := []string{"Analyze", "Supports", "Name", "ValidateDownMigrations"}

	panicValues := map[string]any{
		"string":        "deliberate panic",
		"error":         errors.New("deliberate error panic"),
		"custom struct": customPanic{detail: "deliberate"},
		"integer":       42,
		"nil pointer":   (*customPanic)(nil),
		"slice":         []string{"a", "b"},
	}

	for _, point := range panicPoints {
		for valueName, value := range panicValues {
			t.Run(point+"/"+valueName, func(t *testing.T) {
				t.Parallel()

				// Name() is reached inside Certify only when an analyzer fails, so the
				// error is supplied to make that path real rather than hypothetical.
				stub := panickingAnalyzer{panicIn: point, value: value}
				if point == "Name" {
					stub.analyzeErr = errors.New("analysis failed")
				}

				e := engine.New([]analyzer.Analyzer{stub})

				cert, err := e.Certify(context.Background(), []domain.ChangedFile{{
					Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;"),
				}})

				assertPanicCertificate(t, cert, err)
			})
		}
	}
}

// A panic raised with nil is the awkward case: recover() returns nil, so a handler that checks
// only "if r != nil" misses it entirely and the panic continues unwinding.
func TestPanicWithNilValue(t *testing.T) {
	t.Parallel()

	e := engine.New([]analyzer.Analyzer{panickingAnalyzer{panicIn: "Analyze", value: nil}})

	cert, err := e.Certify(context.Background(), []domain.ChangedFile{{
		Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;"),
	}})

	// Go converts panic(nil) into a *runtime.PanicNilError, so recover() sees a non-nil value
	// and the boundary holds. This test exists to notice if that ever changes.
	assertPanicCertificate(t, cert, err)
}

// Runtime faults — nil dereference, index out of range, nil map write — are panics too, and are
// far likelier in real analyzer code than a deliberate panic() call.
func TestRuntimeFaultsAreRecovered(t *testing.T) {
	t.Parallel()

	faults := map[string]func(){
		"nil map write":      func() { var m map[string]string; m["boom"] = "x" },
		"index out of range": func() { s := []int{}; _ = s[5] },
		"nil dereference":    func() { var p *customPanic; _ = p.detail },
		"divide by zero":     func() { zero := 0; _ = 1 / zero },
		"slice bounds":       func() { s := make([]int, 3); _ = s[1:9] },
		"type assertion":     func() { var v any = "string"; _ = v.(int) },
		"close nil channel":  func() { var ch chan int; close(ch) },
	}

	for name, fault := range faults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			e := engine.New([]analyzer.Analyzer{faultingAnalyzer{fault: fault}})

			cert, err := e.Certify(context.Background(), []domain.ChangedFile{{
				Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;"),
			}})

			assertPanicCertificate(t, cert, err)
		})
	}
}

type faultingAnalyzer struct{ fault func() }

func (faultingAnalyzer) Name() string         { return "faulting" }
func (faultingAnalyzer) Supports(string) bool { return true }

func (f faultingAnalyzer) Analyze(context.Context, []domain.ChangedFile) ([]domain.Finding, error) {
	f.fault()
	return nil, nil
}

// A panic must not be able to hide behind analyzers that succeeded, however many there are and
// whatever they found.
func TestPanicOutranksAnyNumberOfCleanAnalyzers(t *testing.T) {
	t.Parallel()

	safe := domain.Finding{
		RuleID: "PG020", File: "a.sql", Line: 1, Statement: "x",
		Reversibility: domain.ReversibilityReversible, LockHazard: domain.LockNone,
		Rationale: "a rationale long enough to be a sentence", UndoStep: "UNDO;",
	}

	analyzers := []analyzer.Analyzer{
		panickingAnalyzer{name: "a-clean", findings: []domain.Finding{safe}},
		panickingAnalyzer{name: "b-clean", findings: []domain.Finding{safe}},
		panickingAnalyzer{name: "c-boom", panicIn: "Analyze", value: "boom"},
		panickingAnalyzer{name: "d-clean", findings: []domain.Finding{safe}},
	}

	cert, err := engine.New(analyzers).Certify(context.Background(), []domain.ChangedFile{{
		Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;"),
	}})

	assertPanicCertificate(t, cert, err)

	// The clean findings are discarded on purpose: a partial conclusion from a broken run is
	// not evidence, and presenting three reassuring findings beside a crash would be worse than
	// presenting none.
	for _, f := range cert.Findings {
		if f.RuleID == "PG020" {
			t.Error("findings from before the panic survived into the certificate")
		}
	}
}

// The engine must survive being handed a changeset that is itself malformed.
func TestCertifyWithHostileInput(t *testing.T) {
	t.Parallel()

	inputs := map[string][]domain.ChangedFile{
		"nil slice":            nil,
		"empty slice":          {},
		"zero-value file":      {{}},
		"nil contents":         {{Path: "a.sql", Status: domain.StatusModified}},
		"invalid status":       {{Path: "a.sql", Status: domain.ChangeStatus("WHO_KNOWS"), Current: []byte("SELECT 1;")}},
		"empty path":           {{Path: "", Status: domain.StatusAdded, Current: []byte("SELECT 1;")}},
		"duplicate paths":      {{Path: "a.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;")}, {Path: "a.sql", Status: domain.StatusRemoved, Previous: []byte("SELECT 2;")}},
		"binary content":       {{Path: "a.sql", Status: domain.StatusAdded, Current: []byte{0x00, 0xff, 0xfe}}},
		"removed with content": {{Path: "a.sql", Status: domain.StatusRemoved, Current: []byte("SELECT 1;")}},
	}

	for name, files := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cert, _ := realEngine().Certify(context.Background(), files)

			if !cert.Grade.Valid() {
				t.Errorf("Grade = %q is not valid", cert.Grade)
			}
			if cert.AIGateStatus != cert.Grade.Gate() {
				t.Errorf("gate %q disagrees with grade %q", cert.AIGateStatus, cert.Grade)
			}
			if cert.Findings == nil || cert.UndoPlan == nil || cert.Blockers == nil || cert.DownMigrations == nil {
				t.Error("certificate contains a nil slice")
			}
		})
	}
}

// A finding whose verdict the domain does not recognise means an analyzer produced something
// incoherent. That is precisely what UNKNOWN is for, and it must grade F rather than merely
// failing the "all REVERSIBLE" test and capping at B.
func TestUnrecognisedVerdictGradesF(t *testing.T) {
	t.Parallel()

	for _, verdict := range []domain.Reversibility{"", "reversible", "PROBABLY_FINE", "SAFE"} {
		t.Run(string(verdict), func(t *testing.T) {
			t.Parallel()

			corrupt := domain.Finding{
				RuleID: "BROKEN", File: "a.sql", Line: 1, Statement: "x",
				Reversibility: verdict, LockHazard: domain.LockNone,
				Rationale: "an analyzer produced this",
			}

			cert, _ := engine.New([]analyzer.Analyzer{
				panickingAnalyzer{name: "corrupt", findings: []domain.Finding{corrupt}},
			}).Certify(context.Background(), []domain.ChangedFile{{
				Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;"),
			}})

			if cert.Grade != domain.GradeF {
				t.Errorf("verdict %q graded %q, want F", verdict, cert.Grade)
			}
			if cert.AIGateStatus != domain.GateFail {
				t.Errorf("verdict %q gated %q, want FAIL", verdict, cert.AIGateStatus)
			}
			if len(cert.Blockers) == 0 {
				t.Error("an incoherent verdict produced no blockers")
			}
		})
	}
}

// assertPanicCertificate holds every property the panic certificate must satisfy.
func assertPanicCertificate(t *testing.T, cert domain.ReversibilityCertificate, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("a panic produced no error")
	}
	if !errors.Is(err, domain.ErrAnalyzerPanic) {
		t.Errorf("error = %v, want it to wrap ErrAnalyzerPanic", err)
	}

	if cert.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F", cert.Grade)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q, want FAIL", cert.AIGateStatus)
	}
	if cert.SchemaVersion != domain.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", cert.SchemaVersion, domain.SchemaVersion)
	}
	if len(cert.Blockers) == 0 {
		t.Error("no blockers explaining the failure")
	}

	var sawPanicFinding bool
	for _, f := range cert.Findings {
		if f.RuleID == domain.RuleEnginePanic {
			sawPanicFinding = true
			if f.Reversibility != domain.ReversibilityUnknown {
				t.Errorf("ENGINE_PANIC reversibility = %q, want UNKNOWN", f.Reversibility)
			}
			if f.UndoStep != "" {
				t.Errorf("ENGINE_PANIC offers an undo step %q", f.UndoStep)
			}
		}
	}
	if !sawPanicFinding {
		t.Errorf("no ENGINE_PANIC finding; findings were %+v", cert.Findings)
	}

	// The plan must not look like a script somebody can run.
	if len(cert.UndoPlan) == 0 {
		t.Error("the panic certificate carries no undo plan at all")
	}

	if cert.Findings == nil || cert.UndoPlan == nil || cert.Blockers == nil || cert.DownMigrations == nil {
		t.Error("the panic certificate contains a nil slice")
	}
}
