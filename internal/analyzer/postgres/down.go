// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres/parser"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// ValidateDownMigrations checks, for every migration in the changeset, whether a down migration
// exists, parses, and reverses what the up migration did.
//
// It is a separate exported function rather than part of Analyze because the Analyzer interface
// returns only findings, and down-migration status is not a classification — it is an input to
// the scorer's cap rules. Keeping it stateless means the analyzer holds no per-run state and
// stays safe to share across goroutines. Resolves CLAUDE.md §16.1.
//
// The three levels are reported independently, and level 3 is advisory: per docs/RULES.md §1 it
// must never on its own produce grade F.
func ValidateDownMigrations(ctx context.Context, p parser.SQLParser, files []domain.ChangedFile) ([]domain.DownMigrationStatus, error) {
	if p == nil {
		return nil, fmt.Errorf("%s: validating down migrations: %w", Name, domain.ErrParserUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: validating down migrations: %w", Name, err)
	}

	pairs := pairMigrations(files)

	out := make([]domain.DownMigrationStatus, 0, len(pairs))
	for _, pair := range pairs {
		// A down migration with no corresponding up migration is not a migration this
		// changeset is responsible for.
		if pair.up == nil {
			continue
		}

		status := domain.DownMigrationStatus{
			Migration: pair.id,
			UpFile:    pair.up.Path,
		}

		if pair.down == nil {
			status.SymmetryNotes = []string{"no down migration accompanies this up migration"}
			out = append(out, status)
			continue
		}

		status.DownFile = pair.down.Path
		status.Exists = true

		downSQL := strings.TrimSpace(string(pair.down.Current))
		if downSQL == "" {
			status.SymmetryNotes = []string{"the down migration is empty"}
			out = append(out, status)
			continue
		}

		downStmts, err := p.Parse(ctx, downSQL)
		if err != nil {
			// A down migration that exists but does not parse is worse than none, because it
			// looks like safety. Level 2 fails; it is not an analyzer error.
			status.SymmetryNotes = []string{fmt.Sprintf("the down migration does not parse: %v", err)}
			out = append(out, status)
			continue
		}
		status.Parses = true

		upStmts, err := p.Parse(ctx, string(pair.up.Current))
		if err != nil {
			// The up migration is unparseable, which PG027 already reports as UNKNOWN.
			// Symmetry simply cannot be evaluated, so it fails closed.
			status.SymmetryNotes = []string{"symmetry could not be checked because the up migration does not parse"}
			out = append(out, status)
			continue
		}

		status.Symmetric, status.SymmetryNotes = checkSymmetry(upStmts, downStmts)
		out = append(out, status)
	}

	return out, nil
}

// checkSymmetry is the level 3 heuristic: every object the up migration creates should be
// dropped by the down migration, and every object it drops should be recreated.
//
// It is a heuristic and is labelled as one. A migration can be perfectly reversible without
// textual symmetry — a data backfill has nothing to create or drop — which is exactly why
// docs/RULES.md §1 forbids this check from producing grade F on its own.
func checkSymmetry(up, down []parser.Statement) (bool, []string) {
	downDrops := objectSet(down, parser.Statement.Drops)
	downCreates := objectSet(down, parser.Statement.Creates)

	var notes []string

	for _, s := range up {
		if obj, ok := s.Creates(); ok {
			if _, reversed := downDrops[obj]; !reversed {
				notes = append(notes, fmt.Sprintf("up creates %s but down never drops it", obj))
			}
		}
		if obj, ok := s.Drops(); ok {
			if _, reversed := downCreates[obj]; !reversed {
				notes = append(notes, fmt.Sprintf("up drops %s but down never recreates it", obj))
			}
		}
	}

	if len(notes) == 0 {
		return true, nil
	}

	// Sorted and de-duplicated: these strings reach the certificate, and the certificate must
	// be byte-identical across runs.
	sort.Strings(notes)
	return false, dedupe(notes)
}

// objectSet collects the objects a set of statements creates or drops, selected by the passed
// accessor.
func objectSet(stmts []parser.Statement, get func(parser.Statement) (parser.ObjectRef, bool)) map[parser.ObjectRef]struct{} {
	out := map[parser.ObjectRef]struct{}{}
	for _, s := range stmts {
		if obj, ok := get(s); ok {
			out[obj] = struct{}{}
		}
	}
	return out
}

func dedupe(in []string) []string {
	out := in[:0:0]
	var prev string
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}
