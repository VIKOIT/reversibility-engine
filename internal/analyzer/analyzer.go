// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package analyzer

import (
	"context"
	"strings"
	"unicode"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
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
	//
	// It takes a domain.Located rather than a string because claiming a file is a path-keyed
	// decision, and every one of those is made in one namespace — see domain.Located. Most
	// implementations look only at the extension and would be right either way; the Terraform
	// analyzer is not, because `--terraform-plan` names a file and a name has to be compared
	// against something. It compared against the changeset's spelling and papered over the
	// mismatch with suffix matching, which over-claimed. The type is what stops the next
	// implementation from having to notice.
	Supports(at domain.Located) bool

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
	// docs/RULES.md §1 passed.
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

// CatalogVersioner is an optional capability an Analyzer may implement when its verdicts depend
// on a data table shipped with the build.
//
// The Terraform analyzer classifies against an embedded catalog, so the same plan can grade
// differently under two builds. Without recording which catalog produced a verdict there would
// be no way to tell why — so the engine mixes the digest into the certificate's input digest and
// puts the version on the certificate.
//
// It is an optional interface for the same reason DownMigrationValidator is: an analyzer with no
// data table should not have to answer a question that does not apply to it.
type CatalogVersioner interface {
	// CatalogVersion is the human-readable version, such as "2026.08.1".
	CatalogVersion() string

	// CatalogDigest is the SHA-256 over the catalog's classifications.
	CatalogDigest() string
}
