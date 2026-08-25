// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
)

// policyToday is fixed so waiver expiry cannot make this suite start failing on a date.
var policyToday = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func sqlFile(path string) domain.ChangedFile {
	return domain.ChangedFile{Path: path, Status: domain.StatusAdded, Current: []byte("SELECT 1;\n")}
}

func certifyWith(t *testing.T, p *policy.Policy, findings []domain.Finding) domain.ReversibilityCertificate {
	t.Helper()

	eng := engine.New(
		[]analyzer.Analyzer{stubAnalyzer{name: "stub", supports: true, findings: findings}},
		engine.WithPolicy(p),
		engine.WithToday(policyToday),
	)

	cert, err := eng.Certify(context.Background(), []domain.ChangedFile{sqlFile("migrations/0031_backfill.sql")})
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	return cert
}

func costly(rule, file string) domain.Finding {
	return domain.Finding{
		RuleID: rule, File: file, Line: 1,
		Reversibility: domain.ReversibilityCostly,
		LockHazard:    domain.LockNone,
		Rationale:     "costly to reverse",
		UndoStep:      domain.UndoStep("-- undo " + rule),
	}
}

func irreversible(rule, file string) domain.Finding {
	return domain.Finding{
		RuleID: rule, File: file, Line: 1,
		Reversibility: domain.ReversibilityIrreversible,
		LockHazard:    domain.LockExclusive,
		Rationale:     "destroys data",
	}
}

// THE CENTRAL INVARIANT OF THE POLICY FILE. A waiver records that somebody accepted a risk. It
// does not make the change reversible, so it may not move the measurement — and because the AI
// merge gate follows the measurement, no waiver can ever authorise an agent to merge.
func TestWaiverNeverImprovesTheGradeOrTheGate(t *testing.T) {
	t.Parallel()

	waived := &policy.Policy{Waivers: []policy.Waiver{{
		Rule: "PG001", Reason: "accepted", Expires: "2026-10-01", ApprovedBy: "vikoit",
	}}}

	cert := certifyWith(t, waived, []domain.Finding{irreversible("PG001", "migrations/0031_backfill.sql")})

	if cert.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F; a waiver must not move the measurement", cert.Grade)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q, want FAIL; a waiver must never let an agent merge", cert.AIGateStatus)
	}
	if cert.EffectiveGrade != domain.GradeA {
		t.Errorf("EffectiveGrade = %q, want A; the waiver should unblock the pipeline", cert.EffectiveGrade)
	}
	if len(cert.Waived) != 1 {
		t.Fatalf("Waived = %v, want the finding reported rather than deleted", cert.Waived)
	}
	if len(cert.Findings) != 0 {
		t.Errorf("Findings = %v, want the waived finding moved out of the scored set", cert.Findings)
	}

	// The blockers still explain the F. The certificate has to say why the change is graded as
	// it is, even when the pipeline was let through.
	if len(cert.Blockers) == 0 {
		t.Error("a grade F certificate carries no blockers")
	}
}

// A waiver accepts a risk; it does not invent an undo. A plan that quietly omitted the waived
// half would claim a completeness it does not have.
func TestUndoPlanStillCoversWaivedFindings(t *testing.T) {
	t.Parallel()

	waived := &policy.Policy{Waivers: []policy.Waiver{{
		Rule: "PG001", Reason: "accepted", Expires: "2026-10-01",
	}}}

	cert := certifyWith(t, waived, []domain.Finding{irreversible("PG001", "migrations/0031_backfill.sql")})

	joined := strings.Join(undoStrings(cert.UndoPlan), "\n")
	if !strings.Contains(joined, "NO COMPLETE UNDO EXISTS") {
		t.Errorf("the undo plan for a waived irreversible change claims a complete undo:\n%s", joined)
	}
}

func TestEffectiveGradeEqualsGradeWithoutAPolicy(t *testing.T) {
	t.Parallel()

	cert := certifyWith(t, nil, []domain.Finding{costly("PG012", "migrations/0031_backfill.sql")})

	if cert.Grade != cert.EffectiveGrade {
		t.Errorf("Grade = %q but EffectiveGrade = %q with no policy in play", cert.Grade, cert.EffectiveGrade)
	}
	if cert.PolicyDigest != "" {
		t.Errorf("PolicyDigest = %q with no policy", cert.PolicyDigest)
	}
	if len(cert.Waived) != 0 {
		t.Errorf("Waived = %v with no policy", cert.Waived)
	}
}

// An expired waiver is inert, so the finding returns with no edit to the file. This is the
// property that makes the expiry date meaningful rather than decorative.
func TestExpiredWaiverRestoresTheFinding(t *testing.T) {
	t.Parallel()

	expired := &policy.Policy{Waivers: []policy.Waiver{{
		Rule: "PG001", Reason: "accepted", Expires: "2026-08-24",
	}}}

	cert := certifyWith(t, expired, []domain.Finding{irreversible("PG001", "migrations/0031_backfill.sql")})

	if len(cert.Waived) != 0 {
		t.Errorf("an expired waiver still applied: %+v", cert.Waived)
	}
	if cert.EffectiveGrade != domain.GradeF {
		t.Errorf("EffectiveGrade = %q, want F once the waiver lapsed", cert.EffectiveGrade)
	}
}

// The policy is an input to the verdict, so it is part of what the digest attributes the verdict
// to. Two runs over identical files under different policies are not the same run.
func TestPolicyIsHashedIntoTheInputDigest(t *testing.T) {
	t.Parallel()

	findings := []domain.Finding{costly("PG012", "migrations/0031_backfill.sql")}

	none := certifyWith(t, nil, findings)

	first, err := policy.Parse([]byte("version: 1\ngate: A\n"), "a.yml", policyToday)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	second, err := policy.Parse([]byte("version: 1\ngate: B\n"), "b.yml", policyToday)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	withFirst := certifyWith(t, first, findings)
	withSecond := certifyWith(t, second, findings)

	if withFirst.InputDigest == none.InputDigest {
		t.Error("adding a policy did not change the input digest")
	}
	if withFirst.InputDigest == withSecond.InputDigest {
		t.Error("two different policies produced the same input digest")
	}
	if withFirst.PolicyDigest != first.Digest {
		t.Errorf("PolicyDigest = %q, want the policy's own digest %q", withFirst.PolicyDigest, first.Digest)
	}

	// Determinism has to survive the addition: the same policy twice is the same certificate.
	again := certifyWith(t, first, findings)
	if again.InputDigest != withFirst.InputDigest {
		t.Error("the same policy produced two different digests")
	}
}

// A policy that cannot be resolved is a broken run. Continuing would enforce something nobody
// configured, and the grade for a run that did not happen is F.
func TestUnresolvablePolicyGradesF(t *testing.T) {
	t.Parallel()

	loosening := &policy.Policy{Overrides: []policy.Override{{
		Rule: "PG001", Severity: domain.ReversibilityCostly,
	}}}

	eng := engine.New(
		[]analyzer.Analyzer{stubAnalyzer{name: "stub", supports: true, findings: []domain.Finding{irreversible("PG001", "migrations/0031_backfill.sql")}}},
		engine.WithPolicy(loosening),
		engine.WithToday(policyToday),
	)

	cert, err := eng.Certify(context.Background(), []domain.ChangedFile{sqlFile("migrations/0031_backfill.sql")})
	if err == nil {
		t.Fatal("a policy that weakened a finding was accepted")
	}
	if !errors.Is(err, domain.ErrInvalidPolicy) {
		t.Errorf("error = %v, want one wrapping ErrInvalidPolicy", err)
	}
	if cert.Grade != domain.GradeF || cert.EffectiveGrade != domain.GradeF {
		t.Errorf("Grade = %q / EffectiveGrade = %q, want F for a run that could not resolve its policy",
			cert.Grade, cert.EffectiveGrade)
	}
	if cert.AIGateStatus != domain.GateFail {
		t.Errorf("AIGateStatus = %q, want FAIL", cert.AIGateStatus)
	}
}

// Overrides tighten the measurement, which is the one direction configuration is allowed to move
// it. The grade itself gets worse, not better.
func TestTighteningOverrideWorsensTheGrade(t *testing.T) {
	t.Parallel()

	tighten := &policy.Policy{Overrides: []policy.Override{{
		Rule: "K8S008", Severity: domain.ReversibilityIrreversible,
	}}}

	before := certifyWith(t, nil, []domain.Finding{costly("K8S008", "migrations/0031_backfill.sql")})
	after := certifyWith(t, tighten, []domain.Finding{costly("K8S008", "migrations/0031_backfill.sql")})

	if before.Grade.Rank() <= after.Grade.Rank() {
		t.Errorf("an override to IRREVERSIBLE did not worsen the grade: %q then %q", before.Grade, after.Grade)
	}
	if after.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F", after.Grade)
	}
}

func undoStrings(plan []domain.UndoStep) []string {
	out := make([]string, 0, len(plan))
	for _, s := range plan {
		out = append(out, string(s))
	}
	return out
}
