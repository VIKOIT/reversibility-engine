// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// TestAnalyzeFixtures drives the analyzer over every PostgreSQL fixture.
//
// One fixture per rule, PG001 through PG027, plus the DOWN* fixtures. Green since S2. A rule
// whose fixture is deleted stops being tested, which is why internal/fixture also asserts that
// every rule ID still has one.
func TestAnalyzeFixtures(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	cases, err := fixture.Cases(root, "postgres")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	files := provider.NewFake(root)
	subject := postgres.New()

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			changed, err := files.ChangedFiles(ctx, tc.Ref)
			if err != nil {
				t.Fatalf("resolving changeset: %v", err)
			}
			if len(changed) == 0 {
				t.Fatalf("fixture resolved to an empty changeset")
			}

			got, err := subject.Analyze(ctx, changed)
			if err != nil {
				t.Fatalf("Analyze: %v\n\nfixture rationale: %s", err, tc.Expect.Note)
			}

			domain.SortFindings(got)

			if diff := cmp.Diff(tc.Expect.Findings, fixture.Project(got)); diff != "" {
				t.Errorf("classification mismatch (-want +got):\n%s\n\nfixture rationale: %s", diff, tc.Expect.Note)
			}

			assertFindingsAreExplained(t, got)
		})
	}
}

// A finding a reader cannot act on is a finding that gets ignored. Every one must explain
// itself, and anything short of a sentence is not an explanation.
func assertFindingsAreExplained(t *testing.T, findings []domain.Finding) {
	t.Helper()

	for _, f := range findings {
		if strings.TrimSpace(f.Rationale) == "" {
			t.Errorf("%s at %s:%d has no rationale", f.RuleID, f.File, f.Line)
			continue
		}
		if len(f.Rationale) < 20 {
			t.Errorf("%s at %s:%d has a rationale too short to explain anything: %q",
				f.RuleID, f.File, f.Line, f.Rationale)
		}

		if f.Reversibility == domain.ReversibilityIrreversible && f.UndoStep != "" {
			t.Errorf("%s at %s:%d is IRREVERSIBLE but offers an undo step %q; that is a lie the certificate must never tell",
				f.RuleID, f.File, f.Line, f.UndoStep)
		}

		if len(f.Statement) > domain.MaxStatementLength {
			t.Errorf("%s at %s:%d: statement is %d chars, exceeding the %d bound",
				f.RuleID, f.File, f.Line, len(f.Statement), domain.MaxStatementLength)
		}
	}
}

func TestSupports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"migrations/0001_init.up.sql", true},
		{"migrations/0001_init.down.sql", true},
		{"MIGRATIONS/0001.SQL", true},
		{"k8s/deployment.yaml", false},
		{"README.md", false},
		{"sql", false},
		{"", false},
		{"weird.sql.bak", false},
		{"dir.sql/file.txt", false},
	}

	subject := postgres.New()
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := subject.Supports(tt.path); got != tt.want {
				t.Errorf("Supports(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	if got := postgres.New().Name(); got != "postgres" {
		t.Errorf("Name() = %q, want %q", got, "postgres")
	}
}

// A cancelled context must not yield a clean, empty result that the scorer would read as
// "nothing risky here".
func TestAnalyzeRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := postgres.New().Analyze(ctx, nil)
	if err == nil {
		t.Fatalf("Analyze with a cancelled context returned nil error and %d findings", len(got))
	}
}

// TestValidateDownMigrationsFixtures drives the three validation levels over the fixtures that
// declare a downMigrations expectation.
func TestValidateDownMigrationsFixtures(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	cases, err := fixture.Cases(root, "postgres")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	files := provider.NewFake(root)
	sqlParser := parser.NewPgQuery()

	asserted := 0
	for _, tc := range cases {
		if len(tc.Expect.DownMigrations) == 0 {
			continue
		}
		asserted++

		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			changed, err := files.ChangedFiles(ctx, tc.Ref)
			if err != nil {
				t.Fatalf("resolving changeset: %v", err)
			}

			got, err := postgres.ValidateDownMigrations(ctx, sqlParser, changed)
			if err != nil {
				t.Fatalf("ValidateDownMigrations: %v", err)
			}

			projected := make([]fixture.DownMigration, 0, len(got))
			for _, s := range got {
				projected = append(projected, fixture.DownMigration{
					Migration: s.Migration,
					Exists:    s.Exists,
					Parses:    s.Parses,
					Symmetric: s.Symmetric,
				})
			}

			if diff := cmp.Diff(tc.Expect.DownMigrations, projected); diff != "" {
				t.Errorf("down-migration validation mismatch (-want +got):\n%s\n\nfixture rationale: %s", diff, tc.Expect.Note)
			}

			// A failed level must say what it objected to, or a reviewer cannot dismiss a
			// false positive without reading the analyzer's source.
			for _, s := range got {
				if !s.Symmetric && len(s.SymmetryNotes) == 0 {
					t.Errorf("migration %s failed a validation level but recorded no note explaining why", s.Migration)
				}
			}
		})
	}

	if asserted == 0 {
		t.Fatalf("no fixture asserts down-migration validation; the DOWN* fixtures are not being exercised")
	}
}

func TestValidateDownMigrationsRejectsNilParser(t *testing.T) {
	t.Parallel()

	got, err := postgres.ValidateDownMigrations(context.Background(), nil, nil)
	if err == nil {
		t.Fatalf("nil parser returned nil error and %d statuses; a missing parser must fail, not pass", len(got))
	}
	if !errors.Is(err, domain.ErrParserUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrParserUnavailable", err)
	}
}

// stubParser lets the classification rules be exercised with no cgo in the path, which is the
// entire point of the SQLParser seam in ADR/0001.
type stubParser struct {
	stmts []parser.Statement
	err   error
}

func (s stubParser) Parse(context.Context, string) ([]parser.Statement, error) {
	return s.stmts, s.err
}

// The seam has to be real: an analyzer built on a substitute parser must classify normally.
func TestAnalyzeThroughTheParserSeam(t *testing.T) {
	t.Parallel()

	subject := postgres.NewWithParser(stubParser{stmts: []parser.Statement{
		{Kind: parser.KindDropTable, Object: "users", SQL: "DROP TABLE users;", Line: 1},
	}})

	got, err := subject.Analyze(context.Background(), []domain.ChangedFile{{
		Path:    "migrations/0001_x.up.sql",
		Status:  domain.StatusAdded,
		Current: []byte("DROP TABLE users;"),
	}})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if got[0].RuleID != "PG001" || got[0].Reversibility != domain.ReversibilityIrreversible {
		t.Errorf("got %s/%s, want PG001/IRREVERSIBLE", got[0].RuleID, got[0].Reversibility)
	}
}

// A file that will not parse must become a visible UNKNOWN finding, not an error that discards
// the findings of every other file in the changeset.
func TestAnalyzeReportsParseFailureAsUnknown(t *testing.T) {
	t.Parallel()

	subject := postgres.NewWithParser(stubParser{err: errors.New("syntax error at or near \"FLARBLE\"")})

	got, err := subject.Analyze(context.Background(), []domain.ChangedFile{{
		Path:    "migrations/0001_broken.up.sql",
		Status:  domain.StatusAdded,
		Current: []byte("\nALTER TABLE orders FLARBLE COLUMN quantity;\n"),
	}})
	if err != nil {
		t.Fatalf("Analyze returned an error instead of a finding: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if got[0].RuleID != "PG027" {
		t.Errorf("RuleID = %q, want PG027", got[0].RuleID)
	}
	if got[0].Reversibility != domain.ReversibilityUnknown {
		t.Errorf("Reversibility = %q, want UNKNOWN", got[0].Reversibility)
	}
	if got[0].UndoStep != "" {
		t.Errorf("an unparseable migration was given an undo step %q", got[0].UndoStep)
	}
	// The blank first line must not swallow the attribution.
	if got[0].Line != 2 {
		t.Errorf("Line = %d, want 2 (the first non-empty line)", got[0].Line)
	}
}

func TestAnalyzeWithoutParser(t *testing.T) {
	t.Parallel()

	got, err := postgres.NewWithParser(nil).Analyze(context.Background(), nil)
	if err == nil {
		t.Fatalf("a nil parser returned nil error and %d findings", len(got))
	}
	if !errors.Is(err, domain.ErrParserUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrParserUnavailable", err)
	}
}

// Down migrations describe the rollback, not the change being assessed. Classifying them would
// report the undo of a safe change as a destructive one.
func TestAnalyzeIgnoresDownMigrations(t *testing.T) {
	t.Parallel()

	got, err := postgres.New().Analyze(context.Background(), []domain.ChangedFile{
		{Path: "migrations/0001_x.up.sql", Status: domain.StatusAdded, Current: []byte("CREATE TABLE t (id bigint);")},
		{Path: "migrations/0001_x.down.sql", Status: domain.StatusAdded, Current: []byte("DROP TABLE t;")},
		{Path: "migrations/0002/up.sql", Status: domain.StatusAdded, Current: []byte("CREATE TABLE u (id bigint);")},
		{Path: "migrations/0002/down.sql", Status: domain.StatusAdded, Current: []byte("DROP TABLE u;")},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	for _, f := range got {
		if strings.Contains(f.File, "down") {
			t.Errorf("classified a down migration: %s at %s", f.RuleID, f.File)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d findings, want 2 (one per up migration)", len(got))
	}
}

// An empty changeset is not a safe changeset by accident — it simply has nothing to say. The
// analyzer must return no findings and no error, and let the scorer decide what that means.
func TestAnalyzeEmptyChangeset(t *testing.T) {
	t.Parallel()

	got, err := postgres.New().Analyze(context.Background(), []domain.ChangedFile{
		{Path: "README.md", Status: domain.StatusModified, Current: []byte("# hello")},
		{Path: "k8s/deployment.yaml", Status: domain.StatusModified, Current: []byte("kind: Deployment")},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d findings for a changeset with no SQL, want 0", len(got))
	}
}

// A removed migration file describes a change that is being taken out of the changeset. Its
// statements are not going to run, so classifying them would report risk that does not exist.
func TestAnalyzeIgnoresRemovedFiles(t *testing.T) {
	t.Parallel()

	got, err := postgres.New().Analyze(context.Background(), []domain.ChangedFile{{
		Path:     "migrations/0001_x.up.sql",
		Status:   domain.StatusRemoved,
		Previous: []byte("DROP TABLE users;"),
	}})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d findings for a removed file, want 0", len(got))
	}
}

// Findings must come back in canonical order regardless of the order files arrive in, because
// the certificate has to be byte-identical across runs.
func TestAnalyzeIsDeterministic(t *testing.T) {
	t.Parallel()

	files := []domain.ChangedFile{
		{Path: "migrations/0002_b.up.sql", Status: domain.StatusAdded, Current: []byte("DROP TABLE b;\nTRUNCATE c;")},
		{Path: "migrations/0001_a.up.sql", Status: domain.StatusAdded, Current: []byte("CREATE TABLE a (id bigint);")},
	}

	subject := postgres.New()

	first, err := subject.Analyze(context.Background(), files)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	for i := 0; i < 20; i++ {
		got, err := subject.Analyze(context.Background(), files)
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if diff := cmp.Diff(first, got); diff != "" {
			t.Fatalf("run %d differed from the first (-first +got):\n%s", i, diff)
		}
	}
}

// The three levels must degrade independently. These are the paths the fixtures do not reach.
func TestValidateDownMigrationsEdgeCases(t *testing.T) {
	t.Parallel()

	sqlParser := parser.NewPgQuery()

	tests := []struct {
		name  string
		files []domain.ChangedFile
		want  domain.DownMigrationStatus
	}{
		{
			name: "empty down file fails level 2",
			files: []domain.ChangedFile{
				{Path: "migrations/0001_x.up.sql", Current: []byte("CREATE TABLE t (id bigint);")},
				{Path: "migrations/0001_x.down.sql", Current: []byte("   \n\t\n")},
			},
			want: domain.DownMigrationStatus{Migration: "0001_x", Exists: true, Parses: false, Symmetric: false},
		},
		{
			name: "unparseable down file fails level 2",
			files: []domain.ChangedFile{
				{Path: "migrations/0001_x.up.sql", Current: []byte("CREATE TABLE t (id bigint);")},
				{Path: "migrations/0001_x.down.sql", Current: []byte("DROP TABLEE t;")},
			},
			want: domain.DownMigrationStatus{Migration: "0001_x", Exists: true, Parses: false, Symmetric: false},
		},
		{
			name: "unparseable up file makes symmetry uncheckable",
			files: []domain.ChangedFile{
				{Path: "migrations/0001_x.up.sql", Current: []byte("CREATE TABLEE t (id bigint);")},
				{Path: "migrations/0001_x.down.sql", Current: []byte("DROP TABLE t;")},
			},
			want: domain.DownMigrationStatus{Migration: "0001_x", Exists: true, Parses: true, Symmetric: false},
		},
		{
			name: "symmetric pair passes all three levels",
			files: []domain.ChangedFile{
				{Path: "migrations/0001_x.up.sql", Current: []byte("CREATE TABLE t (id bigint);")},
				{Path: "migrations/0001_x.down.sql", Current: []byte("DROP TABLE t;")},
			},
			want: domain.DownMigrationStatus{Migration: "0001_x", Exists: true, Parses: true, Symmetric: true},
		},
		{
			name: "a drop in up must be recreated in down",
			files: []domain.ChangedFile{
				{Path: "migrations/0001_x.up.sql", Current: []byte("DROP TABLE t;")},
				{Path: "migrations/0001_x.down.sql", Current: []byte("SELECT 1;")},
			},
			want: domain.DownMigrationStatus{Migration: "0001_x", Exists: true, Parses: true, Symmetric: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := postgres.ValidateDownMigrations(context.Background(), sqlParser, tt.files)
			if err != nil {
				t.Fatalf("ValidateDownMigrations: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d statuses, want 1", len(got))
			}

			g := got[0]
			if g.Migration != tt.want.Migration || g.Exists != tt.want.Exists ||
				g.Parses != tt.want.Parses || g.Symmetric != tt.want.Symmetric {
				t.Errorf("got {migration:%s exists:%v parses:%v symmetric:%v}, want {%s %v %v %v}",
					g.Migration, g.Exists, g.Parses, g.Symmetric,
					tt.want.Migration, tt.want.Exists, tt.want.Parses, tt.want.Symmetric)
			}
			if !g.Symmetric && len(g.SymmetryNotes) == 0 {
				t.Error("a failed level recorded no explanatory note")
			}
		})
	}
}

// A down migration with no up migration is not this changeset's responsibility.
func TestValidateDownMigrationsIgnoresOrphanedDown(t *testing.T) {
	t.Parallel()

	got, err := postgres.ValidateDownMigrations(context.Background(), parser.NewPgQuery(),
		[]domain.ChangedFile{{Path: "migrations/0001_x.down.sql", Current: []byte("DROP TABLE t;")}})
	if err != nil {
		t.Fatalf("ValidateDownMigrations: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d statuses for an orphaned down migration, want 0", len(got))
	}
}

func TestValidateDownMigrationsRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := postgres.ValidateDownMigrations(ctx, parser.NewPgQuery(), nil); err == nil {
		t.Fatal("ValidateDownMigrations with a cancelled context returned nil error")
	}
}

// Symmetry notes reach the certificate, so they must be stable across runs.
func TestSymmetryNotesAreDeterministic(t *testing.T) {
	t.Parallel()

	files := []domain.ChangedFile{
		{Path: "migrations/0001_x.up.sql", Current: []byte("CREATE TABLE a (id bigint);\nCREATE TABLE b (id bigint);\nCREATE INDEX i ON a (id);")},
		{Path: "migrations/0001_x.down.sql", Current: []byte("SELECT 1;")},
	}

	first, err := postgres.ValidateDownMigrations(context.Background(), parser.NewPgQuery(), files)
	if err != nil {
		t.Fatalf("ValidateDownMigrations: %v", err)
	}

	for i := 0; i < 20; i++ {
		got, err := postgres.ValidateDownMigrations(context.Background(), parser.NewPgQuery(), files)
		if err != nil {
			t.Fatalf("ValidateDownMigrations: %v", err)
		}
		if diff := cmp.Diff(first, got); diff != "" {
			t.Fatalf("run %d differed (-first +got):\n%s", i, diff)
		}
	}
}
