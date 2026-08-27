// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package fixture_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/fixture"
)

// docs/SPECIFICATION.md §13: every construct the code can classify must have a row in the
// authoritative table. A classification with no table row does not exist.
//
// This is the sibling of TestEveryRuleHasAFixture and it exists for a reason found the
// expensive way. `convertDrop` folded OBJECT_MATVIEW into KindDropView, so DROP MATERIALIZED
// VIEW was graded by PG016 — a row that lists plain views, functions and triggers and says
// nothing about materialized views. The code was classifying a construct the table did not
// list, the rationale it printed was false, and every test in the repository passed.
//
// A fixture test cannot catch that: PG016 had a fixture, and the fixture used a plain view.
// What was missing was the check in the other direction.

// ruleLiteral matches a rule ID as it appears in analyzer source, e.g. ruleID: "PG029".
var ruleLiteral = regexp.MustCompile(`"((?:PG|K8S|TF)\d{3})"`)

// tableRow matches a rule ID in the leading cell of a row in docs/RULES.md.
var tableRow = regexp.MustCompile(`(?m)^\|\s*((?:PG|K8S|TF)\d{3})\s*\|`)

// repoRoot walks up from the fixture root to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal("could not locate the repository root from the fixture root")
	return ""
}

// classifiableRules returns every rule ID the analyzers can emit.
//
// It reads the source rather than driving the classifiers, and that is deliberate. Several
// kinds branch on the statement's fields — ADD COLUMN alone reaches PG018, PG019 or PG020
// depending on nullability and default — so calling each classifier once per Kind would report
// a subset and pass while the table drifted. The set of literals in the source is exactly "what
// the code can emit", which is what the invariant is about.
func classifiableRules(t *testing.T, root string) map[string]string {
	t.Helper()

	found := map[string]string{}
	analyzers := filepath.Join(root, "internal", "analyzer")

	err := filepath.WalkDir(analyzers, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}

		for _, m := range ruleLiteral.FindAllStringSubmatch(string(src), -1) {
			if _, seen := found[m[1]]; !seen {
				found[m[1]] = filepath.ToSlash(rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the analyzers: %v", err)
	}

	if len(found) == 0 {
		t.Fatal("no rule IDs found in the analyzer sources; this test is not testing anything")
	}
	return found
}

// tabledRules returns every rule ID with a row in docs/RULES.md.
func tabledRules(t *testing.T, root string) map[string]bool {
	t.Helper()

	src, err := os.ReadFile(filepath.Join(root, "docs", "RULES.md"))
	if err != nil {
		t.Fatalf("reading docs/RULES.md: %v", err)
	}

	found := map[string]bool{}
	for _, m := range tableRow.FindAllStringSubmatch(string(src), -1) {
		found[m[1]] = true
	}

	if len(found) == 0 {
		t.Fatal("no rule rows found in docs/RULES.md; this test is not testing anything")
	}
	return found
}

// retiredRules were specified, considered, and deliberately not implemented. They have a row in
// the table explaining why and no case in any classifier, and they are never reused.
var retiredRules = map[string]bool{"TF003": true}

func TestEveryClassificationHasATableRow(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	emitted := classifiableRules(t, root)
	tabled := tabledRules(t, root)

	// Direction 1: the code classifies something the table does not list. This is the D2
	// failure, and it is the more dangerous of the two — the verdict ships, a reader trusts it,
	// and the document that is supposed to be authoritative never mentioned the construct.
	var undocumented []string
	for id, file := range emitted {
		if !tabled[id] {
			undocumented = append(undocumented, id+" (emitted by "+file+")")
		}
	}
	sort.Strings(undocumented)

	for _, id := range undocumented {
		t.Errorf("%s has no row in docs/RULES.md.\n"+
			"A classification with no table row does not exist: either add the row, or stop emitting the ID.", id)
	}

	// Direction 2: the table lists something nothing can emit. Less dangerous and still wrong —
	// it advertises a guarantee the engine does not provide, and a reader cannot tell which
	// rows are real.
	var unimplemented []string
	for id := range tabled {
		if _, ok := emitted[id]; !ok && !retiredRules[id] {
			unimplemented = append(unimplemented, id)
		}
	}
	sort.Strings(unimplemented)

	for _, id := range unimplemented {
		t.Errorf("%s has a row in docs/RULES.md and no classifier case emits it.\n"+
			"Either implement it, or declare it retired in retiredRules with its reason in the table.", id)
	}
}

// A retired ID must stay retired: no classifier may quietly start emitting one, because the ID
// is documented as deliberately unimplemented and a consumer may be suppressing it.
func TestRetiredRulesAreNotEmitted(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	emitted := classifiableRules(t, root)

	for id := range retiredRules {
		if file, ok := emitted[id]; ok {
			t.Errorf("retired rule %s is emitted by %s; retired IDs are never reused and never renumbered", id, file)
		}
	}
}
