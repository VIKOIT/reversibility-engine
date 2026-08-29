// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package fixture_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The README's rules badge said `27 PG · 15 K8S · 9 TF` while the table held 59 PG rules.
//
// It is the smallest defect in the audit and it is here for a reason that is not its size: the
// badge is the first claim a reader meets, and it had been wrong for thirty-two rules without
// anything noticing. Nothing checked it, so nothing could. The rule tables are the product
// specification and every other claim about them is checked against them — TestEveryRuleHasAFixture
// in one direction, TestEveryClassificationHasATableRow in the other — and the number on the
// front page was the one claim exempt.
//
// So this is not a test that the badge currently reads 59. It is a test that the badge is derived
// from the table, which is what stops the next thirty-two rules from arriving unannounced.

// badgePattern matches the shields.io rules badge in the README.
//
// The separator is a URL-encoded interpunct, and the three counts are the three groups. Written
// as one pattern rather than three so that reordering the badge fails loudly rather than
// silently matching two of three.
var badgePattern = regexp.MustCompile(
	`!\[Rules\]\(https://img\.shields\.io/badge/rules-(\d+)%20PG%20%C2%B7%20(\d+)%20K8S%20%C2%B7%20(\d+)%20TF-`)

// tocCounts matches the per-section counts in the table of contents of docs/RULES.md.
var tocCounts = map[string]*regexp.Regexp{
	"PG":  regexp.MustCompile(`\[§1 PostgreSQL\][^|]*\|[^|]*\|\s*(\d+)\s*\|`),
	"K8S": regexp.MustCompile(`\[§2 Kubernetes\][^|]*\|[^|]*\|\s*(\d+)\s*\|`),
	"TF":  regexp.MustCompile(`\[§5 Terraform\][^|]*\|[^|]*\|\s*\*\*(\d+) active\*\*`),
}

// activeRuleCounts counts the rule IDs docs/RULES.md defines, by prefix, excluding retired ones.
//
// Retired IDs are subtracted rather than skipped by pattern: a retired rule still has a row and
// still occupies its number forever, and the badge counts what the engine can emit.
func activeRuleCounts(t *testing.T, root string) map[string]int {
	t.Helper()

	counts := map[string]int{}
	for id := range tabledRules(t, root) {
		if retiredRules[id] {
			continue
		}
		counts[strings.TrimRight(id, "0123456789")]++
	}

	for _, prefix := range []string{"PG", "K8S", "TF"} {
		if counts[prefix] == 0 {
			t.Fatalf("no %s rules found in docs/RULES.md; this test is not testing anything", prefix)
		}
	}
	return counts
}

func TestTheReadmeBadgeMatchesTheRuleTables(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	counts := activeRuleCounts(t, root)

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}

	m := badgePattern.FindStringSubmatch(string(readme))
	if m == nil {
		t.Fatal("the README has no rules badge in the expected shape; if it moved, move this test with it")
	}

	for i, prefix := range []string{"PG", "K8S", "TF"} {
		claimed, err := strconv.Atoi(m[i+1])
		if err != nil {
			t.Fatalf("badge %s count %q is not a number: %v", prefix, m[i+1], err)
		}

		if claimed != counts[prefix] {
			t.Errorf(
				"the README badge claims %d %s rules and docs/RULES.md defines %d.\n"+
					"The tables are the specification; the badge is a claim about them. Update the badge:\n"+
					"  %s",
				claimed, prefix, counts[prefix], suggestedBadge(counts))
		}
	}
}

// TestTheRulesTableOfContentsMatchesItsOwnTables checks the same claim one file in.
//
// docs/RULES.md opens with a table of contents that states each section's rule count, and it can
// drift from the rows below it exactly as the badge drifted from the file. A specification that
// disagrees with itself is worse than one that is merely out of date, because a reader has no way
// to tell which half to believe.
func TestTheRulesTableOfContentsMatchesItsOwnTables(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	counts := activeRuleCounts(t, root)

	src, err := os.ReadFile(filepath.Join(root, "docs", "RULES.md"))
	if err != nil {
		t.Fatalf("reading docs/RULES.md: %v", err)
	}

	for _, prefix := range []string{"PG", "K8S", "TF"} {
		m := tocCounts[prefix].FindStringSubmatch(string(src))
		if m == nil {
			t.Errorf("the %s row of the table of contents in docs/RULES.md no longer states a count", prefix)
			continue
		}

		claimed, err := strconv.Atoi(m[1])
		if err != nil {
			t.Errorf("the %s count %q in the table of contents is not a number: %v", prefix, m[1], err)
			continue
		}

		if claimed != counts[prefix] {
			t.Errorf("the table of contents claims %d %s rules and the §-tables below define %d",
				claimed, prefix, counts[prefix])
		}
	}
}

// suggestedBadge renders the line the README should carry, so a failure is a fix and not a task.
func suggestedBadge(counts map[string]int) string {
	return fmt.Sprintf(
		"![Rules](https://img.shields.io/badge/rules-%d%%20PG%%20%%C2%%B7%%20%d%%20K8S%%20%%C2%%B7%%20%d%%20TF-blue)",
		counts["PG"], counts["K8S"], counts["TF"])
}
