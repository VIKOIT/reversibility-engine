// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package kubernetes_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// TestAnalyzeFixtures drives the analyzer over every Kubernetes fixture.
//
// EXPECTED TO FAIL until S3 implements the analyzer.
func TestAnalyzeFixtures(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	cases, err := fixture.Cases(root, "kubernetes")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	files := provider.NewFake(root)
	subject := kubernetes.New()

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			changed, err := files.ChangedFiles(ctx, tc.Ref)
			if err != nil {
				t.Fatalf("resolving changeset: %v", err)
			}
			if len(changed) == 0 {
				t.Fatalf("fixture resolved to an empty changeset")
			}

			got, err := subject.Analyze(ctx, changed)
			if err != nil {
				t.Fatalf("Analyze: %v\n\nfixture rationale: %s", err, tc.Expect.Note)
			}

			domain.SortFindings(got)

			if diff := cmp.Diff(tc.Expect.Findings, fixture.Project(got)); diff != "" {
				t.Errorf("classification mismatch (-want +got):\n%s\n\nfixture rationale: %s", diff, tc.Expect.Note)
			}

			for _, f := range got {
				if strings.TrimSpace(f.Rationale) == "" {
					t.Errorf("%s at %s has no rationale", f.RuleID, f.File)
				}

				// docs/RULES.md §4.2, the owner's ruling: Kubernetes findings never hold
				// database locks.
				if f.LockHazard != domain.LockNone {
					t.Errorf("%s at %s has LockHazard %q; Kubernetes findings are strictly NONE",
						f.RuleID, f.File, f.LockHazard)
				}

				if f.Reversibility == domain.ReversibilityIrreversible && f.UndoStep != "" {
					t.Errorf("%s at %s is IRREVERSIBLE but offers an undo step %q",
						f.RuleID, f.File, f.UndoStep)
				}
			}
		})
	}
}

func TestSupports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"k8s/deployment.yaml", true},
		{"k8s/service.yml", true},
		{"K8S/DEPLOYMENT.YAML", true},
		{"migrations/0001.sql", false},
		{"README.md", false},
		{"yaml", false},
		{"", false},
		{"chart.yaml.tpl", false},
		{"dir.yaml/file.txt", false},
	}

	subject := kubernetes.New()
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := subject.Supports(tt.path); got != tt.want {
				t.Errorf("Supports(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	if got := kubernetes.New().Name(); got != "kubernetes" {
		t.Errorf("Name() = %q, want %q", got, "kubernetes")
	}
}

func TestAnalyzeRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := kubernetes.New().Analyze(ctx, nil)
	if err == nil {
		t.Fatalf("Analyze with a cancelled context returned nil error and %d findings", len(got))
	}
}
