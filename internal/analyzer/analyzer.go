package analyzer

import (
	"context"
	"strings"
	"unicode"

	"github.com/abdo-s1/reversibility-engine/internal/domain"
)

// Analyzer classifies the changes in a changeset against one authoritative rule table.
//
// Implementations are pure: given the same files they return the same findings, and they touch
// no network, disk, git, or GitHub. That is what makes every rule in the tables testable from a
// fixture directory alone.
type Analyzer interface {
	// Name is a stable identifier used in logs and certificates.
	Name() string

	// Supports reports whether this analyzer claims the given path. A file no analyzer claims
	// is not an error; it is simply outside the engine's scope.
	Supports(path string) bool

	// Analyze classifies the supported files in the changeset.
	//
	// It returns an error only when analysis could not be completed. It must never return a
	// nil error alongside an incomplete result, because the scorer reads an absence of
	// findings as an absence of risk.
	Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error)
}

// DownMigrationValidator is an optional capability an Analyzer may also implement.
//
// Down-migration status is not a classification, so it cannot travel through Analyze — but the
// scorer needs it, because a missing or unparseable down migration caps the grade at C. An
// optional interface keeps that out of the Analyzer contract, so the orchestrator does not have
// to know which analyzer happens to care about migrations.
type DownMigrationValidator interface {
	// ValidateDownMigrations reports, per migration, which of the three validation levels in
	// CLAUDE.md §9 passed.
	ValidateDownMigrations(ctx context.Context, files []domain.ChangedFile) ([]domain.DownMigrationStatus, error)
}

// NormalizeStatement collapses a statement onto one line and bounds its length, producing the
// value stored in Finding.Statement.
//
// Normalization exists so that two statements differing only in formatting produce identical
// certificates — determinism is a product requirement, and a reformatted migration must not
// change the output digest.
func NormalizeStatement(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	// Collapse every run of whitespace, including newlines and tabs, to a single space.
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteRune(' ')
		}
		space = false
		b.WriteRune(r)
	}

	out := b.String()

	// Count runes, not bytes: cutting a multi-byte identifier in half would emit invalid UTF-8
	// into a JSON certificate.
	runes := []rune(out)
	if len(runes) <= domain.MaxStatementLength {
		return out
	}

	// The marker is inside the budget rather than appended to it, so the field never exceeds
	// its documented bound, and a reader can tell that something was cut.
	const marker = "..."
	return string(runes[:domain.MaxStatementLength-len(marker)]) + marker
}
