// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
)

// FuzzCertify drives the whole pipeline — both analyzers, the scorer, the undo plan, the digest —
// with arbitrary bytes, and asserts the invariants that make a certificate trustworthy.
//
// The one that matters most is the last: a certificate may never report a passing grade while
// carrying a finding that says the change cannot be undone.
func FuzzCertify(f *testing.F) {
	seeds := []struct{ sql, manifest string }{
		{"", ""},
		{"DROP TABLE t;", "kind: Namespace\n"},
		{"CREATE INDEX CONCURRENTLY i ON t (c);", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"},
		{"ALTER TABLE t ALTER COLUMN c TYPE integer;", "kind: Deployment\nspec:\n replicas: [3\n"},
		{"GRANT SELECT ON t TO r;", "apiVersion: acme.io/v1\nkind: Flurble\nmetadata:\n  name: f\n"},
		{";;;;", "---\n---\n"},
		{"\x00\xff", "\x00\xff"},
		{"SELECT $$ DROP TABLE x $$;", "kind: ConfigMap\ndata:\n  x: |\n    ---\n"},
	}

	for _, seed := range seeds {
		f.Add(seed.sql, seed.manifest)
	}

	e := realEngine()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, sql, manifest string) {
		files := []domain.ChangedFile{
			{Path: "migrations/0001.up.sql", Status: domain.StatusAdded, Current: []byte(sql)},
			{Path: "k8s/manifest.yaml", Status: domain.StatusModified, Previous: []byte(manifest), Current: []byte(manifest)},
		}

		// Certify owns the recover boundary, so it must return rather than panic whatever the
		// input does.
		cert, _ := e.Certify(ctx, files)

		if !cert.Grade.Valid() {
			t.Fatalf("Grade = %q is not a valid grade", cert.Grade)
		}
		if cert.AIGateStatus != cert.Grade.Gate(domain.GateConditions{Coverage: cert.Coverage, PolicyIgnored: len(cert.IgnoredByPolicy)}) {
			t.Errorf("gate %q disagrees with grade %q", cert.AIGateStatus, cert.Grade)
		}
		if cert.SchemaVersion != domain.SchemaVersion {
			t.Errorf("SchemaVersion = %q", cert.SchemaVersion)
		}
		if len(cert.InputDigest) != 64 {
			t.Errorf("InputDigest = %q, want 64 hex characters", cert.InputDigest)
		}

		// Nil slices serialize as null and break byte-identical output.
		if cert.Findings == nil || cert.UndoPlan == nil || cert.Blockers == nil || cert.DownMigrations == nil {
			t.Error("certificate contains a nil slice")
		}

		// THE INVARIANT. A change the engine could not reverse or could not understand must
		// never sit inside a passing certificate. If this ever fires, the product is broken.
		for _, finding := range cert.Findings {
			switch finding.Reversibility {
			case domain.ReversibilityIrreversible, domain.ReversibilityUnknown:
				if cert.Grade != domain.GradeF {
					t.Fatalf("grade %q with a %s finding (%s)", cert.Grade, finding.Reversibility, finding.RuleID)
				}
				if cert.AIGateStatus == domain.GatePass {
					t.Fatalf("the AI gate passed with a %s finding (%s)", finding.Reversibility, finding.RuleID)
				}
			}

			if !finding.Reversibility.Valid() {
				t.Errorf("%s has an invalid verdict %q", finding.RuleID, finding.Reversibility)
			}
			if !finding.LockHazard.Valid() {
				t.Errorf("%s has an invalid lock hazard %q", finding.RuleID, finding.LockHazard)
			}
		}

		// Grade F must always be explained; nothing else may carry blockers.
		if cert.Grade == domain.GradeF && len(cert.Blockers) == 0 {
			t.Error("grade F with no blockers")
		}
		if cert.Grade != domain.GradeF && len(cert.Blockers) > 0 {
			t.Errorf("grade %q carries blockers", cert.Grade)
		}

		// The certificate must survive serialization, since that is how every consumer sees it.
		if _, err := json.Marshal(cert); err != nil {
			t.Errorf("certificate does not marshal: %v", err)
		}
	})
}

// FuzzCertifyIsDeterministic asserts that two runs over the same arbitrary input agree exactly.
//
// Determinism is easy to hold for curated fixtures and easy to lose on the messy paths, which is
// where map iteration and unsorted slices hide.
func FuzzCertifyIsDeterministic(f *testing.F) {
	f.Add("DROP TABLE a;\nDROP TABLE b;\nTRUNCATE c;")
	f.Add("CREATE TABLE a (id bigint);\nALTER TABLE a ALTER COLUMN id TYPE integer;")
	f.Add("")
	f.Add("\x00\xff\xfe")

	e := realEngine()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, sql string) {
		files := []domain.ChangedFile{
			{Path: "migrations/0002_b.up.sql", Status: domain.StatusAdded, Current: []byte(sql)},
			{Path: "migrations/0001_a.up.sql", Status: domain.StatusAdded, Current: []byte(sql)},
		}

		first, _ := e.Certify(ctx, files)
		second, _ := e.Certify(ctx, files)

		if hashCertificate(t, first) != hashCertificate(t, second) {
			t.Fatalf("two runs over identical input produced different certificates")
		}

		// Reversing the input order must not change anything either: order is an accident of
		// the provider, not a property of the change.
		reversed := []domain.ChangedFile{files[1], files[0]}
		if hashCertificate(t, first) != hashCertificate(t, mustCertify(ctx, t, e, reversed)) {
			t.Fatalf("certificate depends on the order files arrived in")
		}
	})
}

func mustCertify(ctx context.Context, t *testing.T, e *engine.Engine, files []domain.ChangedFile) domain.ReversibilityCertificate {
	t.Helper()
	cert, _ := e.Certify(ctx, files)
	return cert
}

func hashCertificate(t *testing.T, cert domain.ReversibilityCertificate) string {
	t.Helper()

	encoded, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
