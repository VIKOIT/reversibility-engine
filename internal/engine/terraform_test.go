// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/kubernetes"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/postgres"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer/terraform"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/engine"
)

func terraformAnalyzers(t *testing.T) []analyzer.Analyzer {
	t.Helper()

	tf, err := terraform.New(terraform.Options{})
	if err != nil {
		t.Fatalf("terraform.New: %v", err)
	}
	return []analyzer.Analyzer{postgres.New(), kubernetes.New(), tf}
}

const destroyPlan = `{"format_version":"1.1","terraform_version":"1.9.5","resource_changes":[
  {"address":"aws_db_instance.orders","mode":"managed","type":"aws_db_instance",
   "change":{"actions":["delete"],"before":{"id":"orders"},"after":null}}]}`

// The catalog identity travels onto the certificate and into the digest ONLY when a plan was
// actually analyzed.
//
// This is what keeps every digest ever produced for a changeset with no Terraform plan exactly
// as it was — a stored certificate from before this analyzer existed still compares against a
// rerun, and verdicts.txt does not move.
func TestCatalogIdentityAppearsOnlyWhenAPlanIsAnalyzed(t *testing.T) {
	t.Parallel()

	eng := engine.New(terraformAnalyzers(t))

	sql := []domain.ChangedFile{{
		Path: "migrations/0001.up.sql", Status: domain.StatusAdded,
		Current: []byte("CREATE INDEX CONCURRENTLY i ON t (a);\n"),
	}}
	withoutPlan, err := eng.Certify(context.Background(), sql)
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	if withoutPlan.CatalogVersion != "" {
		t.Errorf("CatalogVersion = %q for a changeset with no Terraform plan", withoutPlan.CatalogVersion)
	}

	// The same files, graded by an engine with no Terraform analyzer at all, must produce the
	// identical digest. If it does not, registering the analyzer changed every existing verdict.
	plain := engine.New([]analyzer.Analyzer{postgres.New(), kubernetes.New()})
	bare, err := plain.Certify(context.Background(), sql)
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	if bare.InputDigest != withoutPlan.InputDigest {
		t.Errorf("registering the Terraform analyzer changed the digest of a changeset it does not claim:\n  %s\n  %s",
			bare.InputDigest, withoutPlan.InputDigest)
	}

	withPlan, err := eng.Certify(context.Background(), []domain.ChangedFile{{
		Path: "infra/main.tfplan.json", Status: domain.StatusAdded, Current: []byte(destroyPlan),
	}})
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	if withPlan.CatalogVersion == "" {
		t.Error("CatalogVersion is empty for a changeset that was classified against the catalog")
	}
	if withPlan.Grade != domain.GradeF {
		t.Errorf("Grade = %q, want F for a destroyed database", withPlan.Grade)
	}
}

// Two catalogs disagreeing about one type must produce two different digests, or a verdict could
// not be attributed to the classification that produced it.
func TestCatalogDigestReachesTheCertificate(t *testing.T) {
	t.Parallel()

	stateless, err := terraform.ParseCatalog([]byte(
		"catalog_version: \"test-1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATELESS\n    evidence: \"https://x\"\n"))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	stateful, err := terraform.ParseCatalog([]byte(
		"catalog_version: \"test-1\"\nprovider: p\nentries:\n  - type: aws_thing\n    class: STATEFUL\n    evidence: \"https://x\"\n"))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}

	files := []domain.ChangedFile{{
		Path:   "infra/main.tfplan.json",
		Status: domain.StatusAdded,
		Current: []byte(`{"format_version":"1.1","resource_changes":[
			{"address":"aws_thing.x","mode":"managed","type":"aws_thing",
			 "change":{"actions":["delete"],"before":{"id":"x"},"after":null}}]}`),
	}}

	certify := func(c *terraform.Catalog) domain.ReversibilityCertificate {
		tf, err := terraform.New(terraform.Options{Catalog: c})
		if err != nil {
			t.Fatalf("terraform.New: %v", err)
		}
		cert, err := engine.New([]analyzer.Analyzer{tf}).Certify(context.Background(), files)
		if err != nil {
			t.Fatalf("Certify: %v", err)
		}
		return cert
	}

	mild := certify(stateless)
	severe := certify(stateful)

	if mild.InputDigest == severe.InputDigest {
		t.Error("two catalogs that classify the same type differently produced one digest")
	}
	if mild.Grade == severe.Grade {
		t.Errorf("both catalogs graded %q; the classification change had no effect", mild.Grade)
	}
	if strings.Contains(string(mild.Grade), "F") && !strings.Contains(string(severe.Grade), "F") {
		t.Error("the stateful catalog graded better than the stateless one")
	}
}
