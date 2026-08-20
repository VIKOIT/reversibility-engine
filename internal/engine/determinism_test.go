// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// certificateHash is the byte-identity check: a certificate that differs anywhere, including in
// slice order or in a nil-versus-empty slice, produces a different hash.
func certificateHash(t *testing.T, cert domain.ReversibilityCertificate) string {
	t.Helper()

	encoded, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshalling certificate: %v", err)
	}

	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// A merge gate whose answer shuffles between runs is a gate nobody trusts. docs/RULES.md §3 makes
// this a hard requirement and specifies the 100-run check.
func TestCertificateIsByteIdenticalAcrossRuns(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	files := provider.NewFake(root)

	// Every fixture in the repository, not one hand-picked case: determinism has to hold for
	// the messy inputs too.
	for _, group := range []string{"postgres", "kubernetes"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, tc := range cases {
			t.Run(group+"/"+tc.Name, func(t *testing.T) {
				t.Parallel()

				ctx := context.Background()

				changed, err := files.ChangedFiles(ctx, tc.Ref)
				if err != nil {
					t.Fatalf("resolving changeset: %v", err)
				}

				// A fresh engine each iteration, so any state accidentally retained between
				// runs would show up as a difference.
				first, _ := realEngine().Certify(ctx, changed)
				want := certificateHash(t, first)

				for i := 0; i < 100; i++ {
					got, _ := realEngine().Certify(ctx, changed)

					if h := certificateHash(t, got); h != want {
						t.Fatalf("run %d produced a different certificate\nSHA256 %s vs %s\ndiff (-first +run):\n%s",
							i, want, h, cmp.Diff(first, got))
					}
				}
			})
		}
	}
}

// File order is an accident of the provider, not a property of the change. Two orderings of the
// same changeset must certify identically, digest included.
func TestCertificateIgnoresInputFileOrder(t *testing.T) {
	t.Parallel()

	forward := []domain.ChangedFile{
		sql("migrations/0001_a.up.sql", "CREATE TABLE a (id bigint);"),
		sql("migrations/0001_a.down.sql", "DROP TABLE a;"),
		sql("migrations/0002_b.up.sql", "CREATE TABLE b (id bigint);"),
		sql("migrations/0002_b.down.sql", "DROP TABLE b;"),
	}

	reversed := make([]domain.ChangedFile, len(forward))
	for i, f := range forward {
		reversed[len(forward)-1-i] = f
	}

	a := certify(t, forward...)
	b := certify(t, reversed...)

	if a.InputDigest != b.InputDigest {
		t.Errorf("digest depends on file order: %s vs %s", a.InputDigest, b.InputDigest)
	}
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("certificate depends on file order (-forward +reversed):\n%s", diff)
	}
}

// A certificate must contain nothing that varies between runs: no timestamps, no run IDs, no
// hostnames. This checks the serialized form, which is what consumers actually see.
func TestCertificateContainsNothingVolatile(t *testing.T) {
	t.Parallel()

	cert := certify(t,
		sql("migrations/0001.up.sql", "DROP TABLE t;"),
		sql("migrations/0001.down.sql", "CREATE TABLE t (id bigint);"),
	)

	encoded, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	for _, forbidden := range []string{"timestamp", "generatedAt", "createdAt", "runId", "uuid", "hostname", "duration"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Errorf("certificate contains volatile field %q: %s", forbidden, encoded)
		}
	}
}

// PROPERTY: adding a statement to a changeset can only lower or hold the grade, never raise it.
//
// More change means more risk, never less. A tool that could be made to look safer by adding to
// a pull request would be trivially gameable — by a human under deadline pressure, or by an
// autonomous agent optimizing for a green gate.
//
// Statements are appended, not prepended. Order is meaningful: a CREATE TABLE placed *before* an
// ALTER COLUMN TYPE legitimately improves the verdict by supplying the prior type the classifier
// needs (CLAUDE.md §16.3). Appending cannot retroactively explain an earlier statement.
func TestAddingAStatementNeverRaisesTheGrade(t *testing.T) {
	t.Parallel()

	bases := []string{
		"CREATE INDEX CONCURRENTLY idx ON orders (status);",
		"ALTER TABLE orders ADD COLUMN notes text;",
		"ALTER TABLE orders RENAME TO purchase_orders;",
		"ALTER TABLE orders DROP CONSTRAINT c;",
		"CREATE TABLE orders (id bigint PRIMARY KEY, quantity bigint NOT NULL);",
		"DROP TABLE legacy;",
		"GRANT SELECT ON orders TO ro;",
	}

	// One statement per rule family, spanning every reversibility class.
	additions := []string{
		"CREATE INDEX CONCURRENTLY idx2 ON orders (created_at);",
		"ALTER TABLE orders ADD COLUMN extra text;",
		"ALTER TABLE orders ADD COLUMN v uuid DEFAULT gen_random_uuid();",
		"ALTER TABLE orders ADD CONSTRAINT c2 CHECK (total >= 0);",
		"ALTER TABLE orders ADD CONSTRAINT c3 CHECK (total >= 0) NOT VALID;",
		"CREATE INDEX idx3 ON orders (region);",
		"ALTER TABLE orders ALTER COLUMN region SET NOT NULL;",
		"ALTER TABLE orders RENAME COLUMN a TO b;",
		"DROP INDEX idx_old;",
		"DROP INDEX CONCURRENTLY idx_old2;",
		"DROP VIEW v;",
		"ALTER TABLE orders ADD COLUMN t uuid NOT NULL;",
		"DROP TABLE other;",
		"TRUNCATE audit_log;",
		"DELETE FROM sessions;",
		"DELETE FROM sessions WHERE a < now();",
		"DROP SCHEMA reporting;",
		"DROP TABLE payments CASCADE;",
		"ALTER SEQUENCE s RESTART WITH 1;",
		"VACUUM FULL orders;",
	}

	e := realEngine()
	ctx := context.Background()

	// A property test over inputs that all grade the same proves nothing. This records the
	// spread so the test cannot rot into a tautology if the rules change.
	seenGrades := map[domain.Grade]bool{}

	for _, base := range bases {
		baseFiles := []domain.ChangedFile{
			sql("migrations/0001.up.sql", base),
			sql("migrations/0001.down.sql", "SELECT 1;"),
		}

		baseCert, _ := e.Certify(ctx, baseFiles)
		seenGrades[baseCert.Grade] = true

		for _, addition := range additions {
			extended := []domain.ChangedFile{
				sql("migrations/0001.up.sql", base+"\n"+addition),
				sql("migrations/0001.down.sql", "SELECT 1;"),
			}

			extendedCert, _ := e.Certify(ctx, extended)

			if extendedCert.Grade.Rank() > baseCert.Grade.Rank() {
				t.Errorf("appending a statement raised the grade from %s to %s\n  base:     %s\n  addition: %s",
					baseCert.Grade, extendedCert.Grade, base, addition)
			}
		}
	}

	if len(seenGrades) < 3 {
		t.Errorf("the base changesets produced only %d distinct grades (%v); the property is not being exercised across the range",
			len(seenGrades), seenGrades)
	}
}

// The same property at the file level: adding a whole migration cannot improve the verdict.
func TestAddingAFileNeverRaisesTheGrade(t *testing.T) {
	t.Parallel()

	e := realEngine()
	ctx := context.Background()

	base := []domain.ChangedFile{
		sql("migrations/0001_a.up.sql", "CREATE INDEX CONCURRENTLY i ON t (c);"),
		sql("migrations/0001_a.down.sql", "DROP INDEX CONCURRENTLY i;"),
	}

	baseCert, _ := e.Certify(ctx, base)
	if baseCert.Grade != domain.GradeA {
		t.Fatalf("base grade = %q, want A", baseCert.Grade)
	}

	additions := []struct {
		name  string
		files []domain.ChangedFile
	}{
		{"a safe migration", []domain.ChangedFile{
			sql("migrations/0002_b.up.sql", "CREATE INDEX CONCURRENTLY j ON t (d);"),
			sql("migrations/0002_b.down.sql", "DROP INDEX CONCURRENTLY j;"),
		}},
		{"a migration with no down", []domain.ChangedFile{
			sql("migrations/0002_b.up.sql", "ALTER TABLE t ADD COLUMN x text;"),
		}},
		{"a destructive migration", []domain.ChangedFile{
			sql("migrations/0002_b.up.sql", "DROP TABLE t;"),
			sql("migrations/0002_b.down.sql", "CREATE TABLE t (id bigint);"),
		}},
		{"an unparseable migration", []domain.ChangedFile{
			sql("migrations/0002_b.up.sql", "ALTER TABLE t FLARBLE COLUMN x;"),
			sql("migrations/0002_b.down.sql", "SELECT 1;"),
		}},
		{"a manifest", []domain.ChangedFile{{
			Path:     "k8s/ns.yaml",
			Status:   domain.StatusRemoved,
			Previous: []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: legacy\n"),
		}}},
	}

	for _, addition := range additions {
		t.Run(addition.name, func(t *testing.T) {
			extended := append(append([]domain.ChangedFile{}, base...), addition.files...)

			cert, _ := e.Certify(ctx, extended)
			if cert.Grade.Rank() > baseCert.Grade.Rank() {
				t.Errorf("adding %s raised the grade from %s to %s", addition.name, baseCert.Grade, cert.Grade)
			}
		})
	}
}

// The digest is what makes a certificate attributable to an exact changeset.
func TestInputDigest(t *testing.T) {
	t.Parallel()

	base := []domain.ChangedFile{
		{Path: "a.sql", Status: domain.StatusAdded, Current: []byte("SELECT 1;")},
		{Path: "b.sql", Status: domain.StatusAdded, Current: []byte("SELECT 2;")},
	}

	want := engine.InputDigest(base)

	t.Run("stable across calls", func(t *testing.T) {
		t.Parallel()
		for i := 0; i < 50; i++ {
			if got := engine.InputDigest(base); got != want {
				t.Fatalf("digest changed between calls: %s vs %s", want, got)
			}
		}
	})

	t.Run("independent of input order", func(t *testing.T) {
		t.Parallel()
		swapped := []domain.ChangedFile{base[1], base[0]}
		if got := engine.InputDigest(swapped); got != want {
			t.Errorf("digest depends on order: %s vs %s", want, got)
		}
	})

	t.Run("sensitive to content", func(t *testing.T) {
		t.Parallel()
		changed := []domain.ChangedFile{base[0], {Path: "b.sql", Status: domain.StatusAdded, Current: []byte("SELECT 3;")}}
		if got := engine.InputDigest(changed); got == want {
			t.Error("digest ignored a content change")
		}
	})

	t.Run("sensitive to path", func(t *testing.T) {
		t.Parallel()
		renamed := []domain.ChangedFile{base[0], {Path: "c.sql", Status: domain.StatusAdded, Current: []byte("SELECT 2;")}}
		if got := engine.InputDigest(renamed); got == want {
			t.Error("digest ignored a path change")
		}
	})

	t.Run("sensitive to status", func(t *testing.T) {
		t.Parallel()
		restatused := []domain.ChangedFile{base[0], {Path: "b.sql", Status: domain.StatusModified, Current: []byte("SELECT 2;")}}
		if got := engine.InputDigest(restatused); got == want {
			t.Error("digest ignored a status change")
		}
	})

	// Reversibility is a property of a transition, so two changesets reaching the same final
	// state from different starting points are different inputs.
	t.Run("sensitive to the previous side", func(t *testing.T) {
		t.Parallel()

		from1 := []domain.ChangedFile{{Path: "a.sql", Status: domain.StatusModified, Previous: []byte("1"), Current: []byte("2")}}
		from2 := []domain.ChangedFile{{Path: "a.sql", Status: domain.StatusModified, Previous: []byte("9"), Current: []byte("2")}}

		if engine.InputDigest(from1) == engine.InputDigest(from2) {
			t.Error("digest ignored the previous content")
		}
	})

	// Length prefixing is what stops "ab"+"c" from colliding with "a"+"bc".
	t.Run("field boundaries cannot be shifted", func(t *testing.T) {
		t.Parallel()

		one := []domain.ChangedFile{{Path: "ab", Status: domain.StatusAdded, Current: []byte("c")}}
		two := []domain.ChangedFile{{Path: "a", Status: domain.StatusAdded, Current: []byte("bc")}}

		if engine.InputDigest(one) == engine.InputDigest(two) {
			t.Error("digest collides when a field boundary shifts")
		}
	})

	t.Run("empty changeset has a digest", func(t *testing.T) {
		t.Parallel()
		if got := engine.InputDigest(nil); len(got) != 64 {
			t.Errorf("digest of an empty changeset = %q, want 64 hex characters", got)
		}
	})
}

// Every certificate the engine emits must be internally consistent, whatever the input.
func TestCertificateInvariantsHoldForEveryFixture(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixture root: %v", err)
	}

	files := provider.NewFake(root)
	e := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})

	for _, group := range []string{"postgres", "kubernetes"} {
		cases, err := fixture.Cases(root, group)
		if err != nil {
			t.Fatalf("loading %s fixtures: %v", group, err)
		}

		for _, tc := range cases {
			t.Run(group+"/"+tc.Name, func(t *testing.T) {
				t.Parallel()

				ctx := context.Background()

				changed, err := files.ChangedFiles(ctx, tc.Ref)
				if err != nil {
					t.Fatalf("resolving changeset: %v", err)
				}

				cert, _ := e.Certify(ctx, changed)

				if !cert.Grade.Valid() {
					t.Errorf("Grade %q is not a valid grade", cert.Grade)
				}
				if cert.AIGateStatus != cert.Grade.Gate() {
					t.Errorf("AIGateStatus %q disagrees with grade %q", cert.AIGateStatus, cert.Grade)
				}
				if cert.SchemaVersion != domain.SchemaVersion {
					t.Errorf("SchemaVersion = %q", cert.SchemaVersion)
				}
				if len(cert.InputDigest) != 64 {
					t.Errorf("InputDigest = %q, want 64 hex characters", cert.InputDigest)
				}

				// F must always be explained, and only F carries blockers.
				if cert.Grade == domain.GradeF && len(cert.Blockers) == 0 {
					t.Error("grade F with no blockers")
				}
				if cert.Grade != domain.GradeF && len(cert.Blockers) > 0 {
					t.Errorf("grade %q carries blockers: %v", cert.Grade, cert.Blockers)
				}

				// An unreversible changeset must never present a plan that looks complete.
				var unreversible bool
				for _, f := range cert.Findings {
					if f.Reversibility == domain.ReversibilityIrreversible || f.Reversibility == domain.ReversibilityUnknown {
						unreversible = true
					}
					if !f.Reversibility.Valid() {
						t.Errorf("finding %s has invalid reversibility %q", f.RuleID, f.Reversibility)
					}
				}
				if unreversible {
					if len(cert.UndoPlan) == 0 || !strings.Contains(string(cert.UndoPlan[0]), "NO COMPLETE UNDO") {
						t.Errorf("undo plan claims completeness despite an unreversible finding: %v", cert.UndoPlan)
					}
					if cert.Grade != domain.GradeF {
						t.Errorf("grade %q despite an unreversible finding, want F", cert.Grade)
					}
				}

				if cert.Findings == nil || cert.UndoPlan == nil || cert.Blockers == nil || cert.DownMigrations == nil {
					t.Error("certificate contains a nil slice, which serializes as null")
				}
			})
		}
	}
}

// A certificate must survive a JSON round trip unchanged. The renderers and every downstream
// merge gate depend on it.
func TestCertificateSurvivesJSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := certify(t,
		sql("migrations/0001.up.sql", "ALTER TABLE orders ADD COLUMN notes text;\nDROP TABLE legacy;"),
		sql("migrations/0001.down.sql", "ALTER TABLE orders DROP COLUMN notes;"),
	)

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	var decoded domain.ReversibilityCertificate
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	if diff := cmp.Diff(original, decoded); diff != "" {
		t.Errorf("certificate changed across a JSON round trip (-original +decoded):\n%s", diff)
	}
}

// Concurrent use must not change any answer. The engine is documented as safe to share.
func TestEngineIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	e := realEngine()
	files := []domain.ChangedFile{
		sql("migrations/0001.up.sql", "DROP TABLE t;\nCREATE INDEX CONCURRENTLY i ON u (c);"),
		sql("migrations/0001.down.sql", "SELECT 1;"),
	}

	want, _ := e.Certify(context.Background(), files)
	wantHash := certificateHash(t, want)

	results := make(chan string, 16)
	for i := 0; i < 16; i++ {
		go func() {
			cert, _ := e.Certify(context.Background(), files)

			encoded, err := json.Marshal(cert)
			if err != nil {
				results <- fmt.Sprintf("marshal error: %v", err)
				return
			}
			sum := sha256.Sum256(encoded)
			results <- hex.EncodeToString(sum[:])
		}()
	}

	for i := 0; i < 16; i++ {
		if got := <-results; got != wantHash {
			t.Errorf("goroutine %d produced a different certificate: %s", i, got)
		}
	}
}
