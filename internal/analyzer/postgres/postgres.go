// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Name is the stable identifier for this analyzer.
const Name = "postgres"

// Analyzer classifies PostgreSQL migration statements against rules PG001-PG027.
//
// It holds no mutable state: the schema tracked across a changeset lives for the duration of a
// single Analyze call, so one Analyzer is safe to share across goroutines.
type Analyzer struct {
	parser parser.SQLParser
}

// New returns a Postgres analyzer backed by the real PostgreSQL grammar.
func New() *Analyzer { return NewWithParser(parser.NewPgQuery()) }

// NewWithParser returns a Postgres analyzer using the supplied parser.
//
// This is the seam from ADR/0001: it lets the classification rules be exercised without cgo,
// and lets a future pure-Go parser be swapped in without touching this file.
func NewWithParser(p parser.SQLParser) *Analyzer { return &Analyzer{parser: p} }

// Name implements analyzer.Analyzer.
func (a *Analyzer) Name() string { return Name }

// Supports claims .sql files.
func (a *Analyzer) Supports(path string) bool {
	return strings.EqualFold(extension(path), ".sql")
}

// ValidateDownMigrations implements analyzer.DownMigrationValidator, so the orchestrator can
// collect down-migration status without knowing which analyzer produces it.
func (a *Analyzer) ValidateDownMigrations(ctx context.Context, files []domain.ChangedFile) ([]domain.DownMigrationStatus, error) {
	return ValidateDownMigrations(ctx, a.parser, files)
}

// Analyze classifies every up migration in the changeset.
//
// Down migrations are not classified: they describe the rollback, not the change being
// assessed. They are read by ValidateDownMigrations instead.
//
// A file that fails to parse yields a single PG027/UNKNOWN finding for that file rather than an
// error, because one malformed migration must not erase the findings of the others — the
// certificate should show everything that is wrong at once.
func (a *Analyzer) Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error) {
	if a.parser == nil {
		return nil, fmt.Errorf("%s analyzer: %w", Name, domain.ErrParserUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s analyzer: %w", Name, err)
	}

	// One tracker for the whole changeset: PG006 and PG007 depend on a CREATE TABLE that may
	// live in an earlier migration file.
	sch := newSchema()

	// Migrations apply in path order, and the tracker is order-dependent by design, so the
	// order is established here rather than assumed of the caller.
	//
	// Every provider already sorts, but relying on that made the verdict depend on the order a
	// caller happened to pass files in: given the same ALTER twice, whichever arrived first was
	// told its prior type was undeclared and the second was told the conversion was uncovered.
	// Same grade, different rationale, different certificate bytes. Fuzzing found it; sorting
	// here means no future provider can reintroduce it.
	ordered := make([]domain.ChangedFile, len(files))
	copy(ordered, files)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })

	var findings []domain.Finding

	for _, f := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s analyzer: %w", Name, err)
		}

		if !a.Supports(f.Path) || f.IsRemoved() {
			continue
		}
		if _, isDown, isMigration := classifyPath(f.Path); !isMigration || isDown {
			continue
		}

		fileFindings, err := a.analyzeFile(ctx, f, sch)
		if err != nil {
			return nil, err
		}
		findings = append(findings, fileFindings...)
	}

	domain.SortFindings(findings)
	return findings, nil
}

// analyzeFile classifies one up migration.
func (a *Analyzer) analyzeFile(ctx context.Context, f domain.ChangedFile, sch *schema) ([]domain.Finding, error) {
	sql := string(f.Current)

	stmts, err := a.parser.Parse(ctx, sql)
	if err != nil {
		// Fail closed. The file is reported as UNKNOWN, which grades F; it is never skipped
		// and never assumed harmless.
		return []domain.Finding{{
			RuleID:        "PG027",
			File:          f.Path,
			Line:          parseErrorLine(sql, err),
			Statement:     analyzer.NormalizeStatement(firstNonEmptyLine(sql)),
			Reversibility: domain.ReversibilityUnknown,
			LockHazard:    domain.LockExclusive,
			Rationale:     fmt.Sprintf("This migration could not be parsed, so nothing in it can be classified: %v.", err),
		}}, nil
	}

	out := make([]domain.Finding, 0, len(stmts))
	for _, s := range stmts {
		c := classify(s, sch)

		// Applied after classification so that an ALTER COLUMN TYPE is judged against the type
		// it replaces rather than the one it installs.
		sch.apply(s)

		out = append(out, domain.Finding{
			RuleID:        c.ruleID,
			File:          f.Path,
			Line:          s.Line,
			Statement:     analyzer.NormalizeStatement(s.SQL),
			Reversibility: c.reversibility,
			LockHazard:    c.lock,
			Rationale:     c.rationale,
			UndoStep:      c.undo,
		})
	}
	return out, nil
}

// parseErrorLine recovers the line a syntax error points at.
//
// pg_query reports a byte cursor position inside its error text. Rather than depend on the
// parser's internal error type, the position is recovered from the message when present, and
// otherwise the finding points at the first line — a wrong line number must never be a reason
// to drop the finding.
func parseErrorLine(sql string, err error) int {
	_ = err
	// The parser does not expose a stable cursor position through the SQLParser seam, so the
	// finding is attributed to the first line of the file. Improving this means widening the
	// seam, which is not worth trading the isolation for.
	for i, line := range strings.Split(sql, "\n") {
		if strings.TrimSpace(line) != "" {
			return i + 1
		}
	}
	return 1
}

func firstNonEmptyLine(sql string) string {
	for _, line := range strings.Split(sql, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// extension returns the final dot-suffix of a slash-separated path, or "" if there is none.
func extension(path string) string {
	slash := strings.LastIndex(path, "/")
	dot := strings.LastIndex(path, ".")
	if dot < 0 || dot < slash {
		return ""
	}
	return path[dot:]
}
