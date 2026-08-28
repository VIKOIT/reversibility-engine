// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/delivery/cli"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

// This file exists because the gate has now been bypassed twice, by two different routes, and
// both routes were reviewed before they shipped. The :v1 image ran revctl with no arguments and
// exited 0. The scoring rules turned a changeset of unreadable migrations into grade A and gate
// PASS. Neither was caught by a test, because every test in this package was written by someone
// who already knew what the command was supposed to do and checked that it did.
//
// So this is a property, not a list of cases. The property is:
//
//	Under a gate, revctl exits 0 only when an analyzer claimed a file — or when the certificate
//	explicitly reports that there was nothing to claim, in the one shape that cannot be mistaken
//	for approval: Grade N/A, AIGateStatus NOT_APPLICABLE, Outcome NO_CANDIDATES.
//
// and it is checked against an oracle that does not ask the engine anything. The engine's own
// opinion of what it claimed is exactly the thing under test.

// ---------------------------------------------------------------------------------------
// The oracle
// ---------------------------------------------------------------------------------------

// analyzedExtensions is the extension convention from docs/RULES.md, restated here on purpose.
//
// It is a second, independent copy. Deriving it from Engine.Supports would make this test agree
// with the engine by construction and prove nothing — the entire question is whether the engine
// and the world agree about what was read.
var analyzedExtensions = map[string]bool{
	".sql":  true,
	".yaml": true,
	".yml":  true,
}

// migrationShapedExtensions restates the candidate predicate from docs/RULES.md §3, again as a
// second independent copy rather than by calling engine.Candidate.
//
// The first version of this file had no such oracle, and a mutation that made engine.Candidate
// return false unconditionally passed all 588 cases: every Django tree simply became
// NO_CANDIDATES, which the exit-0 branch permits. The invariant survived and the product did
// not — a green check over thirteen unread migrations is the P0 regardless of which value the
// grade field holds.
var migrationShapedExtensions = map[string]bool{
	".py": true,
	".rb": true,
	".js": true,
	".ts": true,
}

var migrationDirNames = map[string]bool{
	"migrations": true,
	"migration":  true,
	"migrate":    true,
}

// oracleWalk visits every file the run could have seen, without consulting the engine.
//
// A path that cannot be walked contributes nothing, and that is the honest answer: an unreadable
// directory is not evidence that it held migrations, and not evidence that it did not.
func oracleWalk(c combination, visit func(rel string, name string)) {
	// A policy that ignores everything means no file reaches an analyzer, whatever is on disk.
	if c.mod.ignoreAll {
		return
	}

	roots := append([]string{}, c.paths...)
	roots = append(roots, c.mod.beforePaths(c)...)

	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				// Swallowing the walk error is the point, not an oversight: an unreadable
				// directory yields no claim in either direction, which is what the oracle is
				// being asked. Propagating it would abort the walk and hide sibling files that
				// are perfectly readable.
				return nil //nolint:nilerr // an unwalkable path is not a claim
			}

			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				rel = p
			}

			visit(filepath.ToSlash(strings.ToLower(rel)), strings.ToLower(d.Name()))
			return nil
		})
	}
}

// oracleClaimsSomething reports whether the run should have had anything to analyze.
func oracleClaimsSomething(t *testing.T, c combination) bool {
	t.Helper()

	found := false
	oracleWalk(c, func(_ string, name string) {
		if analyzedExtensions[strings.ToLower(filepath.Ext(name))] || strings.HasSuffix(name, ".tfplan.json") {
			found = true
		}
	})
	return found
}

// oracleSeesMigrationShapedFiles reports whether the run should have found something that looks
// like a migration even though no analyzer can read it.
//
// This is what makes NO_CANDIDATES falsifiable. Without it, an engine that recognised nothing at
// all would report "there was nothing here" over any repository and exit 0 every time.
func oracleSeesMigrationShapedFiles(t *testing.T, c combination) bool {
	t.Helper()

	found := false
	oracleWalk(c, func(rel string, name string) {
		if !migrationShapedExtensions[strings.ToLower(filepath.Ext(name))] {
			return
		}
		if inMigrationDir(rel) {
			found = true
		}
	})
	return found
}

func inMigrationDir(rel string) bool {
	for _, segment := range strings.Split(path.Dir(rel), "/") {
		if migrationDirNames[segment] {
			return true
		}
	}
	return false
}

// oracleSeesUnanalyzedInMigrationDir restates the strict-coverage denominator independently.
//
// **Every file in a migration directory counts, not only the migration-shaped ones.** An earlier
// version of this file had no such oracle, and a mutation that reverted the denominator to
// "candidates only" passed all 672 cases — the properties all still held, and the check the
// ruling exists to enforce had quietly stopped enforcing anything.
//
// It checks the whole denominator. It did not always: until the two-phase FileProvider landed,
// a directory identified only by holding an analyzable file had its unconventional siblings
// counted only if they were migration-shaped, because the provider decided what to read from
// each path alone and never fetched them.
//
// That boundary is gone. List enumerates the changeset before anything is read, so
// "db/schema/notes.txt beside a .sql file" is now visible and now counts — which is what closed
// the rename bypass. The oracle asserts the rule rather than the old limitation.
func oracleSeesUnanalyzedInMigrationDir(t *testing.T, c combination) bool {
	t.Helper()

	analyzable := func(name string) bool {
		return analyzedExtensions[strings.ToLower(filepath.Ext(name))] ||
			strings.HasSuffix(strings.ToLower(name), ".tfplan.json")
	}

	// First pass: which directories are migration directories. Both clauses, because the
	// enumeration now reaches both.
	dirs := map[string]bool{}
	oracleWalk(c, func(rel string, name string) {
		if analyzable(name) || inMigrationDir(rel) {
			dirs[path.Dir(rel)] = true
		}
	})

	found := false
	oracleWalk(c, func(rel string, name string) {
		if analyzable(name) {
			return
		}

		// Everything in a migration-named directory counts, whatever it is.
		if inMigrationDir(rel) {
			found = true
			return
		}

		// Inside a directory that holds an analyzable file, everything counts too — the second
		// clause of "what is a migration directory", and the one a rename used to defeat.
		if dirs[path.Dir(rel)] {
			found = true
			return
		}

		// Elsewhere, only an unclaimed .sql counts. A .py or .rb outside a migration directory
		// is a script: the engine deliberately does not fail a changeset over one, because that
		// would be severity invented from ignorance, which is the opposite mistake.
		if strings.HasSuffix(strings.ToLower(name), ".sql") {
			found = true
		}
	})
	return found
}

// ---------------------------------------------------------------------------------------
// The input space
// ---------------------------------------------------------------------------------------

// tree is one shape of repository content.
type tree struct {
	name string

	// build returns the path arguments for the run. It may return a path that does not exist,
	// or no paths at all.
	build func(t *testing.T) []string
}

func trees() []tree {
	return []tree{
		{"empty directory", func(t *testing.T) []string {
			return []string{t.TempDir()}
		}},
		{"documentation only", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"README.md":     "# hello\n",
				"docs/guide.md": "words\n",
				"LICENSE":       "MIT\n",
			})}
		}},
		{"go source only", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"main.go":          "package main\n",
				"internal/x/x.go":  "package x\n",
				"migrations/mk.go": "package migrations\n",
			})}
		}},
		{"django migrations", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"django/contrib/auth/migrations/0001_initial.py":  "from django.db import migrations\n",
				"django/contrib/auth/migrations/0002_alter.py":    "from django.db import migrations\n",
				"django/contrib/auth/migrations/__init__.py":      "",
				"django/contrib/auth/models.py":                   "class User: pass\n",
				"django/contrib/sessions/migrations/0001_init.py": "from django.db import migrations\n",
			})}
		}},
		{"rails migrations", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"db/migrate/20240101_create_orders.rb": "class CreateOrders < ActiveRecord::Migration\nend\n",
				"db/migrate/20240102_add_index.rb":     "class AddIndex < ActiveRecord::Migration\nend\n",
				"app/models/order.rb":                  "class Order; end\n",
			})}
		}},
		{"node migrations", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"migrations/001_create.js": "exports.up = () => {};\n",
				"migrations/002_alter.ts":  "export const up = () => {};\n",
				"src/index.ts":             "export {};\n",
			})}
		}},
		{"scripts that are not migrations", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"migrate.py":    "print(1)\n",
				"tools/seed.rb": "puts 1\n",
				"build.js":      "module.exports = {};\n",
			})}
		}},
		{"a path that does not exist", func(t *testing.T) []string {
			return []string{filepath.Join(t.TempDir(), "no-such-directory")}
		}},
		{"a glob that expanded to nothing", func(_ *testing.T) []string {
			// A shell whose pattern matches nothing passes no path argument at all. That reaches
			// the CLI as an empty argument list, which it must reject rather than treat as an
			// empty and therefore clean changeset.
			return nil
		}},
		{"an unreadable directory", func(t *testing.T) []string {
			root := writeTree(t, map[string]string{"migrations/0001_up.sql": "DROP TABLE orders;\n"})
			locked := filepath.Join(root, "migrations")

			// Best effort: on Windows this only clears the read-only bit and the walk still
			// succeeds. The property has to hold either way, which is exactly why this asserts
			// a property rather than a specific exit code.
			_ = os.Chmod(locked, 0)
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

			return []string{root}
		}},
		{"a reversible sql migration", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
				"0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
			})}
		}},
		{"a destructive sql migration", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"0001_drop.up.sql":   "DROP TABLE legacy_orders;\n",
				"0001_drop.down.sql": "CREATE TABLE legacy_orders (id bigint);\n",
			})}
		}},
		{"sql beside unreadable migrations", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"db/migrate/0001_add.sql":        "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
				"db/migrate/0001_add.down.sql":   "DROP INDEX CONCURRENTLY idx;\n",
				"db/migrate/0002_backfill.rb":    "class Backfill < ActiveRecord::Migration\nend\n",
				"django/app/migrations/x0001.py": "from django.db import migrations\n",
			})}
		}},
		{"migrations beside a readme", func(t *testing.T) []string {
			// The strict-coverage denominator, as a tree. Neither the README nor the .gitkeep is
			// migration-shaped, and both sit in a migration directory, so both count.
			return []string{writeTree(t, map[string]string{
				"db/migrate/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
				"db/migrate/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
				"db/migrate/README.md":         "# how to write migrations\n",
				"db/migrate/.gitkeep":          "",
			})}
		}},
		{"migrations in an unconventionally named directory", func(t *testing.T) []string {
			// No migration-shaped directory name anywhere. The directory is identified by
			// holding a .sql file, which is the clause that stops a rename defeating the check.
			return []string{writeTree(t, map[string]string{
				"db/schema/0001_idx.up.sql":   "CREATE INDEX CONCURRENTLY idx ON orders (status);\n",
				"db/schema/0001_idx.down.sql": "DROP INDEX CONCURRENTLY idx;\n",
				"db/schema/notes.txt":         "written by hand\n",
			})}
		}},
		{"a kubernetes manifest", func(t *testing.T) []string {
			return []string{writeTree(t, map[string]string{
				"deploy.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n",
			})}
		}},
	}
}

// gating is one way of asking for a gate, or of not asking.
type gating struct {
	name string
	args []string

	// inForce is false only when nothing was asked to be gated, where every exit code is 0 by
	// design — grade F included.
	inForce bool
}

func gatings() []gating {
	return []gating{
		{"no gate", nil, false},
		{"--gate", []string{"--gate"}, true},
		{"--min-grade A", []string{"--min-grade", "A"}, true},
		{"--min-grade B", []string{"--min-grade", "B"}, true},
		{"--min-grade C", []string{"--min-grade", "C"}, true},
		{"--min-grade F", []string{"--min-grade", "F"}, true},
	}
}

// modifier is everything else that can change what the run sees.
type modifier struct {
	name string

	// args are appended to the command line. beforeMode and ignoreAll need per-run paths and
	// are handled separately.
	args []string

	// beforeMode picks what --before points at: "" none, "same" the same tree, "empty" an
	// empty directory.
	beforeMode string

	// ignoreAll writes a policy that ignores every path.
	ignoreAll bool
}

func (m modifier) beforePaths(c combination) []string {
	switch m.beforeMode {
	case "same":
		return c.paths
	case "empty":
		return []string{c.scratch}
	default:
		return nil
	}
}

func modifiers() []modifier {
	return []modifier{
		{name: "plain", args: []string{"--no-config"}},
		{name: "policy ignoring everything", ignoreAll: true},
		{name: "--before an identical tree", args: []string{"--no-config"}, beforeMode: "same"},
		{name: "--before an empty tree", args: []string{"--no-config"}, beforeMode: "empty"},
		{name: "--terraform-plan naming nothing", args: []string{"--no-config", "--terraform-plan", "no-such-plan.json"}},
		{name: "--context naming nothing", args: []string{"--no-config", "--context", "no-such-snapshot.json"}},
		{name: "discovering a policy", args: nil},
		// Kept in the space because a deprecated no-op that started doing something again would
		// be a silent behaviour change, and this is where that would show.
		{name: "--require-full-coverage (deprecated no-op)", args: []string{"--no-config", "--require-full-coverage"}},
	}
}

// combination is one point in the input space.
type combination struct {
	tree  tree
	gate  gating
	mod   modifier
	paths []string

	// scratch is an empty directory owned by this combination. It doubles as the --before empty
	// tree and as the home of the ignore-everything policy.
	scratch string
}

func (c combination) args(certPath string) []string {
	args := []string{"check"}
	args = append(args, c.gate.args...)
	args = append(args, c.mod.args...)

	if c.mod.ignoreAll {
		args = append(args, "--config", c.policyPath())
	}
	for _, p := range c.mod.beforePaths(c) {
		args = append(args, "--before", p)
	}

	// JSON to a file, always. The certificate has to be readable whatever the run was asked to
	// print, and the format cannot affect the exit code — TestExitCodeDoesNotDependOnFormat
	// holds that separately rather than multiplying this space by three.
	args = append(args, "--format", "json", "--output", certPath)

	return append(args, c.paths...)
}

func (c combination) policyPath() string { return filepath.Join(c.scratch, "policy.yml") }

// ---------------------------------------------------------------------------------------
// The property
// ---------------------------------------------------------------------------------------

func TestNoArgumentCombinationPassesAGateWithoutAnalysis(t *testing.T) {
	t.Parallel()

	for _, tr := range trees() {
		for _, g := range gatings() {
			for _, m := range modifiers() {
				name := fmt.Sprintf("%s/%s/%s", tr.name, g.name, m.name)

				t.Run(name, func(t *testing.T) {
					t.Parallel()

					c := combination{tree: tr, gate: g, mod: m, scratch: t.TempDir()}
					c.paths = tr.build(t)

					if m.ignoreAll {
						writePolicyIgnoringEverything(t, c.policyPath())
					}

					certPath := filepath.Join(t.TempDir(), "certificate.json")
					_, stderr, code := run(c.args(certPath)...)

					assertProperty(t, c, certPath, stderr, code)

					if cert, ok := readCertificate(t, certPath); ok {
						assertCoverageProperty(t, c, cert, code, stderr)
					}
				})
			}
		}
	}
}

// assertProperty is the whole of the invariant, stated once.
func assertProperty(t *testing.T, c combination, certPath, stderr string, code int) {
	t.Helper()

	claimed := oracleClaimsSomething(t, c)

	if code != cli.ExitOK && code != cli.ExitGateFailed && code != cli.ExitError {
		t.Fatalf("exit code %d is outside the documented contract of 0, 1, 2\n%s", code, stderr)
	}

	cert, ok := readCertificate(t, certPath)
	if !ok {
		// No certificate on disk. That is only ever acceptable as a run that did not complete:
		// a missing verdict must never be a zero exit. This is the :v1 image rule, checked here
		// from the other side.
		if code != cli.ExitError {
			t.Fatalf("no certificate was written and the run exited %d; absence of output is never success\n%s",
				code, stderr)
		}
		return
	}

	// Property 1 — the invariant itself, and it holds whether or not a gate was asked for. A
	// certificate is read by people and by merge bots that never look at an exit code.
	if !claimed {
		if cert.Grade == certificate.GradeA {
			t.Errorf("grade A with nothing claimed by any analyzer (outcome %q)", cert.Outcome)
		}
		if cert.Passed() || cert.AIGateStatus == certificate.GatePass {
			t.Errorf("gate PASS with nothing claimed by any analyzer (outcome %q)", cert.Outcome)
		}
		if cert.Assessed() {
			t.Errorf("outcome %q claims the changeset was assessed; the oracle found no analyzable file", cert.Outcome)
		}
	}

	// Property 2 — a claimed file is the only thing that makes ANALYZED legitimate. Stated in
	// both directions, so that an engine which silently stopped claiming files would fail here
	// rather than quietly reporting NO_CANDIDATES for every pull request in the world.
	if claimed && code != cli.ExitError && cert.Outcome != certificate.OutcomeAnalyzed {
		t.Errorf("outcome %q for a changeset the oracle says holds an analyzable file", cert.Outcome)
	}

	// Property 3 — the two fields that describe the same fact must never disagree.
	if cert.Applicable != cert.Assessed() {
		t.Errorf("Applicable = %v but Outcome = %q; the derived field has drifted", cert.Applicable, cert.Outcome)
	}

	if !c.gate.inForce {
		// Nothing was gated, so every completed run exits 0 by design — grade F included. There
		// is no exit-code property to check, and inventing one would be checking a gate nobody
		// asked for.
		return
	}

	// Property 4 — the one the P0 violated. Exit 0 under a gate means either the engine read
	// something, or it said in the certificate that there was nothing to read, in the one shape
	// no reader mistakes for approval.
	if code == cli.ExitOK && !claimed {
		if cert.Outcome != certificate.OutcomeNoCandidates {
			t.Errorf("exit 0 under %s with outcome %q and nothing claimed", c.gate.name, cert.Outcome)
		}
		if cert.Grade != certificate.GradeNotApplicable {
			t.Errorf("exit 0 under %s with grade %q and nothing claimed; N/A is the only honest answer",
				c.gate.name, cert.Grade)
		}
		if cert.AIGateStatus != certificate.GateNotApplicable {
			t.Errorf("exit 0 under %s with gate status %q and nothing claimed", c.gate.name, cert.AIGateStatus)
		}
	}

	// Property 5 — unsupported content is exit 2 under a gate, never 0 and never 1. Not 1,
	// because 1 means "measured and too low" and invites the fix a failed gate invites: lower
	// the threshold. No threshold makes an unread migration safe.
	if cert.Outcome == certificate.OutcomeUnsupportedContent && code != cli.ExitError {
		t.Errorf("unsupported content exited %d under %s, want %d\n%s",
			code, c.gate.name, cli.ExitError, stderr)
	}

	// Property 6 — NO_CANDIDATES has to be falsifiable, or the whole scheme collapses into a
	// permissive default wearing a different name.
	//
	// "There was nothing here" is a claim about the changeset, and this checks it against a
	// changeset that demonstrably held migration-shaped files. Without this, an engine that
	// recognised nothing at all would report NO_CANDIDATES over every repository on earth and
	// exit 0 every time, and properties 1 through 5 would all still hold.
	if !claimed && oracleSeesMigrationShapedFiles(t, c) && code != cli.ExitError {
		t.Errorf("exited %d under %s with outcome %q, over a tree holding migration-shaped files "+
			"no analyzer claimed; want %d\n%s",
			code, c.gate.name, cert.Outcome, cli.ExitError, stderr)
	}
}

// assertCoverageProperty is the second axis, checked over the same input space.
//
// It is separate from assertProperty because it is a separate question and answering them in one
// block was how the first version of this file ended up with a property that could not fail.
func assertCoverageProperty(t *testing.T, c combination, cert certificate.Certificate, code int, stderr string) {
	t.Helper()

	// Property 7 — coverage is always recorded. An unset value is not FULL, and a certificate
	// that does not say how much it read cannot be reasoned about at all.
	if !cert.FullyCovered() && cert.Coverage != certificate.CoveragePartial {
		t.Errorf("Coverage = %q, which is neither FULL nor PARTIAL", cert.Coverage)
	}

	// Property 8 — PASS requires full coverage. This is the ruling, checked at the boundary
	// rather than only in the domain unit test, because the engine assembles the certificate
	// and could set the two fields inconsistently.
	if cert.Passed() && !cert.FullyCovered() {
		t.Errorf("aiGateStatus PASS at coverage %q; an agent must not merge a partly understood changeset",
			cert.Coverage)
	}

	// Property 9 — coverage and its evidence agree in both directions. A PARTIAL with no files
	// listed is unactionable, and a FULL with files listed is incoherent.
	switch {
	case cert.FullyCovered() && len(cert.UnanalyzedFiles) > 0:
		t.Errorf("Coverage FULL with %d unanalyzed file(s) listed", len(cert.UnanalyzedFiles))
	case !cert.FullyCovered() && len(cert.UnanalyzedFiles) == 0:
		t.Error("Coverage PARTIAL with no unanalyzed files listed; a coverage gap nobody can act on")
	}

	// Every listed file carries a reason. "Not analyzed" without one sends the reviewer to read
	// the engine's source, which they will not do.
	for _, u := range cert.UnanalyzedFiles {
		if u.Path == "" || u.Reason == "" {
			t.Errorf("unanalyzed file %+v is missing a path or a reason", u)
		}
	}

	// Property 10 — the oracle's view of coverage, independent of the engine's. Migration-shaped
	// files that no analyzer claims mean PARTIAL, whatever else the run concluded.
	if oracleSeesMigrationShapedFiles(t, c) && cert.FullyCovered() {
		t.Errorf("Coverage FULL over a tree holding migration-shaped files no analyzer claimed")
	}

	// Property 11 — a partial pass is a bypass. Unconditionally: no flag, no threshold, no
	// argument combination makes a partially analyzed changeset anything but exit 2.
	//
	// This replaces a weaker property that only held when --require-full-coverage was passed.
	// The flag is now a deprecated no-op and the check is unconditional, so the property is
	// unconditional too.
	if !cert.FullyCovered() && code != cli.ExitError {
		t.Errorf("coverage %q exited %d, want %d — a partial pass is a bypass\n%s",
			cert.Coverage, code, cli.ExitError, stderr)
	}

	// Property 12 — a partially covered changeset never carries a passing grade either. The
	// exit code is what CI reads; this is what a merge bot reads, and they must agree.
	if !cert.FullyCovered() && cert.Grade == certificate.GradeA {
		t.Errorf("grade A at coverage %q", cert.Coverage)
	}

	// Property 13 — the denominator, checked against an oracle that does not ask the engine.
	//
	// Without this, reverting coverage to "count only the files an analyzer wanted" passes every
	// other property here: nothing else can tell the difference between a check that is
	// satisfied and a check that is vacuous.
	if oracleSeesUnanalyzedInMigrationDir(t, c) && cert.FullyCovered() {
		t.Errorf("Coverage FULL over a migration directory holding a file no analyzer can read")
	}
}

func readCertificate(t *testing.T, path string) (certificate.Certificate, bool) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return certificate.Certificate{}, false
	}

	var cert certificate.Certificate
	if err := json.Unmarshal(raw, &cert); err != nil {
		t.Fatalf("a certificate was written and does not parse: %v\n%s", err, raw)
	}
	return cert, true
}

func writePolicyIgnoringEverything(t *testing.T, path string) {
	t.Helper()

	body := "version: 1\nignore:\n  - \"**\"\n  - \"**/*\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the policy: %v", err)
	}
}

// The exit code is the contract with CI, and it must not depend on what the run was asked to
// print. This is what lets the property above fix the format at json and still cover the space.
func TestExitCodeDoesNotDependOnFormat(t *testing.T) {
	t.Parallel()

	for _, tr := range trees() {
		t.Run(tr.name, func(t *testing.T) {
			t.Parallel()

			paths := tr.build(t)

			first := -1
			for _, format := range []string{"json", "markdown", "sarif"} {
				args := append([]string{"check", "--gate", "--no-config", "--format", format,
					"--output", filepath.Join(t.TempDir(), "cert")}, paths...)

				_, stderr, code := run(args...)
				if first < 0 {
					first = code
					continue
				}
				if code != first {
					t.Errorf("--format %s exits %d, --format json exits %d\n%s", format, code, first, stderr)
				}
			}
		})
	}
}
