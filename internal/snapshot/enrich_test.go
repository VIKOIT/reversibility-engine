// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// collectedAt of every fixture, so staleness is a decision of the test rather than of the date.
var snapshotDay = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func loadSet(t *testing.T, files ...string) *snapshot.Set {
	t.Helper()

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, filepath.Join("testdata", f))
	}

	set, err := snapshot.Load(paths, snapshot.Options{Now: snapshotDay})
	if err != nil {
		t.Fatalf("Load(%v): %v", files, err)
	}
	if set == nil {
		t.Fatalf("Load(%v) returned no context", files)
	}
	return set
}

func finding(rule, relation, object string, rev domain.Reversibility, lock domain.LockHazard) domain.Finding {
	return domain.Finding{
		RuleID:        rule,
		File:          "migrations/0001.up.sql",
		Line:          1,
		Reversibility: rev,
		LockHazard:    lock,
		Rationale:     "as classified",
		Subject:       domain.Subject{Relation: relation, Object: object},
	}
}

func enrichOne(t *testing.T, set *snapshot.Set, f domain.Finding) *domain.FindingContext {
	t.Helper()

	out := set.Enrich([]domain.Finding{f})
	if len(out) != 1 {
		t.Fatalf("Enrich returned %d findings, want 1", len(out))
	}
	return out[0].Context
}

// THE ONE-WAY RATCHET. Enrichment may make a finding more severe and may never make one less
// severe, so no snapshot — of any database, in any state — can improve a verdict.
//
// The lock hazard is stricter still: context describes how long a lock is held, never which lock
// is taken, so it must come through untouched in both directions.
func TestEnrichmentOnlyEverRaisesSeverity(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json", "k8s.json")

	findings := []domain.Finding{
		finding("PG006", "orders", "total", domain.ReversibilityIrreversible, domain.LockTableRewrite),
		finding("PG007", "orders", "total", domain.ReversibilityCostly, domain.LockTableRewrite),
		finding("PG017", "orders", "shipped_at", domain.ReversibilityCostly, domain.LockFullScan),
		finding("PG017", "orders", "id", domain.ReversibilityCostly, domain.LockFullScan),
		finding("PG014", "", "idx_orders_status", domain.ReversibilityCostly, domain.LockExclusive),
		finding("PG021", "orders", "orders_fk", domain.ReversibilityCostly, domain.LockFullScan),
		finding("K8S003", "shop/orders-data", "PersistentVolumeClaim", domain.ReversibilityIrreversible, domain.LockNone),
		finding("K8S004", "shop/orders-data", "PersistentVolumeClaim", domain.ReversibilityIrreversible, domain.LockNone),
		finding("PG001", "orders", "", domain.ReversibilityIrreversible, domain.LockExclusive),
	}

	enriched := set.Enrich(findings)

	for i, got := range enriched {
		want := findings[i]
		if got.Reversibility.Severity() < want.Reversibility.Severity() {
			t.Errorf("%s: enrichment weakened %q to %q", want.RuleID, want.Reversibility, got.Reversibility)
		}
		if got.LockHazard != want.LockHazard {
			t.Errorf("%s: LockHazard became %q, was %q", want.RuleID, got.LockHazard, want.LockHazard)
		}
		if got.RuleID != want.RuleID || got.File != want.File || got.Line != want.Line {
			t.Errorf("%s: identity changed: %+v", want.RuleID, got)
		}
	}

	// Exactly one of these is expected to move, and it is the one with nulls in production.
	if enriched[2].Reversibility != domain.ReversibilityWillFail {
		t.Errorf("SET NOT NULL against a column with nulls stayed %q, want WILL_FAIL", enriched[2].Reversibility)
	}
	if enriched[3].Reversibility != domain.ReversibilityCostly {
		t.Errorf("SET NOT NULL against a clean column became %q, want it unchanged", enriched[3].Reversibility)
	}
}

// A band never exists without a snapshot, and never for a lock whose cost does not scale with
// size. That second half is what keeps a two-gigabyte index drop from being reported as an
// OUTAGE for an operation that takes milliseconds.
func TestBandsOnlyExistWhereDurationScalesWithSize(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json")

	tests := []struct {
		name string
		in   domain.Finding
		want domain.LockDurationBand
	}{
		{
			name: "a table rewrite is banded",
			in:   finding("PG006", "orders", "total", domain.ReversibilityIrreversible, domain.LockTableRewrite),
			want: domain.BandOutage,
		},
		{
			name: "a full scan is banded",
			in:   finding("PG021", "orders", "c", domain.ReversibilityCostly, domain.LockFullScan),
			want: domain.BandDisruptive,
		},
		{
			// EXCLUSIVE is above FULL_SCAN in severity, but dropping an index is not slower for
			// being large, and there is no rate defined for it.
			name: "an exclusive index drop is not banded",
			in:   finding("PG014", "orders", "idx_orders_status", domain.ReversibilityCostly, domain.LockExclusive),
			want: "",
		},
		{
			name: "a lock below FULL_SCAN is not banded",
			in:   finding("PG015", "orders", "idx_orders_hot", domain.ReversibilityCostly, domain.LockNone),
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := domain.LockDurationBand("")
			if c := enrichOne(t, set, tc.in); c != nil {
				got = c.LockDurationBand
			}
			if got != tc.want {
				t.Errorf("band = %q, want %q", got, tc.want)
			}
		})
	}
}

// The rules that gain context are exactly the ones the session specified. A rule enriched by
// accident would be an interpretation nobody agreed to.
func TestOnlySpecifiedRulesGainContext(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json", "k8s.json")

	enriched := map[string]bool{
		"PG006": true, "PG007": true, "PG014": true, "PG015": true,
		"PG017": true, "PG021": true, "K8S003": true, "K8S004": true,
	}

	// Rules that plausibly look enrichable and deliberately are not.
	for _, rule := range []string{"PG001", "PG002", "PG005", "PG023", "PG024", "K8S001", "K8S006", "K8S008"} {
		f := finding(rule, "orders", "shipped_at", domain.ReversibilityCostly, domain.LockNone)
		if c := enrichOne(t, set, f); c != nil {
			t.Errorf("%s gained context %+v; only %v are specified", rule, c, keys(enriched))
		}
	}
}

func TestPostgresEnrichment(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json")

	tests := []struct {
		name      string
		finding   domain.Finding
		wantRows  int64
		wantBytes int64
		mustSay   []string
		mustNotBe string
	}{
		{
			name:      "a type change reports the whole table",
			finding:   finding("PG006", "orders", "total", domain.ReversibilityIrreversible, domain.LockTableRewrite),
			wantRows:  212000000,
			wantBytes: 34359738368,
			mustSay:   []string{"212M", "32.0 GiB", "48.0 GiB including indexes", "Rewrites the whole of orders"},
		},
		{
			// The highest-value single check in the session: a fact the database already knows,
			// turned into a sentence before the migration runs rather than after it fails.
			name:     "SET NOT NULL on a column with nulls reports what has to be backfilled",
			finding:  finding("PG017", "orders", "shipped_at", domain.ReversibilityCostly, domain.LockFullScan),
			wantRows: 212000000,
			mustSay:  []string{"Confirmed against production", "31%", "backfill"},
		},
		{
			name:      "SET NOT NULL on a clean column says the constraint should validate",
			finding:   finding("PG017", "orders", "id", domain.ReversibilityCostly, domain.LockFullScan),
			wantRows:  212000000,
			mustSay:   []string{"no nulls"},
			mustNotBe: "WILL FAIL",
		},
		{
			// A fraction that rounds to zero still fails the migration. Reporting it as 0% would
			// be the single most misleading thing this package could print.
			name:    "a vanishingly small null fraction is still reported, never rounded away",
			finding: finding("PG017", "orders", "rare_null", domain.ReversibilityCostly, domain.LockFullScan),
			mustSay: []string{"<0.01%"},
		},
		{
			name:      "an unused index is reported as genuinely cheap to drop",
			finding:   finding("PG014", "orders", "idx_orders_status", domain.ReversibilityCostly, domain.LockExclusive),
			wantBytes: 2147483648,
			mustSay:   []string{"not used index idx_orders_status once", "2.0 GiB", "2026-02-01"},
		},
		{
			name:      "a hot index is reported as changing query plans",
			finding:   finding("PG015", "orders", "idx_orders_hot", domain.ReversibilityCostly, domain.LockNone),
			wantBytes: 1073741824,
			mustSay:   []string{"98K times", "change query plans"},
		},
		{
			name:     "constraint validation reports the scan",
			finding:  finding("PG021", "orders", "orders_total_check", domain.ReversibilityCostly, domain.LockFullScan),
			wantRows: 212000000,
			mustSay:  []string{"Validating this constraint scans", "212M"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := enrichOne(t, set, tc.finding)
			if got == nil {
				t.Fatalf("no context was attached to %s", tc.finding.RuleID)
			}

			if tc.wantRows != 0 && got.RowEstimate != tc.wantRows {
				t.Errorf("RowEstimate = %d, want %d", got.RowEstimate, tc.wantRows)
			}
			if tc.wantBytes != 0 && got.SizeBytes != tc.wantBytes {
				t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, tc.wantBytes)
			}
			for _, want := range tc.mustSay {
				if !strings.Contains(got.ContextNote, want) {
					t.Errorf("note does not mention %q:\n%s", want, got.ContextNote)
				}
			}
			if tc.mustNotBe != "" && strings.Contains(got.ContextNote, tc.mustNotBe) {
				t.Errorf("note wrongly says %q:\n%s", tc.mustNotBe, got.ContextNote)
			}
		})
	}
}

// Every duration this package prints carries a tilde. A number a reader remembers as a
// measurement is worse than no number.
func TestDurationsAreAlwaysMarkedAsEstimates(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json")

	// PG017 is represented by its clean column: one with nulls becomes WILL_FAIL and carries no
	// duration at all, which is asserted separately.
	for _, f := range []domain.Finding{
		finding("PG006", "orders", "total", domain.ReversibilityIrreversible, domain.LockTableRewrite),
		finding("PG017", "orders", "id", domain.ReversibilityCostly, domain.LockFullScan),
		finding("PG021", "orders", "c", domain.ReversibilityCostly, domain.LockFullScan),
	} {
		c := enrichOne(t, set, f)
		if c == nil || c.EstimatedLockDuration == "" {
			t.Fatalf("%s produced no duration estimate", f.RuleID)
		}
		if !strings.HasPrefix(c.EstimatedLockDuration, "~") {
			t.Errorf("%s: duration %q is not marked as an estimate", f.RuleID, c.EstimatedLockDuration)
		}
	}
}

func TestKubernetesEnrichment(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "k8s.json")

	t.Run("a Delete policy is confirmed against the cluster", func(t *testing.T) {
		t.Parallel()

		c := enrichOne(t, set, finding("K8S003", "shop/orders-data", "PersistentVolumeClaim",
			domain.ReversibilityIrreversible, domain.LockNone))
		if c == nil {
			t.Fatal("no context attached")
		}
		for _, want := range []string{"Confirmed against the cluster", "fast-ssd", "Delete", "100Gi"} {
			if !strings.Contains(c.ContextNote, want) {
				t.Errorf("note does not mention %q:\n%s", want, c.ContextNote)
			}
		}
	})

	// The case the session brief calls out by name: Retain is materially less severe, and the
	// finding stands anyway. Recording the fact without moving the grade is the no-downgrade
	// rule doing exactly what it exists for.
	t.Run("a Retain policy is recorded without improving anything", func(t *testing.T) {
		t.Parallel()

		in := finding("K8S003", "shop/archive-data", "PersistentVolumeClaim",
			domain.ReversibilityIrreversible, domain.LockNone)

		out := set.Enrich([]domain.Finding{in})
		if out[0].Reversibility != domain.ReversibilityIrreversible {
			t.Errorf("Reversibility = %q, want IRREVERSIBLE; a Retain policy must not downgrade a finding",
				out[0].Reversibility)
		}
		if out[0].Context == nil || !strings.Contains(out[0].Context.ContextNote, "Retain") {
			t.Fatalf("the Retain policy was not recorded: %+v", out[0].Context)
		}
		if !strings.Contains(out[0].Context.ContextNote, "The finding stands") {
			t.Errorf("the note does not explain why the finding stands:\n%s", out[0].Context.ContextNote)
		}
	})

	t.Run("a storage decrease is measured against the bound volume", func(t *testing.T) {
		t.Parallel()

		c := enrichOne(t, set, finding("K8S004", "shop/orders-data", "PersistentVolumeClaim",
			domain.ReversibilityIrreversible, domain.LockNone))
		if c == nil || !strings.Contains(c.ContextNote, "100Gi") {
			t.Fatalf("the bound capacity was not reported: %+v", c)
		}
	})
}

// Context that names the wrong object is worse than no context, because context is believed.
// An unqualified name matching two schemas, or a claim name matching two namespaces, resolves to
// nothing at all rather than to a guess.
func TestAmbiguousSubjectsAreRefused(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json", "k8s.json")

	t.Run("a table name in two schemas", func(t *testing.T) {
		t.Parallel()

		if c := enrichOne(t, set, finding("PG006", "ambiguous", "col",
			domain.ReversibilityIrreversible, domain.LockTableRewrite)); c != nil {
			t.Errorf("an ambiguous table was resolved to %+v", c)
		}

		// Qualifying it removes the ambiguity, so the same lookup then succeeds.
		if c := enrichOne(t, set, finding("PG006", "archive.ambiguous", "col",
			domain.ReversibilityIrreversible, domain.LockTableRewrite)); c == nil {
			t.Error("a schema-qualified table was not resolved")
		}
	})

	t.Run("a claim name in two namespaces", func(t *testing.T) {
		t.Parallel()

		if c := enrichOne(t, set, finding("K8S004", "orders-data", "PersistentVolumeClaim",
			domain.ReversibilityIrreversible, domain.LockNone)); c != nil {
			t.Errorf("an unnamespaced claim was resolved to %+v", c)
		}
	})

	t.Run("an object that is not in the snapshot at all", func(t *testing.T) {
		t.Parallel()

		if c := enrichOne(t, set, finding("PG006", "no_such_table", "col",
			domain.ReversibilityIrreversible, domain.LockTableRewrite)); c != nil {
			t.Errorf("a table absent from the snapshot was resolved to %+v", c)
		}
	})
}

// A nil set is the no-context case and must behave exactly as the engine did before snapshots
// existed, rather than force a branch on every caller.
func TestNilSetIsInert(t *testing.T) {
	t.Parallel()

	var set *snapshot.Set

	in := []domain.Finding{finding("PG006", "orders", "total", domain.ReversibilityIrreversible, domain.LockTableRewrite)}
	out := set.Enrich(in)

	if len(out) != 1 || out[0].Context != nil {
		t.Errorf("a nil context set enriched a finding: %+v", out)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
