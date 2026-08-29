// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package terraform classifies Terraform plan JSON.
//
// It reads `terraform show -json` output and NEVER terraform.tfstate. State files hold provider
// credentials and resource attributes in plaintext; a plan does not, and there is no code path
// here that opens one.
//
// Only destruction is classified. A resource being created or updated in place has a reverse by
// construction, which is what keeps the catalog finite: the problem was never "hundreds of AWS
// resource types", it is "the resource types whose destruction hurts".
package terraform

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/catalog"
	"github.com/VIKOIT/reversibility-engine/internal/analyzer"
	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// PlanSuffix is the filename convention the analyzer claims.
//
// Deliberately narrow. Claiming plan.json would grade F on any repository that happens to have
// one, because a file this analyzer claims and cannot read is UNKNOWN — and being fail-closed
// about unreadable input only works if the input was actually meant for it. A user whose plan is
// named otherwise passes --terraform-plan rather than renaming their file.
const PlanSuffix = ".tfplan.json"

// Override is a user classification from .reversibility.yml.
type Override struct {
	Type  string
	Class Class
}

// Options configures the analyzer.
type Options struct {
	// Catalog replaces the embedded one. Tests use it; nothing else should.
	Catalog *Catalog

	// Overrides are the user's terraform_types entries. They may classify a type the catalog
	// does not know, and may tighten one it does. They may never loosen one.
	Overrides []Override

	// ExtraPlanPaths are files to claim regardless of their name, from --terraform-plan.
	//
	// **They must already be in the decision namespace.** The CLI resolves what the user typed
	// through provider.QualifyPath before it gets here, because resolving a path against the
	// filesystem is I/O and an analyzer does none. Handing this a raw command-line argument is
	// how the suffix-matching workaround came to exist.
	ExtraPlanPaths []string
}

// Analyzer implements analyzer.Analyzer for Terraform plans.
//
// It holds no mutable state: the catalog and the overrides are resolved at construction, and
// everything else lives for the duration of one Analyze call.
type Analyzer struct {
	catalog   *Catalog
	overrides map[string]Class
	extra     map[domain.Located]bool
}

// New returns an analyzer, or an error if the configuration is not permitted.
//
// It returns an error — unlike the other analyzers' constructors — because Layer 3's asymmetry
// is a configuration rule, and a configuration error must stop the run rather than surface later
// as a finding. A user who tried to weaken a classification needs to be told so, not quietly
// obeyed or quietly ignored.
func New(opts Options) (*Analyzer, error) {
	c := opts.Catalog
	if c == nil {
		var err error
		if c, err = LoadEmbedded(catalog.AWS); err != nil {
			return nil, fmt.Errorf("loading the embedded catalog: %w", err)
		}
	}

	a := &Analyzer{
		catalog:   c,
		overrides: map[string]Class{},
		extra:     map[domain.Located]bool{},
	}

	for _, o := range opts.Overrides {
		if err := a.applyOverride(o); err != nil {
			return nil, err
		}
	}

	for _, p := range opts.ExtraPlanPaths {
		a.extra[domain.Located(normalizePath(p))] = true
	}

	return a, nil
}

// applyOverride merges one user classification, enforcing that it may only tighten.
//
// Loosening is a configuration error rather than a silent no-op. The path for "this rule is
// wrong about my infrastructure" is a waiver — which carries a reason and an expiry, and which
// per the S10 ruling changes the gate decision and never the grade. An override that could
// weaken a classification would be a waiver with none of those properties.
func (a *Analyzer) applyOverride(o Override) error {
	if strings.TrimSpace(o.Type) == "" {
		return fmt.Errorf("%w: a terraform_types entry names no type", domain.ErrInvalidPolicy)
	}
	if !o.Class.Valid() {
		return fmt.Errorf("%w: terraform_types entry for %s has class %q; want %s or %s",
			domain.ErrInvalidPolicy, o.Type, o.Class, ClassStateful, ClassStateless)
	}

	if existing, ok := a.catalog.Lookup(o.Type); ok && !o.Class.AtLeastAsSevereAs(existing.Class) {
		return fmt.Errorf(
			"%w: terraform_types reclassifies %s from %s to %s, which weakens it. An override may only tighten a classification; to accept the risk on a specific plan, use a waiver, which carries a reason and an expiry",
			domain.ErrInvalidPolicy, o.Type, existing.Class, o.Class)
	}

	a.overrides[o.Type] = o.Class
	a.catalog.byType[o.Type] = Entry{
		Type:     o.Type,
		Class:    o.Class,
		Evidence: "classified in .reversibility.yml",
	}

	return nil
}

// Name implements analyzer.Analyzer.
func (a *Analyzer) Name() string { return "terraform" }

// Supports implements analyzer.Analyzer.
//
// **The comparison against `--terraform-plan` is exact**, and it can be because both sides are
// now in the same namespace: the CLI resolves what the user typed through
// `provider.QualifyPath`, and the engine hands this the file's `domain.Located`.
//
// It used to be a bidirectional suffix match, with a comment saying the two spellings "differ".
// They did, and that was the Django P0 in a fourth costume — a workaround for a missing
// namespace rather than a namespace. It also over-claimed in a way nobody had cause to notice:
// `--terraform-plan a/plan.json` matched `b/a/plan.json` as well, so naming one plan could hand
// the Terraform analyzer a file that was never named, and a changeset that should have been
// UNKNOWN got graded against a catalog.
func (a *Analyzer) Supports(at domain.Located) bool {
	if strings.HasSuffix(string(at), PlanSuffix) {
		return true
	}

	return a.extra[at]
}

func normalizePath(p string) string {
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}

// CatalogVersion implements analyzer.CatalogVersioner.
func (a *Analyzer) CatalogVersion() string { return a.catalog.Version }

// CatalogDigest implements analyzer.CatalogVersioner.
func (a *Analyzer) CatalogDigest() string { return a.catalog.Digest() }

// Analyze implements analyzer.Analyzer.
//
// Removed plan files are skipped: deleting the plan document from a repository is not itself a
// change to any infrastructure, and classifying its former contents would report a destruction
// nobody proposed.
func (a *Analyzer) Analyze(ctx context.Context, files []domain.ChangedFile) ([]domain.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("terraform analyzer: %w", err)
	}

	// Sorted, so two runs over one changeset produce findings in one order regardless of how
	// the provider happened to return the files.
	sorted := append([]domain.ChangedFile(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var out []domain.Finding

	for _, f := range sorted {
		if !a.Supports(f.Location()) || f.IsRemoved() {
			continue
		}
		out = append(out, a.analyzeFile(f)...)
	}

	return out, nil
}

// analyzeFile classifies one plan document.
func (a *Analyzer) analyzeFile(f domain.ChangedFile) []domain.Finding {
	p, err := decodePlan(f.Current)
	if err != nil {
		// Fail closed. A plan this build cannot read is reported, never skipped and never
		// assumed harmless.
		return []domain.Finding{{
			RuleID:        "TF009",
			File:          f.Path,
			Line:          0,
			Statement:     analyzer.NormalizeStatement(firstLine(f.Current)),
			Reversibility: domain.ReversibilityUnknown,
			LockHazard:    domain.LockNone,
			Rationale:     fmt.Sprintf("This Terraform plan could not be read, so nothing in it can be classified: %v.", err),
		}}
	}

	changes := append([]resourceChange(nil), p.ResourceChanges...)
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Address < changes[j].Address })

	out := make([]domain.Finding, 0, len(changes))
	for _, rc := range changes {
		c, ok := a.classify(rc)
		if !ok {
			continue
		}

		out = append(out, domain.Finding{
			RuleID: c.ruleID,
			File:   f.Path,

			// A plan is generated JSON. Line 0 means the finding is about the file as a whole,
			// which is honest: sending a reviewer to a line of machine output helps nobody.
			Line:          0,
			Statement:     analyzer.NormalizeStatement(fmt.Sprintf("%s (%s)", rc.Address, strings.Join(rc.Change.Actions, "+"))),
			Reversibility: c.reversibility,

			// Terraform takes no database lock. NONE is the truth, not a default.
			LockHazard: domain.LockNone,
			Rationale:  c.rationale,
			UndoStep:   c.undo,

			// Relation carries the resource type so the growth loop can collect unclassified
			// ones without re-parsing anything.
			Subject: domain.Subject{Relation: rc.Type, Object: rc.Address},
		})
	}

	return out
}

func firstLine(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
