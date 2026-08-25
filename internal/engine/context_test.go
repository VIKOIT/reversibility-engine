// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// contextDay is fixed so a snapshot's age is a decision of the test rather than of the date.
var contextDay = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

// productionContext builds a snapshot deliberately shaped to touch as many fixtures as possible:
// every table and index name the Postgres fixtures use, with enormous sizes, plus a column with
// nulls. If context could move a grade, this is the input that would do it.
func productionContext(t *testing.T) *snapshot.Set {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "pg.json")

	body := `{
  "schemaVersion": "1.0.0",
  "kind": "postgres",
  "collectedAt": "2026-08-25T00:00:00Z",
  "sourceFingerprint": "fixturefixturefixturefixturefixturefixturefixturefixturefixture0",
  "postgres": {
    "tables": [
      {"schema":"public","name":"orders","rowEstimate":900000000,"sizeBytes":1099511627776,"totalSizeBytes":2199023255552},
      {"schema":"public","name":"legacy_orders","rowEstimate":900000000,"sizeBytes":1099511627776,"totalSizeBytes":2199023255552},
      {"schema":"public","name":"users","rowEstimate":900000000,"sizeBytes":1099511627776,"totalSizeBytes":2199023255552},
      {"schema":"public","name":"events","rowEstimate":900000000,"sizeBytes":1099511627776,"totalSizeBytes":2199023255552},
      {"schema":"public","name":"accounts","rowEstimate":900000000,"sizeBytes":1099511627776,"totalSizeBytes":2199023255552}
    ],
    "indexes": [
      {"schema":"public","table":"orders","name":"idx","sizeBytes":549755813888,"scans":0},
      {"schema":"public","table":"orders","name":"idx_orders_status","sizeBytes":549755813888,"scans":0}
    ],
    "columns": [
      {"schema":"public","table":"orders","name":"status","nullFraction":0.9,"averageWidth":8},
      {"schema":"public","table":"users","name":"email","nullFraction":0.9,"averageWidth":32},
      {"schema":"public","table":"accounts","name":"email","nullFraction":0.9,"averageWidth":32}
    ]
  }
}`

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the snapshot: %v", err)
	}

	set, err := snapshot.Load([]string{path}, snapshot.Options{Now: contextDay})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

// THE PROPERTY THE SESSION BRIEF DEMANDS, in its strongest form.
//
// Context may never improve a grade. As built it cannot change one at all — enrichment writes
// only the Context field — so this asserts equality rather than an inequality. That is a
// stronger and much easier property to keep: an inequality would still permit a future edit to
// invent a threshold, and equality fails the moment anybody tries.
//
// Every fixture in the repository is run twice, with and without a snapshot sized to make any
// size-sensitive rule fire if one existed.
func TestContextNeverChangesAGradeForAnyFixture(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	files := provider.NewFake(root)
	production := productionContext(t)

	plain := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})
	enriched := engine.New(
		[]analyzer.Analyzer{postgres.New(), kubernetes.New()},
		engine.WithContext(production),
	)

	for _, group := range []string{"postgres", "kubernetes"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, tc := range cases {
			t.Run(group+"/"+tc.Name, func(t *testing.T) {
				t.Parallel()

				changed, err := files.ChangedFiles(context.Background(), tc.Ref)
				if err != nil {
					t.Fatalf("reading the fixture: %v", err)
				}

				before, _ := plain.Certify(context.Background(), changed)
				after, _ := enriched.Certify(context.Background(), changed)

				if after.Grade != before.Grade {
					t.Errorf("context changed the grade: %q without, %q with", before.Grade, after.Grade)
				}
				if after.EffectiveGrade != before.EffectiveGrade {
					t.Errorf("context changed the effective grade: %q without, %q with",
						before.EffectiveGrade, after.EffectiveGrade)
				}
				if after.AIGateStatus != before.AIGateStatus {
					t.Errorf("context changed the AI merge gate: %q without, %q with",
						before.AIGateStatus, after.AIGateStatus)
				}
				if len(after.Findings) != len(before.Findings) {
					t.Errorf("context changed the number of findings: %d without, %d with",
						len(before.Findings), len(after.Findings))
				}

				// The classification of every finding must survive untouched, not just the
				// aggregate. A pair of compensating changes would keep the grade and still be
				// a reclassification.
				for i := range after.Findings {
					if after.Findings[i].Reversibility != before.Findings[i].Reversibility ||
						after.Findings[i].LockHazard != before.Findings[i].LockHazard {
						t.Errorf("context reclassified %s: %s/%s became %s/%s",
							before.Findings[i].RuleID,
							before.Findings[i].Reversibility, before.Findings[i].LockHazard,
							after.Findings[i].Reversibility, after.Findings[i].LockHazard)
					}
				}
			})
		}
	}
}

// Context is an input to the verdict, so it belongs in the digest — and adding one must not
// disturb any digest produced without one.
func TestContextIsHashedIntoTheInputDigest(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	changed, err := provider.NewFake(root).ChangedFiles(context.Background(),
		provider.FixtureRef("postgres", "PG006_alter_type_narrowing"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	plain, _ := engine.New([]analyzer.Analyzer{postgres.New()}).Certify(context.Background(), changed)

	production := productionContext(t)
	withContext, _ := engine.New([]analyzer.Analyzer{postgres.New()},
		engine.WithContext(production)).Certify(context.Background(), changed)

	if withContext.InputDigest == plain.InputDigest {
		t.Error("supplying a snapshot did not change the input digest")
	}

	again, _ := engine.New([]analyzer.Analyzer{postgres.New()},
		engine.WithContext(production)).Certify(context.Background(), changed)
	if again.InputDigest != withContext.InputDigest {
		t.Error("the same snapshot produced two different digests")
	}
}

// The point of the whole session: a rewrite goes from a category to a quantity.
func TestContextMakesAFindingConcrete(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	changed, err := provider.NewFake(root).ChangedFiles(context.Background(),
		provider.FixtureRef("postgres", "PG006_alter_type_narrowing"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	cert, _ := engine.New([]analyzer.Analyzer{postgres.New()},
		engine.WithContext(productionContext(t))).Certify(context.Background(), changed)

	var enriched *domain.Finding
	for i := range cert.Findings {
		if cert.Findings[i].RuleID == "PG006" && cert.Findings[i].Context != nil {
			enriched = &cert.Findings[i]
			break
		}
	}
	if enriched == nil {
		t.Fatalf("no PG006 finding carried context: %+v", cert.Findings)
	}

	if enriched.Context.RowEstimate == 0 {
		t.Error("the finding carries no row estimate")
	}
	if !strings.HasPrefix(enriched.Context.EstimatedLockDuration, "~") {
		t.Errorf("duration %q is not marked as an estimate", enriched.Context.EstimatedLockDuration)
	}
	if !strings.Contains(enriched.Context.ContextNote, "Rewrites the whole of") {
		t.Errorf("the note does not describe the rewrite: %q", enriched.Context.ContextNote)
	}
}

// A stale snapshot is reported on the certificate rather than discarded, and reporting it
// changes nothing about the verdict.
func TestStaleContextWarnsOnTheCertificate(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	changed, err := provider.NewFake(root).ChangedFiles(context.Background(),
		provider.FixtureRef("postgres", "PG006_alter_type_narrowing"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "pg.json")
	if err := os.WriteFile(path, []byte(`{
  "schemaVersion": "1.0.0",
  "kind": "postgres",
  "collectedAt": "2026-01-01T00:00:00Z",
  "sourceFingerprint": "staleeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
  "postgres": {"tables": [], "indexes": [], "columns": []}
}`), 0o644); err != nil {
		t.Fatalf("writing the snapshot: %v", err)
	}

	stale, err := snapshot.Load([]string{path}, snapshot.Options{Now: contextDay})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cert, _ := engine.New([]analyzer.Analyzer{postgres.New()},
		engine.WithContext(stale)).Certify(context.Background(), changed)

	if len(cert.ContextWarnings) != 1 {
		t.Fatalf("ContextWarnings = %v, want one staleness warning", cert.ContextWarnings)
	}
	if !strings.Contains(cert.ContextWarnings[0], "days old") {
		t.Errorf("the warning does not say how old the snapshot is: %q", cert.ContextWarnings[0])
	}

	plain, _ := engine.New([]analyzer.Analyzer{postgres.New()}).Certify(context.Background(), changed)
	if cert.Grade != plain.Grade {
		t.Errorf("a stale snapshot changed the grade: %q, want %q", cert.Grade, plain.Grade)
	}
}
