// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package postgres

import (
	"path"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Two migration layouts are recognised, per CLAUDE.md §9.
const (
	upSuffix   = ".up.sql"
	downSuffix = ".down.sql"
	upBase     = "up.sql"
	downBase   = "down.sql"
)

// migrationPair is one migration and the down migration that is supposed to reverse it.
type migrationPair struct {
	// id is the identity shared by both halves: "0042_add_orders" for the flat layout,
	// "0042" for the directory layout.
	id string

	up   *domain.ChangedFile
	down *domain.ChangedFile
}

// classifyPath decides what role a .sql file plays.
//
// A file that is not recognisably a down migration is treated as an up migration and gets
// classified. That is the safe direction: analysing something the rules do not need is
// harmless, whereas skipping a destructive statement because its filename was unfamiliar is not.
func classifyPath(p string) (id string, isDown bool, isMigration bool) {
	base := path.Base(p)
	lower := strings.ToLower(base)

	switch {
	case strings.HasSuffix(lower, downSuffix):
		return strings.TrimSuffix(base, base[len(base)-len(downSuffix):]), true, true

	case strings.HasSuffix(lower, upSuffix):
		return strings.TrimSuffix(base, base[len(base)-len(upSuffix):]), false, true

	case lower == downBase:
		return path.Base(path.Dir(p)), true, true

	case lower == upBase:
		return path.Base(path.Dir(p)), false, true

	case strings.HasSuffix(lower, ".sql"):
		return strings.TrimSuffix(base, base[len(base)-len(".sql"):]), false, true

	default:
		return "", false, false
	}
}

// pairMigrations groups the .sql files of a changeset into up/down pairs.
//
// Pairs are keyed by directory as well as identity, so that migrations/0001/up.sql and
// legacy/0001/up.sql are not mistaken for one another.
func pairMigrations(files []domain.ChangedFile) []migrationPair {
	type key struct{ dir, id string }

	pairs := map[key]*migrationPair{}
	var order []key

	for i := range files {
		f := files[i]

		id, isDown, ok := classifyPath(f.Path)
		if !ok {
			continue
		}

		// The directory layout keys on the parent of the migration directory, so that up.sql
		// and down.sql inside migrations/0001/ resolve to the same pair.
		dir := path.Dir(f.Path)
		if base := strings.ToLower(path.Base(f.Path)); base == upBase || base == downBase {
			dir = path.Dir(dir)
		}

		k := key{dir: dir, id: id}
		p, seen := pairs[k]
		if !seen {
			p = &migrationPair{id: id}
			pairs[k] = p
			order = append(order, k)
		}

		if isDown {
			p.down = &files[i]
		} else {
			p.up = &files[i]
		}
	}

	// Sorted by directory then identity: migrations apply in that order, and the certificate
	// must not depend on map iteration.
	sort.Slice(order, func(i, j int) bool {
		if order[i].dir != order[j].dir {
			return order[i].dir < order[j].dir
		}
		return order[i].id < order[j].id
	})

	out := make([]migrationPair, 0, len(order))
	for _, k := range order {
		out = append(out, *pairs[k])
	}
	return out
}
