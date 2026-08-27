// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/render"
)

// The certificate said two things at once and shipped that way for a release: three lines under
// a green **PASS**, the markdown read "the engine has no opinion on it". A reader resolves that
// contradiction in favour of the badge every time, which made the disclaimer worse than useless
// — it was the part that looked like diligence.
//
// The rule this file enforces is one sentence: if the prose disclaims, nothing says PASS. It is
// driven through the real engine rather than over hand-built certificates, because the bug was
// never in the renderer alone. It was in a certificate whose fields disagreed with each other
// before any renderer saw it.

// proseCase is a changeset and what the run over it is expected to conclude.
type proseCase struct {
	name    string
	files   []domain.ChangedFile
	outcome domain.AnalysisOutcome
}

func proseCases() []proseCase {
	return []proseCase{
		{
			name:    "docs only",
			outcome: domain.OutcomeNoCandidates,
			files: []domain.ChangedFile{
				{Path: "README.md", Status: domain.StatusModified, Current: []byte("# hello\n")},
				{Path: "main.go", Status: domain.StatusModified, Current: []byte("package main\n")},
			},
		},
		{
			name:    "no files at all",
			outcome: domain.OutcomeNoCandidates,
		},
		{
			name:    "django migrations",
			outcome: domain.OutcomeUnsupportedContent,
			files: []domain.ChangedFile{
				{Path: "app/migrations/0001_initial.py", Status: domain.StatusAdded, Current: []byte("from django.db import migrations\n")},
				{Path: "app/migrations/0002_add_field.py", Status: domain.StatusAdded, Current: []byte("from django.db import migrations\n")},
			},
		},
		{
			name:    "rails migrations",
			outcome: domain.OutcomeUnsupportedContent,
			files: []domain.ChangedFile{
				{Path: "db/migrate/20240101000000_create_orders.rb", Status: domain.StatusAdded, Current: []byte("class CreateOrders < ActiveRecord::Migration\nend\n")},
			},
		},
		{
			name:    "a reversible migration",
			outcome: domain.OutcomeAnalyzed,
			files: []domain.ChangedFile{
				{Path: "0001_idx.up.sql", Status: domain.StatusAdded, Current: []byte("CREATE INDEX CONCURRENTLY idx ON orders (status);\n")},
				{Path: "0001_idx.down.sql", Status: domain.StatusAdded, Current: []byte("DROP INDEX CONCURRENTLY idx;\n")},
			},
		},
		{
			name:    "a destructive migration",
			outcome: domain.OutcomeAnalyzed,
			files: []domain.ChangedFile{
				{Path: "0001_drop.up.sql", Status: domain.StatusAdded, Current: []byte("DROP TABLE legacy_orders;\n")},
				{Path: "0001_drop.down.sql", Status: domain.StatusAdded, Current: []byte("CREATE TABLE legacy_orders (id bigint);\n")},
			},
		},
		{
			name:    "an unparseable migration",
			outcome: domain.OutcomeAnalyzed,
			files: []domain.ChangedFile{
				{Path: "0001_broken.up.sql", Status: domain.StatusAdded, Current: []byte("THIS IS NOT SQL AT ALL ;;;\n")},
			},
		},
	}
}

func certifyProseCase(t *testing.T, c proseCase) domain.ReversibilityCertificate {
	t.Helper()

	eng := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})

	cert, _ := eng.Certify(context.Background(), c.files)
	if cert.Outcome != c.outcome {
		t.Fatalf("Outcome = %q, want %q — the case no longer exercises what it names", cert.Outcome, c.outcome)
	}
	return cert
}

// TestProseAndFieldsNeverDisagree is the regression for the shipped contradiction.
//
// Whenever the rendered markdown disclaims — in either of the two wordings the renderer owns —
// every field that could be mistaken for approval must agree with it. Not one of them, all of
// them: the failure was that Applicable already said false while Grade and AIGateStatus said A
// and PASS, so a test asserting a single field would have passed throughout.
func TestProseAndFieldsNeverDisagree(t *testing.T) {
	t.Parallel()

	for _, c := range proseCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cert := certifyProseCase(t, c)
			out := renderTo(t, render.FormatMarkdown, cert)

			disclaims := strings.Contains(out, render.DisclaimerNoCandidates) ||
				strings.Contains(out, render.DisclaimerUnsupportedContent)

			if !disclaims {
				return
			}

			if cert.Grade == domain.GradeA {
				t.Errorf("the prose disclaims and Grade is A:\n%s", out)
			}
			if cert.AIGateStatus == domain.GatePass {
				t.Errorf("the prose disclaims and AIGateStatus is PASS:\n%s", out)
			}
			if cert.EffectiveGrade == domain.GradeA {
				t.Errorf("the prose disclaims and EffectiveGrade is A:\n%s", out)
			}
			if cert.Applicable {
				t.Errorf("the prose disclaims and Applicable is true:\n%s", out)
			}

			// The rendered page itself, not only the struct. A reader scanning a pull request
			// sees the badge, and a badge that says PASS beside a disclaimer is the whole bug.
			if strings.Contains(out, "PASS") {
				t.Errorf("the prose disclaims and the rendered certificate still shows PASS:\n%s", out)
			}
		})
	}
}

// The converse: a certificate that does pass must not carry a disclaimer either. The two halves
// together say the document has exactly one opinion.
func TestAPassingCertificateNeverDisclaims(t *testing.T) {
	t.Parallel()

	for _, c := range proseCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cert := certifyProseCase(t, c)
			if cert.AIGateStatus != domain.GatePass {
				return
			}

			out := renderTo(t, render.FormatMarkdown, cert)
			for _, disclaimer := range []string{render.DisclaimerNoCandidates, render.DisclaimerUnsupportedContent} {
				if strings.Contains(out, disclaimer) {
					t.Errorf("a PASS certificate carries a disclaimer:\n%s", out)
				}
			}
		})
	}
}

// Every renderer, not only the one a human reads. A merge bot parses the JSON and a code
// scanner parses the SARIF, and neither of them reads prose at all — so the fields have to hold
// the line on their own.
func TestNoRendererReportsAPassForAnUnanalyzedChangeset(t *testing.T) {
	t.Parallel()

	for _, c := range proseCases() {
		if c.outcome == domain.OutcomeAnalyzed {
			continue
		}

		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cert := certifyProseCase(t, c)

			for _, format := range render.Formats() {
				out := renderTo(t, format, cert)

				if strings.Contains(out, `"PASS"`) || strings.Contains(out, "✅ PASS") {
					t.Errorf("%s reports PASS for an unanalyzed changeset:\n%s", format, out)
				}
				if strings.Contains(out, `"grade": "A"`) {
					t.Errorf("%s reports grade A for an unanalyzed changeset:\n%s", format, out)
				}
			}
		})
	}
}
