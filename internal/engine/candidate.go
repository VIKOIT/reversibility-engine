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
		// Not migration-shaped. It still counts against coverage when it sits inside a
		// migration directory — see Engine.outcome — and the reason has to say which of those
		// two things happened, because they call for different responses: teach the engine a
		// format, or move the file out and ignore it.
		if InMigrationDir(p) {
			return false, "not analyzed, and it sits in a migration directory"
		}
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
//
// The reason is derived from the qualified path and the path is reported unqualified. That is
// the split the whole of §16.10 rests on: the question "what kind of file is this" is about
// where the file sits, and the answer a reader acts on has to name the file the way they named
// it themselves.
func unanalyzedFiles(paths []string, qualify func(string) string) []domain.UnanalyzedFile {
	out := make([]domain.UnanalyzedFile, 0, len(paths))
	for _, p := range paths {
		_, reason := classifyCandidate(qualify(p))
		if reason == "" {
			reason = "not analyzed, and it sits in a migration directory"
		}
		out = append(out, domain.UnanalyzedFile{Path: p, Reason: reason})
	}
	return out
}

// PartialCoverageBlocker is the message a partially covered changeset fails with.
//
// It is exported so the renderers and the CLI say the same sentence the certificate does. A
// safety tool that phrases the same refusal three ways teaches the reader that the wording does
// not matter, and then the wording is the thing nobody reads.
const PartialCoverageBlocker = "Cannot guarantee reversibility. Unanalyzed files found in " +
	"migration directories. Remove them or explicitly ignore them in the config."

// RenderToSQL is the way forward out of both refusals, and it is a supported path rather than a
// workaround.
//
// **The verdict behind it is not being softened.** This engine assesses SQL, rendered Kubernetes
// manifests, and Terraform plans. It will never parse a Django `.py` or a Rails `.rb` migration,
// so on an ORM-native repository 85% of the corpus is un-gradeable and every graded run is
// blocked at `--gate`. That is the honest answer and it stands.
//
// What does not stand is leaving the path out of the message. A team told only that their
// changeset cannot be certified, with no route to a passing grade, uninstalls the gate — and a
// gate nobody runs protects nothing, which is a worse outcome than the one the strictness was
// bought to prevent. Every ORM worth naming can emit the SQL it is about to run. Rendering it is
// the supported workflow, and the refusal is where a reader will actually be standing when they
// need to know that.
const RenderToSQL = "Render these migrations to SQL and point the engine at the output."

// renderingRemedies names the concrete command for each format the engine cannot parse.
//
// Keyed by extension, because the extension is all that is known at this point. A `.py`
// migration is Django's or Alembic's and both are named rather than guessed between: two
// answers cost a reader one line, and the wrong single answer costs them the whole path.
//
// `.js` and `.ts` have no one convention worth printing — node-pg-migrate, Knex, TypeORM and
// Prisma each spell it differently — so they get the general sentence and nothing invented. A
// command that does not exist is worse than no command.
var renderingRemedies = map[string]string{
	".py": "Django: `python manage.py sqlmigrate <app> <name> > rendered/<name>.sql`. " +
		"Alembic: `alembic upgrade <rev> --sql > rendered/<rev>.sql`.",
	".rb": "Rails: set `config.active_record.schema_format = :sql`, run `bin/rails db:migrate`, " +
		"and point the engine at `db/structure.sql`.",
}

// RenderingRemedy returns the way-forward lines for a set of files the engine could not read.
//
// One general sentence, then one line per format that has a name for the command. Sorted and
// deduplicated, because these end up in Blockers and a certificate must be byte-identical for
// identical input.
//
// It returns nothing when nothing in the list is a migration this advice applies to. A `.md`
// beside the migrations fails coverage too, and telling its author to render it to SQL would be
// advice that does not fit the problem — which is how a reader learns to stop reading the
// messages.
func RenderingRemedy(paths []string) []string {
	seen := map[string]bool{}
	for _, p := range paths {
		ext := strings.ToLower(path.Ext(strings.ReplaceAll(p, "\\", "/")))
		if candidateExtensions[ext] {
			seen[ext] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}

	exts := make([]string, 0, len(seen))
	for ext := range seen {
		exts = append(exts, ext)
	}
	sort.Strings(exts)

	out := []string{RenderToSQL}
	for _, ext := range exts {
		if remedy, ok := renderingRemedies[ext]; ok {
			out = append(out, remedy)
		}
	}
	return out
}

// InMigrationDir reports whether a path sits under a directory named for migrations.
//
// This is the half of "is this a migration directory" that can be answered from one path, which
// is what a provider's include predicate has to work with: it decides whether to read a file
// before it has seen the rest of the changeset.
func InMigrationDir(p string) bool {
	clean := strings.ToLower(path.Clean(strings.ReplaceAll(p, "\\", "/")))
	for _, segment := range strings.Split(path.Dir(clean), "/") {
		if migrationDirs[segment] {
			return true
		}
	}
	return false
}

// migrationDirectories identifies every directory this changeset treats as holding migrations.
//
// Two ways in, and the second is why this cannot be answered per path:
//
//   - the directory is named for migrations — migrations/, migration/, db/migrate/;
//   - the directory holds at least one file an analyzer claimed, whatever it is called. A
//     directory of .sql files named db/schema/ is a migration directory by any honest reading,
//     and requiring the conventional name would let a rename defeat the coverage check.
//
// It works in the qualified namespace, because both clauses are questions about location and a
// changeset path has had its root's name stripped off the front — see runConfig.qualify. The map
// is therefore keyed by qualified directory, and every lookup into it must qualify too.
func (e *Engine) migrationDirectories(enumerated []string, qualify func(string) string) map[string]bool {
	dirs := map[string]bool{}

	for _, p := range enumerated {
		q := qualify(p)
		if e.Supports(p) || InMigrationDir(q) {
			dirs[path.Dir(q)] = true
		}
	}
	return dirs
}

// outcome decides what the run was able to do at all, before any question of grading, and
// reports every file inside a migration directory that no analyzer read.
//
// **The denominator is every file in those directories, not every file an analyzer might have
// wanted.** That is the whole of the strict-coverage ruling: a `.md`, a `.gitkeep`, a helper
// script sitting beside the migrations all count against coverage, because the engine cannot
// tell from the outside which of them is inert. A file it did not read is a file it cannot
// vouch for, and a changeset it can only partly vouch for is one it fails.
//
// The escape hatch is the policy, not a heuristic: ignore the file explicitly and the decision
// is recorded, digested, and visible on the certificate.
// It works from the **enumeration** rather than from the files, which is the §16.9 fix: a file
// that exists and was never read is exactly what this has to report, and it cannot appear in a
// list of files that were read.
//
// Every question it asks about *location* is asked of the qualified path, and every path it
// *returns* is the one the caller gave it. A changeset path is relative to whatever the provider
// was pointed at, so `revctl check ./migrations` hands this function `0001_initial.py` — and
// classifying that is how the Django case reached NO_CANDIDATES and exit 0. See §16.10.
func (e *Engine) outcome(
	files []domain.ChangedFile,
	enumerated []string,
	qualify func(string) string,
) (domain.AnalysisOutcome, []string) {
	read := make(map[string]bool, len(files))
	claimed := false

	for _, f := range files {
		read[f.Path] = true
		if e.Supports(f.Path) {
			claimed = true
		}
	}

	dirs := e.migrationDirectories(enumerated, qualify)

	var unsupported []string
	for _, p := range enumerated {
		if read[p] && e.Supports(p) {
			continue
		}

		// Outside every migration directory, only a migration-shaped file counts: a .py at the
		// repository root is a script, and holding a changeset hostage to it would be the
		// severity-from-ignorance failure this engine is not allowed to commit.
		q := qualify(p)
		if dirs[path.Dir(q)] || Candidate(q) {
			unsupported = append(unsupported, p)
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

func normalizePath(p string) string {
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}

// unsupportedContentBlockers turns the candidate paths into the message the certificate carries.
//
// The requirement is that it names what it saw and what it could not do — a bare "not
// applicable" is what let the Django case through, because it read as "nothing here" rather
// than as "thirteen files I cannot parse". One line per directory, so a changeset touching
// several apps says so instead of collapsing into a single count.
//
// **It names the directory as the caller named it, never the qualified path**, and that is not a
// missed opportunity to be more specific. A blocker is on the certificate, the certificate must
// be byte-identical for identical input, and a qualified path outside a checkout is an absolute
// one — so naming it here would make the certificate depend on which machine produced it and
// where the tree happened to be unpacked. That is the same class of input as a timestamp or a
// hostname, and the determinism rule excludes all three. §16.10 qualifies paths to classify
// them and for nothing else.
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

	// Then, once, the way forward. It is appended rather than repeated per directory: a team
	// with migrations in six apps needs the command once, not six times.
	return append(blockers, RenderingRemedy(paths)...)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
