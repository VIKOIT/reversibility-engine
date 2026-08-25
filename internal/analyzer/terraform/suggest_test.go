// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

func tf010(resourceType string) domain.Finding {
	return domain.Finding{
		RuleID:        "TF010",
		Reversibility: domain.ReversibilityUnknown,
		Subject:       domain.Subject{Relation: resourceType},
	}
}

// The growth loop aggregates. Six unknown types must produce one snippet, because six paste
// operations is where somebody gives up and switches the gate off instead.
func TestUnclassifiedTypesAggregate(t *testing.T) {
	t.Parallel()

	findings := []domain.Finding{
		tf010("aws_zeta"),
		tf010("aws_alpha"),
		tf010("aws_zeta"), // the same type destroyed twice is one gap, not two
		{RuleID: "TF001", Subject: domain.Subject{Relation: "aws_db_instance"}},
		{RuleID: "TF009"},
	}

	got := terraform.UnclassifiedTypes(findings)

	want := []string{"aws_alpha", "aws_zeta"}
	if len(got) != len(want) {
		t.Fatalf("UnclassifiedTypes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UnclassifiedTypes = %v, want %v (sorted, deduplicated, TF010 only)", got, want)
		}
	}

	if len(terraform.UnclassifiedTypes(nil)) != 0 {
		t.Error("no findings produced types")
	}
}

// The snippet is one block covering everything, and it suggests the fail-closed class.
func TestPolicySnippet(t *testing.T) {
	t.Parallel()

	got := terraform.PolicySnippet([]string{"aws_alpha", "aws_zeta"})

	if n := strings.Count(got, "terraform_types:"); n != 1 {
		t.Errorf("the snippet has %d headers, want exactly 1 covering every type", n)
	}
	for _, want := range []string{"aws_alpha", "aws_zeta"} {
		if !strings.Contains(got, want) {
			t.Errorf("the snippet omits %s", want)
		}
	}

	// STATEFUL, not STATELESS. The tool does not know, and a snippet that guessed the mild
	// answer would be a snippet that talked the user into it.
	if strings.Contains(got, "STATELESS") {
		t.Errorf("the snippet suggests STATELESS:\n%s", got)
	}
	// "class: STATEFUL" rather than the bare word, which also appears in the guidance comment.
	if n := strings.Count(got, "class: STATEFUL"); n != 2 {
		t.Errorf("the snippet proposes STATEFUL %d times, want once per type:\n%s", n, got)
	}

	if terraform.PolicySnippet(nil) != "" {
		t.Error("an empty type list produced a snippet")
	}
}

// One link for every type. Nothing is sent: this is a URL printed in output the reader is
// already looking at, and there is no telemetry anywhere in this package.
func TestIssueURL(t *testing.T) {
	t.Parallel()

	got := terraform.IssueURL([]string{"aws_alpha", "aws_zeta"}, "2026.08.1")

	if got == "" {
		t.Fatal("no issue URL")
	}
	if !strings.HasPrefix(got, terraform.IssueRepository+"/issues/new?") {
		t.Errorf("URL does not point at the project's issue form: %s", got)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("the URL does not parse: %v", err)
	}

	q := parsed.Query()
	if !strings.Contains(q.Get("title"), "aws_alpha") || !strings.Contains(q.Get("title"), "aws_zeta") {
		t.Errorf("the title does not name both types: %q", q.Get("title"))
	}
	body := q.Get("body")
	for _, want := range []string{"aws_alpha", "aws_zeta", "2026.08.1", "evidence link"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body omits %q:\n%s", want, body)
		}
	}

	if terraform.IssueURL(nil, "x") != "" {
		t.Error("an empty type list produced a URL")
	}

	// A very long list collapses to a count rather than a title nobody can read.
	many := make([]string, 30)
	for i := range many {
		many[i] = "aws_some_quite_long_resource_type_name"
	}
	long, err := url.Parse(terraform.IssueURL(many, "x"))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if !strings.Contains(long.Query().Get("title"), "30 Terraform resource types") {
		t.Errorf("a long list did not collapse: %q", long.Query().Get("title"))
	}
}

// The scan suggestion uses the same evidence keys the analyzer uses at runtime, so a reviewer is
// checking the signal the tool would have acted on rather than a second heuristic.
func TestSuggestClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		attributes []string
		want       terraform.Class
		mustSay    string
	}{
		{
			name:       "storage attributes suggest stateful",
			attributes: []string{"id", "allocated_storage", "engine"},
			want:       terraform.ClassStateful,
			mustSay:    "allocated_storage",
		},
		{
			name:       "several keys are all named",
			attributes: []string{"kms_key_id", "backup_retention_period"},
			want:       terraform.ClassStateful,
			mustSay:    "backup_retention_period, kms_key_id",
		},
		{
			name:       "nothing suggestive is stateless with no reason given",
			attributes: []string{"id", "name", "vpc_id"},
			want:       terraform.ClassStateless,
		},
		{
			name:       "no attributes at all",
			attributes: nil,
			want:       terraform.ClassStateless,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, why := terraform.SuggestClass(tc.attributes)
			if got != tc.want {
				t.Errorf("SuggestClass = %s, want %s", got, tc.want)
			}
			if tc.mustSay != "" && !strings.Contains(why, tc.mustSay) {
				t.Errorf("reason %q does not mention %q", why, tc.mustSay)
			}
			if tc.mustSay == "" && why != "" {
				t.Errorf("a stateless suggestion gave a reason: %q", why)
			}
		})
	}
}

// The analyzer's identity, which the engine reads to attribute a verdict to a catalog.
func TestAnalyzerIdentity(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	if a.Name() != "terraform" {
		t.Errorf("Name = %q", a.Name())
	}
	if a.CatalogVersion() == "" {
		t.Error("CatalogVersion is empty")
	}
	if a.CatalogDigest() == "" {
		t.Error("CatalogDigest is empty")
	}
}
