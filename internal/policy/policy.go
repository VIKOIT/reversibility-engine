// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy

import (
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Policy is a resolved .reversibility.yml.
//
// A nil *Policy is valid and means "no policy": every method is safe to call on it and none of
// them changes anything. That is what keeps the policy optional without every caller growing a
// branch.
type Policy struct {
	// Source is the file this came from, for messages. Empty for a policy built in memory.
	Source string

	// Gate is the minimum passing grade the policy asks for, or "" if it did not say.
	Gate domain.Grade

	Ignore    []string
	Waivers   []Waiver
	Overrides []Override

	// Digest is the SHA-256 over the resolved policy. It goes into the certificate so that a
	// verdict is attributable to the configuration that produced it as well as to the input.
	Digest string
}

// Waiver downgrades a matching finding to advisory.
//
// It never deletes one. A waived finding stays in the certificate, with the reason and the
// expiry date beside it, because silent suppression is how a safety tool stops being one.
type Waiver struct {
	// Rule is the rule ID this waiver covers, such as "PG012". Required.
	Rule string `json:"rule"`

	// Path is a glob limiting the waiver to certain files. Empty means every file, which is
	// almost never what anybody wants and is why the field is worth writing out.
	Path string `json:"path"`

	// Reason is why this risk was accepted. Required — a waiver nobody explained is
	// indistinguishable from a waiver nobody meant.
	Reason string `json:"reason"`

	// Expires is the last day the waiver applies, as YYYY-MM-DD. Required, and capped at
	// MaxWaiverWindow from the day the policy is parsed. A permanent waiver is a deleted rule
	// with extra steps.
	Expires string `json:"expires"`

	// ApprovedBy is who accepted the risk. Optional: it is documentation, and requiring it
	// would only produce a field filled in with "team".
	ApprovedBy string `json:"approved_by"`
}

// Override changes a rule's classification. It may only make one stricter.
type Override struct {
	Rule     string               `json:"rule"`
	Severity domain.Reversibility `json:"severity"`
}

// Decision is the outcome of applying a policy to a set of findings.
type Decision struct {
	// All is every finding with tightening overrides applied, waived ones included. It is what
	// the certificate's Grade and undo plan are computed from: a waiver accepts a risk, it does
	// not make the change reversible, so neither the grade nor the plan may pretend otherwise.
	All []domain.Finding

	// Findings are the findings that still count toward the effective grade — All minus the
	// waived ones.
	Findings []domain.Finding

	// Waived are the findings a live waiver covered. They do not count toward the effective
	// grade, and they still appear in the certificate.
	Waived []domain.WaivedFinding
}

// Ignores reports whether a path is excluded from analysis entirely.
//
// Ignoring happens before analysis rather than after: a file nobody wants graded should not be
// read, classified, and then filtered out, because the version that filters afterwards is one
// refactor away from forgetting to filter.
func (p *Policy) Ignores(path string) bool {
	if p == nil {
		return false
	}

	for _, pattern := range p.Ignore {
		if Match(pattern, path) {
			return true
		}
	}

	return false
}

// Apply resolves the policy against a set of findings as of the given day.
//
// Order matters and is fixed: overrides tighten first, then waivers are matched against the
// result. Waiving first would let a waiver written for a mild classification swallow a finding
// the operator had separately decided to treat as severe.
func (p *Policy) Apply(findings []domain.Finding, today time.Time) (Decision, error) {
	if p == nil {
		return Decision{All: findings, Findings: findings}, nil
	}

	tightened, err := p.applyOverrides(findings)
	if err != nil {
		return Decision{}, err
	}

	decision := Decision{All: tightened}

	for _, f := range tightened {
		waiver, ok := p.waiverFor(f, today)
		if !ok {
			decision.Findings = append(decision.Findings, f)
			continue
		}

		decision.Waived = append(decision.Waived, domain.WaivedFinding{
			Finding:    f,
			Reason:     waiver.Reason,
			Expires:    waiver.Expires,
			ApprovedBy: waiver.ApprovedBy,
		})
	}

	sort.SliceStable(decision.Waived, func(i, j int) bool {
		a, b := decision.Waived[i].Finding, decision.Waived[j].Finding
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.RuleID < b.RuleID
	})

	return decision, nil
}

// applyOverrides raises the classification of matching findings.
//
// An override that would lower one is a configuration error rather than a no-op. Ignoring it
// would leave somebody believing they had reclassified a rule, and the direction they were
// trying to move it in is the dangerous one.
func (p *Policy) applyOverrides(findings []domain.Finding) ([]domain.Finding, error) {
	if len(p.Overrides) == 0 {
		return findings, nil
	}

	out := make([]domain.Finding, 0, len(findings))

	for _, f := range findings {
		for _, o := range p.Overrides {
			if o.Rule != f.RuleID {
				continue
			}

			if o.Severity.Severity() < f.Reversibility.Severity() {
				return nil, fmt.Errorf(
					"%w: override for %s sets %s, which is weaker than the %s the analyzer found at %s; an override may only make a rule stricter",
					domain.ErrInvalidPolicy, o.Rule, o.Severity, f.Reversibility, f.File)
			}

			f.Reversibility = o.Severity
		}

		out = append(out, f)
	}

	return out, nil
}

// waiverFor returns the live waiver covering a finding, if any.
func (p *Policy) waiverFor(f domain.Finding, today time.Time) (Waiver, bool) {
	// A waiver may not reach a verdict the engine could not determine. UNKNOWN means nobody
	// understood the change, and accepting a risk nobody has characterised is not a decision
	// anyone is in a position to make. The same goes for a verdict the domain cannot even
	// read, which is a broken analyzer rather than a classified change.
	//
	// WILL_FAIL is excluded for a different reason: it is not a risk at all. A waiver accepts a
	// trade-off, and there is no trade-off in a statement that production state proves cannot
	// apply — waiving it would document a bug rather than accept one, and the pipeline it
	// unblocked would fail at deploy instead of at review.
	switch {
	case f.Reversibility == domain.ReversibilityUnknown,
		f.Reversibility == domain.ReversibilityWillFail,
		!f.Reversibility.Valid():
		return Waiver{}, false
	}

	for _, w := range p.Waivers {
		if w.Rule != f.RuleID {
			continue
		}
		if w.Path != "" && !Match(w.Path, f.File) {
			continue
		}
		if w.expired(today) {
			continue
		}
		return w, true
	}

	return Waiver{}, false
}

// expired reports whether the waiver has lapsed. An expired waiver is inert: the finding comes
// back on its own, with no edit and no announcement, which is the entire point of the date.
func (w Waiver) expired(today time.Time) bool {
	expires, err := time.Parse(dateLayout, w.Expires)
	if err != nil {
		// Unparseable dates are rejected at load time. Reaching here means the policy was
		// built in memory rather than loaded, and an expiry nobody can read is not one to
		// honour.
		return true
	}

	// The waiver covers the whole of its expiry day.
	return truncateToDay(today).After(expires)
}

func truncateToDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// IsPolicyFile reports whether a path is a policy file.
//
// The policy is configuration for this engine, not a Kubernetes manifest — but it is YAML, so
// the Kubernetes analyzer claims it and correctly reports K8S014/UNKNOWN for a document with no
// kind. The result was that adopting a policy graded your repository F because of the file you
// adopted it with. Excluding it is not a special case in the rules; it is the engine declining
// to analyze its own configuration.
func IsPolicyFile(p string) bool {
	return path.Base(p) == FileName
}
