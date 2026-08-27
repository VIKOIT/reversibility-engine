// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// This file answers one question, and it is the first question a run has to answer: was there
// anything here that the engine ought to have been able to read?
//
// It exists because "no analyzer claimed a file" used to have exactly one meaning — nothing to
// do — and that meaning was applied to a pull request of thirteen Django migrations. Splitting
// it in two is the whole of the P0 fix. See docs/SPECIFICATION.md §2 and docs/RULES.md §3.

// migrationDirs are the path segments that mark a directory as holding migrations.
//
// Drawn from the two conventions the ecosystem actually uses: Django writes
// <app>/migrations/0001_initial.py, Rails writes db/migrate/20240101_add_orders.rb. Neither
// name is a guess, and the list is short on purpose — see candidateExtensions.
var migrationDirs = map[string]bool{
	"migrations": true,
	"migration":  true,
	"migrate":    true,
}

// candidateExtensions are the languages migrations are written in, for the directory-scoped
// half of the predicate.
//
// Note what is absent: .go, .md, .txt, .json, .toml. A Go source file under migrations/ is a
// helper, not a migration, and docs/RULES.md §3 names Go source explicitly as NO_CANDIDATES.
// The cost of a false positive here is an exit 2 on a pull request that deserved a clean 0, so
// the list stays exactly as wide as the evidence.
var candidateExtensions = map[string]bool{
	".py": true,
	".rb": true,
	".js": true,
	".ts": true,
}

// Candidate reports whether path plausibly holds a migration the engine ought to have read.
//
// It is asked only about files no analyzer claimed, so a supported extension never reaches it.
// Two shapes qualify, per docs/RULES.md §3:
//
//   - a .py, .rb, .js, or .ts file under a path segment named migrations, migration, or
//     migrate — the directory name alone is not the signal, and neither is the extension
//     alone; it is the two together;
//   - any .sql file, wherever it sits. A .sql the Postgres analyzer claimed is ANALYZED and
//     never arrives here, so this fires only when SQL was present and nothing was going to
//     read it — which is precisely the case that must not pass.
func Candidate(p string) bool {
	ok, _ := classifyCandidate(p)
	return ok
}

// classifyCandidate is Candidate plus the reason, which the certificate records per file.
//
// The reason describes the engine's limitation and never the file's quality. "No analyzer reads
// Django-style .py migrations" is a fact about this tool; anything that reads as a complaint
// about the changeset would be the engine inventing severity from its own ignorance, which is
// the thing the two-axis certificate exists to prevent.
func classifyCandidate(p string) (bool, string) {
	clean := strings.ToLower(path.Clean(strings.ReplaceAll(p, "\\", "/")))
	ext := path.Ext(clean)

	if strings.HasSuffix(clean, ".sql") {
		return true, "no analyzer claimed this .sql file"
	}

	if !candidateExtensions[ext] {
		return false, ""
	}

	// The file's own name is not consulted as a directory, so a file literally called
	// "migrate.py" at the repository root is not a candidate. That is deliberate: it is far
	// more often a management script than a migration.
	for _, segment := range strings.Split(path.Dir(clean), "/") {
		if migrationDirs[segment] {
			return true, "no analyzer reads " + ext + " migrations"
		}
	}

	return false, ""
}

// unanalyzedFiles turns the candidate paths into the per-file record the certificate carries.
//
// Paths arrive sorted from outcome, so this is deterministic without sorting again.
func unanalyzedFiles(paths []string) []domain.UnanalyzedFile {
	out := make([]domain.UnanalyzedFile, 0, len(paths))
	for _, p := range paths {
		_, reason := classifyCandidate(p)
		out = append(out, domain.UnanalyzedFile{Path: p, Reason: reason})
	}
	return out
}

// outcome decides what the run was able to do at all, before any question of grading.
//
// The three answers are docs/RULES.md §3's. unsupported holds the candidate paths, sorted, and
// is returned even when the outcome is ANALYZED — a partially covered changeset is an open
// question (docs/SPECIFICATION.md §16.7) and the input that ruling will need is computed here.
func (e *Engine) outcome(files []domain.ChangedFile) (domain.AnalysisOutcome, []string) {
	var (
		claimed     bool
		unsupported []string
	)

	for _, f := range files {
		switch {
		case e.Supports(f.Path):
			claimed = true
		case Candidate(f.Path):
			unsupported = append(unsupported, f.Path)
		}
	}

	sort.Strings(unsupported)

	switch {
	case claimed:
		return domain.OutcomeAnalyzed, unsupported
	case len(unsupported) > 0:
		return domain.OutcomeUnsupportedContent, unsupported
	default:
		return domain.OutcomeNoCandidates, nil
	}
}

// unsupportedContentBlockers turns the candidate paths into the message the certificate carries.
//
// The requirement is that it names what it saw and what it could not do — a bare "not
// applicable" is what let the Django case through, because it read as "nothing here" rather
// than as "thirteen files I cannot parse". One line per directory, so a changeset touching
// several apps says so instead of collapsing into a single count.
func unsupportedContentBlockers(paths []string) []string {
	type group struct {
		count int
		exts  map[string]bool
	}

	groups := map[string]*group{}
	var order []string

	for _, p := range paths {
		clean := strings.ReplaceAll(p, "\\", "/")

		dir := path.Dir(clean)
		if dir == "." || dir == "/" {
			dir = "the changeset root"
		}

		g, ok := groups[dir]
		if !ok {
			g = &group{exts: map[string]bool{}}
			groups[dir] = g
			order = append(order, dir)
		}

		g.count++
		if ext := strings.ToLower(path.Ext(clean)); ext != "" {
			g.exts[ext] = true
		}
	}

	// Sorted by directory rather than by first appearance: the certificate must be
	// byte-identical across runs, and map iteration is not.
	sort.Strings(order)

	blockers := make([]string, 0, len(order))
	for _, dir := range order {
		g := groups[dir]

		exts := make([]string, 0, len(g.exts))
		for ext := range g.exts {
			exts = append(exts, ext)
		}
		sort.Strings(exts)

		blockers = append(blockers, "found "+plural(g.count, "file")+" in "+dir+
			" that no analyzer supports ("+strings.Join(exts, ", ")+
			" migrations). Reversibility was not assessed.")
	}

	return blockers
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
