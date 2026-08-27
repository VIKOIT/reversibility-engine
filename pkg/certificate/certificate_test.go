// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package certificate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

func TestFromDomainCarriesEveryField(t *testing.T) {
	t.Parallel()

	in := domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeC,
		EffectiveGrade: domain.GradeC,
		AIGateStatus:   domain.GateFail,
		Applicable:     true,
		InputDigest:    "deadbeef",
		Findings: []domain.Finding{{
			RuleID:        "PG012",
			File:          "migrations/0001.up.sql",
			Line:          7,
			Statement:     "ALTER TABLE orders RENAME TO purchase_orders",
			Reversibility: domain.ReversibilityCostly,
			LockHazard:    domain.LockShort,
			Rationale:     "a rename breaks the previous application version",
			UndoStep:      "ALTER TABLE purchase_orders RENAME TO orders;",
		}},
		UndoPlan: []domain.UndoStep{"ALTER TABLE purchase_orders RENAME TO orders;"},
		Blockers: []string{"a blocker"},
		DownMigrations: []domain.DownMigrationStatus{{
			Migration:     "0001",
			UpFile:        "migrations/0001.up.sql",
			DownFile:      "migrations/0001.down.sql",
			Exists:        true,
			Parses:        true,
			Symmetric:     false,
			SymmetryNotes: []string{"up creates TABLE x but down never drops it"},
		}},
	}

	got := certificate.FromDomain(in)

	want := certificate.Certificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          certificate.GradeC,
		AIGateStatus:   certificate.GateFail,
		Applicable:     true,
		EffectiveGrade: certificate.GradeC,
		InputDigest:    "deadbeef",
		Findings: []certificate.Finding{{
			RuleID:        "PG012",
			File:          "migrations/0001.up.sql",
			Line:          7,
			Statement:     "ALTER TABLE orders RENAME TO purchase_orders",
			Reversibility: certificate.Costly,
			LockHazard:    certificate.LockShort,
			Rationale:     "a rename breaks the previous application version",
			UndoStep:      "ALTER TABLE purchase_orders RENAME TO orders;",
		}},
		Waived:   []certificate.WaivedFinding{},
		UndoPlan: []string{"ALTER TABLE purchase_orders RENAME TO orders;"},
		Blockers: []string{"a blocker"},
		DownMigrations: []certificate.DownMigrationStatus{{
			Migration:     "0001",
			UpFile:        "migrations/0001.up.sql",
			DownFile:      "migrations/0001.down.sql",
			Exists:        true,
			Parses:        true,
			Symmetric:     false,
			SymmetryNotes: []string{"up creates TABLE x but down never drops it"},
		}},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FromDomain lost or altered a field (-want +got):\n%s", diff)
	}
}

// A consumer ranging over these must not have to nil-check. encoding/json renders nil as null,
// which also breaks byte-identical output for two certificates of identical meaning.
func TestFromDomainNormalizesNilSlices(t *testing.T) {
	t.Parallel()

	got := certificate.FromDomain(domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion,
		Grade:         domain.GradeA,
		AIGateStatus:  domain.GatePass,
	})

	if got.Findings == nil || got.UndoPlan == nil || got.Blockers == nil || got.DownMigrations == nil {
		t.Fatal("a nil slice survived conversion")
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Errorf("serialized form contains null: %s", encoded)
	}
}

// Passed is the single definition of the gate for external consumers. One definition means one
// chance to get it wrong, not one per consumer.
func TestPassed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status certificate.GateStatus
		want   bool
	}{
		{certificate.GatePass, true},
		{certificate.GateFail, false},
		{"", false},
		{"pass", false},
		{"PASSED", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			cert := certificate.Certificate{AIGateStatus: tt.status}
			if got := cert.Passed(); got != tt.want {
				t.Errorf("Passed() = %v for status %q, want %v", got, tt.status, tt.want)
			}
		})
	}
}

// The zero value must not pass. A consumer that failed to populate a certificate — a truncated
// download, a failed decode — must not read as an approval.
func TestZeroCertificateDoesNotPass(t *testing.T) {
	t.Parallel()

	var cert certificate.Certificate
	if cert.Passed() {
		t.Error("a zero-value certificate passed the gate")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := certificate.FromDomain(domain.ReversibilityCertificate{
		SchemaVersion:  domain.SchemaVersion,
		Grade:          domain.GradeF,
		AIGateStatus:   domain.GateFail,
		Applicable:     true,
		InputDigest:    "abc123",
		Findings:       []domain.Finding{{RuleID: "PG001", File: "a.sql", Line: 1, Reversibility: domain.ReversibilityIrreversible, LockHazard: domain.LockExclusive, Rationale: "r"}},
		UndoPlan:       []domain.UndoStep{"-- none"},
		Blockers:       []string{"b"},
		DownMigrations: []domain.DownMigrationStatus{},
	})

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	var decoded certificate.Certificate
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	if diff := cmp.Diff(original, decoded); diff != "" {
		t.Errorf("certificate changed across a JSON round trip (-original +decoded):\n%s", diff)
	}
}

// The JSON field names are the public contract. Renaming one is a breaking change that must be
// accompanied by a SchemaVersion bump, so it should require editing this test deliberately.
func TestWireFieldNames(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(certificate.FromDomain(domain.ReversibilityCertificate{
		SchemaVersion: domain.SchemaVersion,
		Findings:      []domain.Finding{{RuleID: "PG001", File: "a.sql", Line: 1}},
	}))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	for _, field := range []string{
		`"schemaVersion"`, `"grade"`, `"aiGateStatus"`, `"applicable"`, `"inputDigest"`,
		`"findings"`, `"undoPlan"`, `"blockers"`, `"downMigrations"`,
		`"ruleId"`, `"file"`, `"line"`, `"statement"`, `"reversibility"`, `"lockHazard"`, `"rationale"`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("wire format is missing %s: %s", field, encoded)
		}
	}
}

func TestSchemaVersionIsPinned(t *testing.T) {
	t.Parallel()

	// Downstream merge gates pin this. A change here is a deliberate, breaking act.
	//
	// 1.5.0 added Outcome, plus N/A to Grade and NOT_APPLICABLE to GateStatus. A consumer
	// testing `grade == "A"` is unaffected; one testing `grade != "F"` now passes changesets
	// nobody analyzed, which is why the bump is a minor rather than a patch.
	if certificate.SchemaVersion != "1.5.0" {
		t.Errorf("SchemaVersion = %q, want 1.5.0", certificate.SchemaVersion)
	}
}

// The public schema must carry the outcome, because it is the field a consumer branches on and
// the only one that separates "nothing here to check" from "something here I could not check".
func TestPublicSchemaCarriesTheOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome  domain.AnalysisOutcome
		assessed bool
	}{
		{domain.OutcomeAnalyzed, true},
		{domain.OutcomeNoCandidates, false},
		{domain.OutcomeUnsupportedContent, false},
		{domain.AnalysisOutcome(""), false},
	} {
		got := certificate.FromDomain(domain.ReversibilityCertificate{Outcome: tc.outcome})

		if string(got.Outcome) != string(tc.outcome) {
			t.Errorf("Outcome = %q, want %q", got.Outcome, tc.outcome)
		}
		if got.Assessed() != tc.assessed {
			t.Errorf("Assessed() = %v for outcome %q, want %v", got.Assessed(), tc.outcome, tc.assessed)
		}

		// Passed and Assessed answer different questions and must never be conflated: a
		// certificate can be assessed and fail, but it can never pass without being assessed.
		if got.Passed() && !got.Assessed() {
			t.Errorf("outcome %q passes the gate without having been assessed", tc.outcome)
		}
	}
}
