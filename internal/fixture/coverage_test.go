// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package fixture_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/fixture"
)

// docs/SPECIFICATION.md §13: a rule with no fixture does not exist. This file is where that is
// enforced.
//
// It is the one test in the repository that must stay green from S1 onward, because it is what
// stops a rule table from quietly growing entries nobody ever proved.

// ruleIDPattern matches the classification rule IDs, as distinct from the DOWN* fixtures which
// exercise down-migration validation rather than a row in either table.
var ruleIDPattern = regexp.MustCompile(`^(PG|K8S|TF)\d{3}$`)

func expectedRules(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("%s%03d", prefix, i))
	}
	return out
}

func TestEveryRuleHasAFixture(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	groups := []struct {
		group  string
		prefix string
		count  int

		// retired are rule IDs that were considered and deliberately not implemented. They are
		// never reused and never renumbered: a retired ID with its reason in docs/RULES.md tells
		// a future contributor the case was thought about, where a gap in the sequence reads as
		// an oversight.
		retired map[string]bool
	}{
		{group: "postgres", prefix: "PG", count: 33},
		{group: "kubernetes", prefix: "K8S", count: 15},
		{group: "terraform", prefix: "TF", count: 10, retired: map[string]bool{"TF003": true}},
	}

	for _, g := range groups {
		t.Run(g.group, func(t *testing.T) {
			t.Parallel()

			cases, err := fixture.Cases(root, g.group)
			if err != nil {
				t.Fatalf("loading %s fixtures: %v", g.group, err)
			}

			covered := map[string]string{}
			for _, c := range cases {
				if prev, dup := covered[c.Expect.Rule]; dup {
					t.Errorf("rule %s is claimed by two fixtures: %s and %s", c.Expect.Rule, prev, c.Name)
				}
				covered[c.Expect.Rule] = c.Name
			}

			for _, rule := range expectedRules(g.prefix, g.count) {
				if g.retired[rule] {
					if name, present := covered[rule]; present {
						t.Errorf("rule %s is retired and must never be reused, but fixture %s claims it", rule, name)
					}
					continue
				}
				if _, ok := covered[rule]; !ok {
					t.Errorf("rule %s has no fixture; per docs/SPECIFICATION.md §13 it therefore does not exist", rule)
				}
			}
		})
	}
}

// A fixture that claims a rule must actually produce a finding for it, otherwise it proves
// nothing and the coverage test above becomes decorative.
func TestFixtureClaimsMatchTheirFindings(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	for _, group := range []string{"postgres", "kubernetes", "terraform"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, c := range cases {
			t.Run(group+"/"+c.Name, func(t *testing.T) {
				if strings.TrimSpace(c.Expect.Note) == "" {
					t.Errorf("fixture has no note explaining why it is shaped the way it is")
				}
				if len(c.Expect.Findings) == 0 {
					t.Fatalf("fixture asserts no findings")
				}

				// The DOWN* fixtures exercise down-migration validation, so their claimed
				// "rule" is not a row in either classification table. They must instead
				// assert a down-migration outcome.
				if !ruleIDPattern.MatchString(c.Expect.Rule) {
					if len(c.Expect.DownMigrations) == 0 {
						t.Errorf("fixture claims non-table rule %q but asserts no downMigrations", c.Expect.Rule)
					}
					return
				}

				for _, f := range c.Expect.Findings {
					if f.RuleID == c.Expect.Rule {
						return
					}
				}
				t.Errorf("fixture claims rule %s but none of its %d findings carry that rule ID",
					c.Expect.Rule, len(c.Expect.Findings))
			})
		}
	}
}

// Every asserted classification must be a value the domain recognises. A fixture asserting
// "IRREVERSABLE" would otherwise sit green forever, testing nothing.
func TestFixtureAssertionsAreWellFormed(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	for _, group := range []string{"postgres", "kubernetes", "terraform"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, c := range cases {
			t.Run(group+"/"+c.Name, func(t *testing.T) {
				for i, f := range c.Expect.Findings {
					if f.RuleID == "" {
						t.Errorf("finding %d has no ruleId", i)
					}
					if f.File == "" {
						t.Errorf("finding %d has no file", i)
					}
					if !f.Reversibility.Valid() {
						t.Errorf("finding %d: reversibility %q is not a recognised verdict", i, f.Reversibility)
					}
					if !f.LockHazard.Valid() {
						t.Errorf("finding %d: lockHazard %q is not a recognised hazard", i, f.LockHazard)
					}

					// An IRREVERSIBLE change with an undo step would be a contradiction the
					// certificate should never contain.
					if f.Reversibility == "IRREVERSIBLE" && f.WantUndoStep {
						t.Errorf("finding %d is IRREVERSIBLE yet expects an undo step", i)
					}

					// docs/RULES.md §4.2: Kubernetes findings never hold database locks.
					if group == "kubernetes" && f.LockHazard != "NONE" {
						t.Errorf("finding %d: Kubernetes lockHazard is %q, but the owner ruled it is strictly NONE", i, f.LockHazard)
					}
				}
			})
		}
	}
}
