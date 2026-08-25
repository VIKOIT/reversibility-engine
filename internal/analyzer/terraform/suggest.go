// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// IssueRepository is where a contributed classification goes.
//
// STRICTLY NO TELEMETRY. Nothing in this package sends anything anywhere. This is a URL a human
// may choose to click, in output they are already reading — a tool that inspects production
// infrastructure and phones home does not get adopted by anyone serious, and the moment it does
// so once, no amount of documentation undoes it.
const IssueRepository = "https://github.com/VIKOIT/reversibility-engine"

// UnclassifiedTypes returns the resource types a set of findings could not classify, sorted and
// deduplicated.
//
// It reads TF010 findings, which carry the type in Subject.Relation, so nothing has to be
// re-parsed to build the suggestion.
func UnclassifiedTypes(findings []domain.Finding) []string {
	seen := map[string]bool{}

	for _, f := range findings {
		if f.RuleID != "TF010" || f.Subject.Relation == "" {
			continue
		}
		seen[f.Subject.Relation] = true
	}

	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// PolicySnippet renders one .reversibility.yml block covering every unclassified type at once.
//
// ONE snippet for all of them, not one each. Six unknown types meaning six paste operations is
// where somebody gives up and switches the gate off instead — which costs more safety than any
// single classification buys back.
//
// The suggested class is STATEFUL for all of them. That is the fail-closed direction and the
// honest one: the tool does not know, and a snippet that guessed STATELESS would be a snippet
// that talked the user into the answer they wanted.
func PolicySnippet(types []string) string {
	if len(types) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("terraform_types:\n")
	for _, t := range types {
		fmt.Fprintf(&b, "  - type: %s\n", t)
		b.WriteString("    class: STATEFUL   # STATEFUL if destroying it loses data, an identity, or a recovery path\n")
	}

	return b.String()
}

// IssueURL builds a pre-filled issue for contributing the types upstream.
//
// One issue for all of them, for the same reason as the snippet. Nothing is sent: this is a link
// printed in output, and it opens a form the user fills in and submits themselves.
func IssueURL(types []string, catalogVersion string) string {
	if len(types) == 0 {
		return ""
	}

	title := fmt.Sprintf("Catalog: classify %s", strings.Join(types, ", "))
	if len(title) > 120 {
		title = fmt.Sprintf("Catalog: classify %d Terraform resource types", len(types))
	}

	var body strings.Builder
	body.WriteString("These resource types were destroyed by a plan and are not in the catalog.\n\n")
	fmt.Fprintf(&body, "Catalog version: %s\n\n", catalogVersion)
	body.WriteString("| Type | Proposed class | Evidence |\n| --- | --- | --- |\n")
	for _, t := range types {
		fmt.Fprintf(&body, "| `%s` | | |\n", t)
	}
	body.WriteString("\nA classification needs an evidence link — the provider documentation for the type.\n")
	body.WriteString("STATEFUL means destroying it destroys data, destroys an identity that re-applying ")
	body.WriteString("the same configuration cannot recreate, or destroys a recovery capability a ")
	body.WriteString("rollback would depend on.\n")

	return fmt.Sprintf("%s/issues/new?title=%s&body=%s&labels=%s",
		IssueRepository,
		url.QueryEscape(title),
		url.QueryEscape(body.String()),
		url.QueryEscape("catalog"),
	)
}
