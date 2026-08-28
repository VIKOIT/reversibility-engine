// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/fixture"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

func newAnalyzer(t *testing.T, opts terraform.Options) *terraform.Analyzer {
	t.Helper()

	a, err := terraform.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// plan wraps a resource_changes array in the surrounding document, so a test case is only the
// part that differs.
func plan(changes string) domain.ChangedFile {
	return domain.ChangedFile{
		Path:    "infra/main.tfplan.json",
		Status:  domain.StatusAdded,
		Current: []byte(`{"format_version":"1.1","terraform_version":"1.9.5","resource_changes":[` + changes + `]}`),
	}
}

func analyze(t *testing.T, a *terraform.Analyzer, f domain.ChangedFile) []domain.Finding {
	t.Helper()

	got, err := a.Analyze(context.Background(), []domain.ChangedFile{f})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return got
}

// Every fixture in the terraform group, classified against the embedded catalog.
func TestFixtures(t *testing.T) {
	t.Parallel()

	root, err := fixture.Root()
	if err != nil {
		t.Fatalf("locating fixtures: %v", err)
	}

	cases, err := fixture.Cases(root, "terraform")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	files := provider.NewFake(root)
	a := newAnalyzer(t, terraform.Options{})

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			changed, err := provider.All(context.Background(), files, tc.Ref)
			if err != nil {
				t.Fatalf("reading the fixture: %v", err)
			}

			got, err := a.Analyze(context.Background(), changed)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}

			if len(got) != len(tc.Expect.Findings) {
				t.Fatalf("got %d findings, want %d: %+v", len(got), len(tc.Expect.Findings), got)
			}

			for i, want := range tc.Expect.Findings {
				g := got[i]
				if g.RuleID != want.RuleID {
					t.Errorf("finding %d: RuleID = %q, want %q", i, g.RuleID, want.RuleID)
				}
				if g.File != want.File {
					t.Errorf("finding %d: File = %q, want %q", i, g.File, want.File)
				}
				if g.Line != want.Line {
					t.Errorf("finding %d: Line = %d, want %d", i, g.Line, want.Line)
				}
				if g.Reversibility != want.Reversibility {
					t.Errorf("finding %d (%s): Reversibility = %q, want %q", i, g.RuleID, g.Reversibility, want.Reversibility)
				}
				if g.LockHazard != want.LockHazard {
					t.Errorf("finding %d (%s): LockHazard = %q, want %q", i, g.RuleID, g.LockHazard, want.LockHazard)
				}
				if hasUndo := g.UndoStep != ""; hasUndo != want.WantUndoStep {
					t.Errorf("finding %d (%s): undo present = %v, want %v", i, g.RuleID, hasUndo, want.WantUndoStep)
				}
			}
		})
	}
}

// THE CORE INSIGHT, as a test. Only destruction is classified, which is what keeps the catalog
// finite — a created or updated-in-place resource has a reverse by construction, whatever its
// type is.
func TestOnlyDestructionConsultsTheCatalog(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	// aws_db_instance is as stateful as the catalog gets. Creating one is still REVERSIBLE.
	created := analyze(t, a, plan(`{"address":"aws_db_instance.new","mode":"managed","type":"aws_db_instance",
		"change":{"actions":["create"],"before":null,"after":{"allocated_storage":100}}}`))
	if len(created) != 1 || created[0].RuleID != "TF008" || created[0].Reversibility != domain.ReversibilityReversible {
		t.Errorf("creating a stateful resource = %+v, want one TF008/REVERSIBLE", created)
	}

	// A no-op and a data source read produce nothing at all.
	quiet := analyze(t, a, plan(`{"address":"aws_db_instance.same","mode":"managed","type":"aws_db_instance",
		"change":{"actions":["no-op"],"before":{},"after":{}}},
		{"address":"data.aws_ami.this","mode":"data","type":"aws_ami",
		"change":{"actions":["read"],"before":null,"after":{}}}`))
	if len(quiet) != 0 {
		t.Errorf("a no-op and a data read produced %+v, want nothing", quiet)
	}
}

// Names lie. aws_db_subnet_group contains "db" and holds nothing, and classifying by matching
// the type name would get it exactly wrong.
func TestClassificationNeverMatchesTheTypeName(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	got := analyze(t, a, plan(`{"address":"aws_db_subnet_group.main","mode":"managed","type":"aws_db_subnet_group",
		"change":{"actions":["delete"],"before":{"id":"main","name":"main","subnet_ids":["subnet-1"]},"after":null}}`))

	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].RuleID != "TF005" || got[0].Reversibility != domain.ReversibilityCostly {
		t.Errorf("aws_db_subnet_group deleted = %s/%s, want TF005/COSTLY — the name contains \"db\" and the resource holds nothing",
			got[0].RuleID, got[0].Reversibility)
	}
}

// Layer 1 runs before Layer 2, so a type nobody has classified is still caught when the plan
// plainly shows it holds something.
func TestEvidenceClassifiesATypeAbsentFromTheCatalog(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	tests := []struct {
		name    string
		before  string
		wantID  string
		wantRev domain.Reversibility
	}{
		{
			name:    "allocated_storage marks it stateful",
			before:  `{"id":"x","allocated_storage":200}`,
			wantID:  "TF001",
			wantRev: domain.ReversibilityIrreversible,
		},
		{
			name:    "no evidence leaves it unknown",
			before:  `{"id":"x","name":"x"}`,
			wantID:  "TF010",
			wantRev: domain.ReversibilityUnknown,
		},
		{
			// The aws_instance ruling in miniature: an empty collection is the attribute
			// existing, not the attribute being used. Treating it as evidence would raise every
			// EC2 instance and undo the reason aws_instance is catalogued STATELESS.
			name:    "an empty collection is not evidence",
			before:  `{"id":"x","ephemeral_block_device":[]}`,
			wantID:  "TF010",
			wantRev: domain.ReversibilityUnknown,
		},
		{
			name:    "a non-empty collection is evidence",
			before:  `{"id":"x","ephemeral_block_device":[{"device_name":"/dev/sdb"}]}`,
			wantID:  "TF001",
			wantRev: domain.ReversibilityIrreversible,
		},
		{
			name:    "a null value is not evidence",
			before:  `{"id":"x","kms_key_id":null}`,
			wantID:  "TF010",
			wantRev: domain.ReversibilityUnknown,
		},
		{
			// The attribute existing at all is the schema signal that this type has storage or
			// protection to speak of, even when it is switched off.
			name:    "a false boolean is evidence",
			before:  `{"id":"x","storage_encrypted":false}`,
			wantID:  "TF001",
			wantRev: domain.ReversibilityIrreversible,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := analyze(t, a, plan(`{"address":"aws_unlisted_thing.x","mode":"managed","type":"aws_unlisted_thing",
				"change":{"actions":["delete"],"before":`+tc.before+`,"after":null}}`))

			if len(got) != 1 {
				t.Fatalf("got %+v", got)
			}
			if got[0].RuleID != tc.wantID || got[0].Reversibility != tc.wantRev {
				t.Errorf("= %s/%s, want %s/%s", got[0].RuleID, got[0].Reversibility, tc.wantID, tc.wantRev)
			}
		})
	}
}

// The two elevate-on-their-own properties, each proved alone.
func TestDisabledSafetyElevatesADestroyOnItsOwn(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	for _, key := range []string{"force_destroy", "skip_final_snapshot"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			// aws_security_group is catalogued STATELESS, so without the flag this is TF005.
			base := analyze(t, a, plan(`{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
				"change":{"actions":["delete"],"before":{"id":"sg-1"},"after":null}}`))
			if base[0].Reversibility != domain.ReversibilityCostly {
				t.Fatalf("baseline = %q, want COSTLY", base[0].Reversibility)
			}

			got := analyze(t, a, plan(`{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
				"change":{"actions":["delete"],"before":{"id":"sg-1","`+key+`":true},"after":null}}`))

			if got[0].Reversibility != domain.ReversibilityIrreversible {
				t.Errorf("with %s=true the verdict is %q, want IRREVERSIBLE", key, got[0].Reversibility)
			}
			if !strings.Contains(got[0].Rationale, key) {
				t.Errorf("the rationale does not name %s: %s", key, got[0].Rationale)
			}

			// false must change nothing.
			off := analyze(t, a, plan(`{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
				"change":{"actions":["delete"],"before":{"id":"sg-1","`+key+`":false},"after":null}}`))
			if off[0].Reversibility != domain.ReversibilityCostly {
				t.Errorf("with %s=false the verdict is %q, want the baseline COSTLY", key, off[0].Reversibility)
			}
		})
	}
}

// TF004 is a closed list of named paths and nothing else. An ordinary update stays REVERSIBLE,
// and the reverse transition on a listed path is not this rule.
func TestSafetyTransitionsAreAClosedList(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	tests := []struct {
		name   string
		before string
		after  string
		wantID string
	}{
		{"deletion_protection off", `{"deletion_protection":true}`, `{"deletion_protection":false}`, "TF004"},
		{"deletion_protection on", `{"deletion_protection":false}`, `{"deletion_protection":true}`, "TF007"},
		{"enable_deletion_protection off", `{"enable_deletion_protection":true}`, `{"enable_deletion_protection":false}`, "TF004"},
		{"skip_final_snapshot on", `{"skip_final_snapshot":false}`, `{"skip_final_snapshot":true}`, "TF004"},
		{"force_destroy on", `{"force_destroy":false}`, `{"force_destroy":true}`, "TF004"},
		{"backup_retention_period to zero", `{"backup_retention_period":7}`, `{"backup_retention_period":0}`, "TF004"},
		{"backup_retention_period raised", `{"backup_retention_period":7}`, `{"backup_retention_period":14}`, "TF007"},
		{"deletion_window_in_days to zero", `{"deletion_window_in_days":30}`, `{"deletion_window_in_days":0}`, "TF004"},

		// The two permitted nested paths. Terraform emits a MaxItems:1 block as a list of one.
		{"versioning disabled", `{"versioning":[{"enabled":true}]}`, `{"versioning":[{"enabled":false}]}`, "TF004"},
		{"point_in_time_recovery disabled", `{"point_in_time_recovery":[{"enabled":true}]}`, `{"point_in_time_recovery":[{"enabled":false}]}`, "TF004"},

		// Nesting is two named paths, not general recursion. A same-named key somewhere else is
		// not this rule.
		{"a lookalike nested elsewhere", `{"replication":[{"deletion_protection":true}]}`, `{"replication":[{"deletion_protection":false}]}`, "TF007"},
		{"an ordinary property", `{"description":"before"}`, `{"description":"after"}`, "TF007"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := analyze(t, a, plan(`{"address":"aws_db_instance.x","mode":"managed","type":"aws_db_instance",
				"change":{"actions":["update"],"before":`+tc.before+`,"after":`+tc.after+`}}`))

			if len(got) != 1 {
				t.Fatalf("got %+v", got)
			}
			if got[0].RuleID != tc.wantID {
				t.Errorf("= %s (%s), want %s. Rationale: %s", got[0].RuleID, got[0].Reversibility, tc.wantID, got[0].Rationale)
			}
		})
	}
}

// A user may classify a type the catalog does not know, and may tighten one it does. Weakening
// is a configuration error, because that path is a waiver — which carries a reason and an expiry
// and which changes the gate decision rather than the grade.
func TestOverridesMayOnlyTighten(t *testing.T) {
	t.Parallel()

	t.Run("classifying an unknown type is permitted", func(t *testing.T) {
		t.Parallel()

		a := newAnalyzer(t, terraform.Options{
			Overrides: []terraform.Override{{Type: "aws_unlisted_thing", Class: terraform.ClassStateful}},
		})

		got := analyze(t, a, plan(`{"address":"aws_unlisted_thing.x","mode":"managed","type":"aws_unlisted_thing",
			"change":{"actions":["delete"],"before":{"id":"x"},"after":null}}`))

		if got[0].RuleID != "TF001" {
			t.Errorf("= %s, want TF001 once the user classified the type", got[0].RuleID)
		}
	})

	t.Run("tightening a catalogued type is permitted", func(t *testing.T) {
		t.Parallel()

		a := newAnalyzer(t, terraform.Options{
			Overrides: []terraform.Override{{Type: "aws_security_group", Class: terraform.ClassStateful}},
		})

		got := analyze(t, a, plan(`{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
			"change":{"actions":["delete"],"before":{"id":"sg-1"},"after":null}}`))

		if got[0].Reversibility != domain.ReversibilityIrreversible {
			t.Errorf("= %q, want IRREVERSIBLE after tightening", got[0].Reversibility)
		}
	})

	t.Run("weakening a catalogued type is refused", func(t *testing.T) {
		t.Parallel()

		_, err := terraform.New(terraform.Options{
			Overrides: []terraform.Override{{Type: "aws_db_instance", Class: terraform.ClassStateless}},
		})
		if err == nil {
			t.Fatal("weakening aws_db_instance to STATELESS was accepted")
		}
		if !errors.Is(err, domain.ErrInvalidPolicy) {
			t.Errorf("error = %v, want one wrapping ErrInvalidPolicy", err)
		}
		if !strings.Contains(err.Error(), "waiver") {
			t.Errorf("the error does not point at the waiver path: %v", err)
		}
	})
}

// THE PROPERTY. A user's configuration may lower a grade and may never raise one — raise meaning
// improve. Since overrides can only tighten, the classification of any given plan can only get
// more severe.
func TestUserConfigCanNeverImproveAVerdict(t *testing.T) {
	t.Parallel()

	p := plan(`{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
		"change":{"actions":["delete"],"before":{"id":"sg-1"},"after":null}},
		{"address":"aws_db_instance.y","mode":"managed","type":"aws_db_instance",
		"change":{"actions":["delete"],"before":{"id":"db-1"},"after":null}}`)

	base := analyze(t, newAnalyzer(t, terraform.Options{}), p)

	// Every override the configuration is permitted to express.
	for _, o := range []terraform.Override{
		{Type: "aws_security_group", Class: terraform.ClassStateful},
		{Type: "aws_db_instance", Class: terraform.ClassStateful},
		{Type: "aws_unlisted_thing", Class: terraform.ClassStateless},
	} {
		configured := analyze(t, newAnalyzer(t, terraform.Options{Overrides: []terraform.Override{o}}), p)

		if len(configured) != len(base) {
			t.Fatalf("override %v changed the finding count", o)
		}
		for i := range configured {
			if configured[i].Reversibility.Severity() < base[i].Reversibility.Severity() {
				t.Errorf("override %v weakened %s from %s to %s",
					o, base[i].RuleID, base[i].Reversibility, configured[i].Reversibility)
			}
		}
	}
}

// THE PROPERTY. Adding a destroyed resource to a plan can only make the verdict worse. There is
// no resource whose destruction makes the rest of a plan safer.
func TestAddingADestroyNeverImprovesTheVerdict(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	base := analyze(t, a, plan(`{"address":"aws_security_group.a","mode":"managed","type":"aws_security_group",
		"change":{"actions":["delete"],"before":{"id":"sg-a"},"after":null}}`))

	additions := []string{
		`{"address":"aws_db_instance.b","mode":"managed","type":"aws_db_instance","change":{"actions":["delete"],"before":{"id":"db-b"},"after":null}}`,
		`{"address":"aws_unlisted.c","mode":"managed","type":"aws_unlisted","change":{"actions":["delete"],"before":{"id":"c"},"after":null}}`,
		`{"address":"aws_lb.d","mode":"managed","type":"aws_lb","change":{"actions":["create","delete"],"before":{"id":"lb-d"},"after":{}}}`,
		`{"address":"aws_s3_bucket.e","mode":"managed","type":"aws_s3_bucket","change":{"actions":["create"],"before":null,"after":{}}}`,
	}

	for _, extra := range additions {
		grown := analyze(t, a, plan(`{"address":"aws_security_group.a","mode":"managed","type":"aws_security_group",
			"change":{"actions":["delete"],"before":{"id":"sg-a"},"after":null}},`+extra))

		// The original finding must be untouched, and nothing may have been softened.
		var original *domain.Finding
		for i := range grown {
			if strings.Contains(grown[i].Statement, "aws_security_group.a") {
				original = &grown[i]
			}
		}
		if original == nil {
			t.Fatalf("the original finding vanished when %s was added", extra)
		}
		if original.Reversibility.Severity() < base[0].Reversibility.Severity() {
			t.Errorf("adding a resource weakened the original finding from %s to %s",
				base[0].Reversibility, original.Reversibility)
		}
	}
}

// Same plan, same catalog, same bytes. A digest that moved between runs would make every
// certificate unverifiable.
func TestAnalysisIsDeterministic(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	p := plan(`{"address":"aws_zeta.z","mode":"managed","type":"aws_zeta","change":{"actions":["delete"],"before":{"id":"z"},"after":null}},
		{"address":"aws_alpha.a","mode":"managed","type":"aws_alpha","change":{"actions":["delete"],"before":{"id":"a"},"after":null}},
		{"address":"aws_db_instance.m","mode":"managed","type":"aws_db_instance","change":{"actions":["delete"],"before":{"id":"m"},"after":null}}`)

	first := analyze(t, a, p)
	for i := 0; i < 20; i++ {
		again := analyze(t, a, p)
		if len(again) != len(first) {
			t.Fatalf("run %d produced %d findings, want %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("run %d differs at finding %d:\n %+v\n %+v", i, j, first[j], again[j])
			}
		}
	}

	// Sorted by address, so the order does not depend on how the plan happened to list them.
	if !strings.Contains(first[0].Statement, "aws_alpha.a") {
		t.Errorf("findings are not sorted by address: first is %q", first[0].Statement)
	}
}

func TestSupports(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{ExtraPlanPaths: []string{"infra/custom-name.json"}})

	tests := map[string]bool{
		"main.tfplan.json":        true,
		"infra/prod.tfplan.json":  true,
		"infra/custom-name.json":  true,
		"plan.json":               false,
		"package.json":            false,
		"migrations/0001.up.sql":  false,
		"k8s/deployment.yaml":     false,
		"infra/terraform.tfstate": false,
	}

	for path, want := range tests {
		if got := a.Supports(path); got != want {
			t.Errorf("Supports(%q) = %v, want %v", path, got, want)
		}
	}
}

// A removed plan document is not a change to any infrastructure. Classifying its former contents
// would report a destruction nobody proposed.
func TestARemovedPlanIsNotAnalyzed(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	got, err := a.Analyze(context.Background(), []domain.ChangedFile{{
		Path:     "infra/main.tfplan.json",
		Status:   domain.StatusRemoved,
		Previous: []byte(`{"format_version":"1.1","resource_changes":[{"address":"aws_db_instance.x","mode":"managed","type":"aws_db_instance","change":{"actions":["delete"],"before":{"id":"x"},"after":null}}]}`),
	}})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("deleting a plan file produced %+v, want nothing", got)
	}
}

func TestUnreadablePlansFailClosed(t *testing.T) {
	t.Parallel()

	a := newAnalyzer(t, terraform.Options{})

	for _, body := range []string{
		`not json at all`,
		`{"terraform_version":"1.9.5"}`,
		`{"format_version":"0.1","resource_changes":[]}`,
		`{"format_version":"2.0","resource_changes":[]}`,
	} {
		got := analyze(t, a, domain.ChangedFile{
			Path: "infra/main.tfplan.json", Status: domain.StatusAdded, Current: []byte(body),
		})

		if len(got) != 1 || got[0].RuleID != "TF009" || got[0].Reversibility != domain.ReversibilityUnknown {
			t.Errorf("%.30s produced %+v, want one TF009/UNKNOWN", body, got)
		}
	}

	// 1.0 and 1.1 are the supported pair.
	for _, version := range []string{"1.0", "1.1"} {
		got := analyze(t, a, domain.ChangedFile{
			Path:    "infra/main.tfplan.json",
			Status:  domain.StatusAdded,
			Current: []byte(`{"format_version":"` + version + `","resource_changes":[]}`),
		})
		if len(got) != 0 {
			t.Errorf("format version %s produced %+v, want it read cleanly", version, got)
		}
	}
}
