package kubernetes_test

import (
	"context"
	"strings"
	"testing"

	"github.com/abdo-s1/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/abdo-s1/reversibility-engine/internal/domain"
)

// FuzzAnalyze asserts that the Kubernetes analyzer never panics and never calls a manifest it
// could not read REVERSIBLE.
//
// The YAML surface is wide: a stream decoder, a JSON round trip, quantity parsing, and a
// structural diff over untyped maps. Each is a place where a hostile document could reach a
// nil dereference or a type assertion.
func FuzzAnalyze(f *testing.F) {
	seeds := []struct{ old, new string }{
		{"", ""},
		{"kind: Deployment\n", "kind: Deployment\n"},
		{"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n", ""},
		{"", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n"},

		// Multi-document streams, the shape rendered Helm output actually takes.
		{"kind: A\n---\nkind: B\n", "kind: A\n"},
		{"---\n---\n---\n", "---\n"},
		{"kind: ConfigMap\n...\n---\nkind: Service\n", "kind: ConfigMap\n"},

		// A separator inside a scalar: content, not a boundary.
		{"kind: ConfigMap\ndata:\n  x: |\n    ---\n", "kind: ConfigMap\n"},

		// Malformed structure.
		{"kind: Deployment\nspec:\n replicas: [3\n", ""},
		{"\t\ttabs are illegal in yaml\n", ""},
		{"[1,2,3]\n", ""},
		{"just a scalar\n", ""},

		// Quantities, including ones that do not parse.
		{
			"apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: p\nspec:\n  resources:\n    requests:\n      storage: 10Gi\n",
			"apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: p\nspec:\n  resources:\n    requests:\n      storage: lots\n",
		},

		// Aliases and anchors, which expand during decoding.
		{"a: &x\n  b: 1\nc: *x\nkind: ConfigMap\nmetadata:\n  name: n\n", ""},

		// Bytes that are not text.
		{"\x00\x01\x02", "\xff\xfe"},

		// Deeply nested structure.
		{strings.Repeat("a:\n ", 200) + "b\n", ""},
	}

	for _, seed := range seeds {
		f.Add(seed.old, seed.new)
	}

	subject := kubernetes.New()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, old, updated string) {
		files := []domain.ChangedFile{{
			Path:     "k8s/manifest.yaml",
			Status:   domain.StatusModified,
			Previous: []byte(old),
			Current:  []byte(updated),
		}}

		findings, err := subject.Analyze(ctx, files)
		if err != nil {
			t.Fatalf("Analyze returned an error rather than an UNKNOWN finding: %v", err)
		}

		for _, finding := range findings {
			if finding.RuleID == "" {
				t.Error("finding has no rule ID")
			}
			if !finding.Reversibility.Valid() {
				t.Errorf("%s has invalid reversibility %q", finding.RuleID, finding.Reversibility)
			}

			// CLAUDE.md §15.2, the owner's ruling: Kubernetes findings never hold database
			// locks. It must hold for hostile input too, not only for the fixtures.
			if finding.LockHazard != domain.LockNone {
				t.Errorf("%s has lock hazard %q, want NONE", finding.RuleID, finding.LockHazard)
			}
			if strings.TrimSpace(finding.Rationale) == "" {
				t.Errorf("%s has no rationale", finding.RuleID)
			}

			switch finding.Reversibility {
			case domain.ReversibilityIrreversible, domain.ReversibilityUnknown:
				if finding.UndoStep != "" {
					t.Errorf("%s is %s yet offers undo step %q",
						finding.RuleID, finding.Reversibility, finding.UndoStep)
				}
			}
		}
	})
}

// FuzzAnalyzeAdded covers the added-only path, where there is no previous side to compare
// against and every rule that needs one must decline rather than dereference nil.
func FuzzAnalyzeAdded(f *testing.F) {
	f.Add("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: a\n")
	f.Add("apiVersion: acme.io/v1\nkind: Flurble\nmetadata:\n  name: a\n")
	f.Add("kind: \n")
	f.Add("{}")
	f.Add("null")
	f.Add("\x00")

	subject := kubernetes.New()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, manifest string) {
		files := []domain.ChangedFile{{
			Path:    "k8s/added.yaml",
			Status:  domain.StatusAdded,
			Current: []byte(manifest),
		}}

		if _, err := subject.Analyze(ctx, files); err != nil {
			t.Fatalf("Analyze: %v", err)
		}
	})
}
