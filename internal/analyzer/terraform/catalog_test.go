// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform_test

import (
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/catalog"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
)

func embedded(t *testing.T) *terraform.Catalog {
	t.Helper()

	c, err := terraform.LoadEmbedded(catalog.AWS)
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return c
}

// EVERY ENTRY MUST CARRY A CLASS AND AN EVIDENCE LINK, and this is where that is enforced.
//
// A classification nobody can check is an opinion. The evidence link is what lets a reviewer who
// did not write the entry decide whether it is right, and it is the reason `catalog scan` output
// cannot be merged unread: a generated candidate has an empty evidence field and fails here.
func TestEveryEntryCarriesClassAndEvidence(t *testing.T) {
	t.Parallel()

	c := embedded(t)

	if len(c.Entries) == 0 {
		t.Fatal("the embedded catalog is empty")
	}

	seen := map[string]bool{}
	for _, e := range c.Entries {
		if !e.Class.Valid() {
			t.Errorf("%s: class %q is not STATEFUL or STATELESS", e.Type, e.Class)
		}
		if strings.TrimSpace(e.Evidence) == "" {
			t.Errorf("%s: no evidence link. A classification nobody can check is an opinion.", e.Type)
		}
		if !strings.HasPrefix(e.Evidence, "https://") {
			t.Errorf("%s: evidence %q is not a link", e.Type, e.Evidence)
		}
		if strings.TrimSpace(e.AddedIn) == "" {
			t.Errorf("%s: no added_in version", e.Type)
		}
		if seen[e.Type] {
			t.Errorf("%s is classified twice", e.Type)
		}
		seen[e.Type] = true
	}
}

// A malformed catalog is refused rather than half-read. Reading the entries that happen to parse
// would classify production resources from a document nobody verified.
func TestMalformedCatalogsAreRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		mustSay string
	}{
		{
			name:    "no version",
			body:    "provider: p\nentries: []\n",
			mustSay: "catalog_version",
		},
		{
			name:    "no provider",
			body:    "catalog_version: \"1\"\nentries: []\n",
			mustSay: "provider",
		},
		{
			name: "an entry with no evidence",
			body: "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATEFUL\n",
			// The exact requirement, quoted back.
			mustSay: "evidence is required",
		},
		{
			name:    "an entry with an unrecognised class",
			body:    "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: MAYBE\n    evidence: \"https://x\"\n",
			mustSay: "class",
		},
		{
			name:    "evidence that is not a link",
			body:    "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATEFUL\n    evidence: \"ask Dave\"\n",
			mustSay: "not a link",
		},
		{
			name:    "the same type twice",
			body:    "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATEFUL\n    evidence: \"https://x\"\n  - type: aws_thing\n    class: STATELESS\n    evidence: \"https://y\"\n",
			mustSay: "classified twice",
		},
		{
			name:    "a key this build does not know",
			body:    "catalog_version: \"1\"\nprovider: p\nentries: []\nsomething_newer: true\n",
			mustSay: "unknown field",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, err := terraform.ParseCatalog([]byte(tc.body))
			if err == nil {
				t.Fatalf("ParseCatalog accepted it: %+v", c)
			}
			if !strings.Contains(err.Error(), tc.mustSay) {
				t.Errorf("error %q does not mention %q", err, tc.mustSay)
			}
		})
	}
}

// The catalog is an input to a verdict, so its identity has to be stable and has to change when
// a classification does.
func TestCatalogDigest(t *testing.T) {
	t.Parallel()

	first := embedded(t)
	again := embedded(t)

	if first.Digest() != again.Digest() {
		t.Error("loading the same catalog twice produced two digests")
	}
	if first.Digest() == "" {
		t.Error("the catalog has no digest")
	}

	base := "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATELESS\n    evidence: \"https://x\"\n"
	tightened := "catalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATEFUL\n    evidence: \"https://x\"\n"

	a, err := terraform.ParseCatalog([]byte(base))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	b, err := terraform.ParseCatalog([]byte(tightened))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}

	if a.Digest() == b.Digest() {
		t.Error("changing a classification did not change the digest")
	}

	// Comments and ordering are not the catalog. A digest that moved when somebody reflowed the
	// file would report a change that did not happen.
	reordered := "# a comment\ncatalog_version: \"1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATELESS\n    evidence: \"https://different-but-same-class\"\n    added_in: \"9\"\n"
	c, err := terraform.ParseCatalog([]byte(reordered))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if c.Digest() != a.Digest() {
		t.Error("a comment and an evidence-link edit changed the digest; only classifications should")
	}
}

// The trap, pinned as data: the catalog itself must not have drifted into classifying by name.
func TestTheNameTrapIsStillClassifiedCorrectly(t *testing.T) {
	t.Parallel()

	c := embedded(t)

	for _, tc := range []struct {
		resourceType string
		want         terraform.Class
		why          string
	}{
		{"aws_db_subnet_group", terraform.ClassStateless, "contains \"db\" and holds nothing"},
		{"aws_db_instance", terraform.ClassStateful, "holds the rows"},
		{"aws_eip", terraform.ClassStateful, "the address cannot be recovered"},
		{"aws_instance", terraform.ClassStateless, "cattle; instance store raises itself via evidence"},
		{"aws_acm_certificate", terraform.ClassStateless, "re-issuable; an imported cert raises itself via private_key"},
		{"aws_db_snapshot", terraform.ClassStateful, "destroying it destroys the undo"},
	} {
		e, ok := c.Lookup(tc.resourceType)
		if !ok {
			t.Errorf("%s is not in the catalog", tc.resourceType)
			continue
		}
		if e.Class != tc.want {
			t.Errorf("%s is %s, want %s — %s", tc.resourceType, e.Class, tc.want, tc.why)
		}
	}
}

// The coverage number published in the docs comes from here, so it is asserted rather than
// remembered.
func TestCoverageIsPublishable(t *testing.T) {
	t.Parallel()

	c := embedded(t)
	stateful, stateless := c.Coverage()

	if stateful+stateless != len(c.Entries) {
		t.Errorf("coverage counts %d+%d do not add up to %d entries", stateful, stateless, len(c.Entries))
	}

	// The stateless half is load-bearing: an unclassified deleted type grades F, so without a
	// solid network/IAM/LB/compute core the first ordinary plan anybody runs fails.
	if stateless < 30 {
		t.Errorf("only %d stateless entries; the seed needs enough of them that an ordinary plan does not grade F on its first run", stateless)
	}

	t.Logf("catalog %s: %d types classified (%d stateful, %d stateless)", c.Version, len(c.Entries), stateful, stateless)
}
