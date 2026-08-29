// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package fixture_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The README's rules badge said `27 PG` while the table defined 59. It had been wrong for
// thirty-two rules, and nothing noticed, because nothing checked it.
//
// Deriving that one number by test was the right fix, and it is a pattern rather than a fix:
//
//	**Any number in documentation that duplicates a fact stated in the specification must be
//	derived by test, not maintained by hand.**
//
// Every number below is a fact recorded somewhere authoritative — a rule table, an embedded
// catalog, a Go constant, a release matrix — and copied somewhere readable. A copy that nothing
// compares is a copy that will drift, and in a safety tool the front page is what a reader
// believes before they read anything else.
//
// The test is deliberately structured as one table of claims rather than one function per
// number, so that adding a documented number means adding a row. See docs/SPECIFICATION.md
// §16.15.

// derivedNumber is one documented claim and the authority it must agree with.
type derivedNumber struct {
	// what the claim is, for the failure message.
	name string

	// file the claim appears in, relative to the repository root.
	file string

	// claim extracts the documented number. The first capture group is the number.
	claim *regexp.Regexp

	// truth returns the authoritative value.
	truth func(t *testing.T, root string) string

	// fold compares case-insensitively, for a claim that opens a sentence.
	fold bool

	// why explains, in the failure, where the authority lives.
	why string
}

func derivedNumbers() []derivedNumber {
	return []derivedNumber{
		{
			name:  "the Terraform catalog's resource-type count in the README's rules table",
			file:  "README.md",
			claim: regexp.MustCompile(`embedded catalog of (\d+) AWS resource types`),
			truth: func(t *testing.T, root string) string { return strconv.Itoa(catalogEntries(t, root)) },
			why:   "catalog/terraform/aws.yaml is the authority; it is embedded and shipped",
		},
		{
			name:  "the Terraform catalog's resource-type count in Limitations",
			file:  "README.md",
			claim: regexp.MustCompile(`covers \*\*(\d+) AWS\s+resource types`),
			truth: func(t *testing.T, root string) string { return strconv.Itoa(catalogEntries(t, root)) },
			why:   "catalog/terraform/aws.yaml is the authority; it is embedded and shipped",
		},
		{
			name:  "the Terraform catalog's stateful count",
			file:  "README.md",
			claim: regexp.MustCompile(`roughly 1,400\*\* — (\d+) stateful`),
			truth: func(t *testing.T, root string) string { return strconv.Itoa(catalogClass(t, root, "STATEFUL")) },
			why:   "catalog/terraform/aws.yaml is the authority",
		},
		{
			name:  "the Terraform catalog's stateless count",
			file:  "README.md",
			claim: regexp.MustCompile(`(\d+) stateless\.`),
			truth: func(t *testing.T, root string) string { return strconv.Itoa(catalogClass(t, root, "STATELESS")) },
			why:   "catalog/terraform/aws.yaml is the authority",
		},
		{
			name:  "the certificate schema version in the README",
			file:  "README.md",
			claim: regexp.MustCompile("currently `(\\d+\\.\\d+\\.\\d+)`"),
			truth: schemaVersion,
			why:   "domain.SchemaVersion is the single place the version lives",
		},
		{
			name: "the number of release targets in the specification",
			file: filepath.Join("docs", "SPECIFICATION.md"),
			// Case-insensitive: the claim opens a sentence, so it is spelled "Four".
			claim: regexp.MustCompile(`(?i)(\w+) native targets, one runner each`),
			truth: func(t *testing.T, root string) string { return spellOut(releaseTargets(t, root)) },
			fold:  true,
			why:   ".github/workflows/release.yml is the authority; it is what actually builds them",
		},
	}
}

func TestDocumentedNumbersAreDerivedFromTheirAuthority(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	for _, dn := range derivedNumbers() {
		t.Run(dn.name, func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(filepath.Join(root, dn.file))
			if err != nil {
				t.Fatalf("reading %s: %v", dn.file, err)
			}

			m := dn.claim.FindStringSubmatch(string(body))
			if m == nil {
				// A claim that has moved is not a claim that is correct. Failing here rather than
				// passing vacuously is the whole point: the previous version of this idea checked
				// one badge, and everything it did not check drifted.
				t.Fatalf("%s no longer states this number in the expected shape.\n"+
					"If the wording changed, move the pattern with it; if the claim was removed, "+
					"remove this row. A claim nothing checks is a claim that will drift.", dn.file)
			}

			got, want := m[1], dn.truth(t, root)
			if dn.fold {
				got, want = strings.ToLower(got), strings.ToLower(want)
			}

			if got != want {
				t.Errorf("%s claims %q and the authority says %q.\n%s.\n"+
					"Documentation numbers are derived, not maintained: update the prose.",
					dn.file, got, want, dn.why)
			}
		})
	}
}

// catalogEntries counts the resource types in the embedded Terraform catalog.
func catalogEntries(t *testing.T, root string) int {
	t.Helper()
	return countMatches(t, filepath.Join(root, "catalog", "terraform", "aws.yaml"),
		regexp.MustCompile(`(?m)^\s+- type:\s`))
}

// catalogClass counts the entries of one classification.
func catalogClass(t *testing.T, root, class string) int {
	t.Helper()
	return countMatches(t, filepath.Join(root, "catalog", "terraform", "aws.yaml"),
		regexp.MustCompile(`(?m)^\s+class:\s+`+regexp.QuoteMeta(class)+`\s*$`))
}

// releaseTargets counts the build matrix entries in the release workflow.
func releaseTargets(t *testing.T, root string) int {
	t.Helper()
	return countMatches(t, filepath.Join(root, ".github", "workflows", "release.yml"),
		regexp.MustCompile(`(?m)^\s+- \{ runner:.*goos:`))
}

// schemaVersion reads the one place the certificate schema version lives.
func schemaVersion(t *testing.T, root string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "internal", "domain", "certificate.go"))
	if err != nil {
		t.Fatalf("reading the domain certificate: %v", err)
	}

	m := regexp.MustCompile(`SchemaVersion = "([^"]+)"`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("domain.SchemaVersion is no longer a string constant; this test cannot derive it")
	}
	return m[1]
}

func countMatches(t *testing.T, path string, pattern *regexp.Regexp) int {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	n := len(pattern.FindAllString(string(body), -1))
	if n == 0 {
		t.Fatalf("%s matched nothing for %s; this test would assert nothing", path, pattern)
	}
	return n
}

// spellOut renders a small count as the word prose uses.
//
// Only the range the documents actually contain. A number outside it returns its digits, which
// will fail the comparison loudly rather than pass by accident.
func spellOut(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	if n >= 0 && n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}

// TestDocumentedRuleIDRangesMatchTheTables extends the same rule to the ID ranges spelled into
// headings and links.
//
// `PG001-PG059` appears in a section heading, in anchors, and in the README's rules table. Each
// is a claim about the highest rule the table defines, and each drifts the same way the badge
// did — silently, and in the direction of understating what the engine does.
func TestDocumentedRuleIDRangesMatchTheTables(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	highest := map[string]int{}

	for id := range tabledRules(t, root) {
		prefix := strings.TrimRight(id, "0123456789")
		n, err := strconv.Atoi(id[len(prefix):])
		if err != nil {
			t.Fatalf("rule ID %q does not end in a number", id)
		}
		if n > highest[prefix] {
			highest[prefix] = n
		}
	}

	for _, file := range []string{"README.md", filepath.Join("docs", "RULES.md")} {
		body, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}

		// Every "PG001-PG059" / "PG001–PG059" style range in the prose.
		// "PG001–PG059", "PG001-PG059", and "PG001 to PG059" all state the same claim, and all
		// three spellings are in use across the two documents.
		ranges := regexp.MustCompile(`(PG|K8S|TF)001\s*(?:[-–]|to)\s*(PG|K8S|TF)(\d{3})`).
			FindAllStringSubmatch(string(body), -1)
		if len(ranges) == 0 {
			t.Errorf("%s states no rule ID range; if the wording changed, move this check with it", file)
			continue
		}

		for _, m := range ranges {
			if m[1] != m[2] {
				t.Errorf("%s states a range across two prefixes: %s", file, m[0])
				continue
			}

			claimed, err := strconv.Atoi(m[3])
			if err != nil {
				t.Errorf("%s: %q does not end in a number", file, m[0])
				continue
			}

			if claimed != highest[m[1]] {
				t.Errorf("%s claims the %s table ends at %03d and it ends at %03d (%s).\n"+
					"The tables are the specification; a range in prose is a claim about them.",
					file, m[1], claimed, highest[m[1]], m[0])
			}
		}
	}
}
