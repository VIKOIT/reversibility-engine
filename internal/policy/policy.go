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

	// TerraformTypes classify Terraform resource types the catalog does not know, or tighten
	// ones it does. The asymmetry — classify or tighten, never loosen — is enforced by the
	// Terraform analyzer, which is the only thing that can see the catalog to compare against.
	TerraformTypes []TerraformType

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

// TerraformType classifies a Terraform resource type.
//
// Permitted: naming a type the catalog does not carry, or tightening one it does. Prohibited:
// weakening a catalog classification — that path is a waiver, which carries a reason and an
// expiry and which changes the gate decision rather than the grade.
type TerraformType struct {
	Type  string `json:"type"`
	Class string `json:"class"`
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

	// DeadWaivers are waivers that covered no finding in this changeset, described for a human.
	//
	// A waiver matching nothing is indistinguishable, from the outside, from a waiver that has
	// simply not been needed yet — so this is reported rather than inferred, and it is worded
	// as an observation rather than an accusation. It is not an error: a waiver written for a
	// rule that did not fire on this pull request is doing exactly what it should.
	//
	// It exists because a waiver whose `path:` glob was written against the repository and
	// evaluated against a changeset rooted below it matched nothing, silently, and the operator
	// who wrote it believed a risk had been accepted.
	DeadWaivers []string
}

// Ignores reports whether a path is excluded from analysis entirely.
//
// Ignoring happens before analysis rather than after: a file nobody wants graded should not be
// read, classified, and then filtered out, because the version that filters afterwards is one
// refactor away from forgetting to filter.
// The path is a domain.Located: an ignore glob is written about where a file sits in the
// repository, and matching it against the changeset's spelling made the whole list inert
// whenever the analysis root was named below the repository root. See Match.
func (p *Policy) Ignores(at domain.Located) bool {
	if p == nil {
		return false
	}

	for _, pattern := range p.Ignore {
		if Match(pattern, at) {
			return true
		}
	}

	return false
}

// IgnoreMatcher tracks which ignore patterns actually matched something.
//
// **A pattern that matches nothing is dead config, and dead config in a safety tool reads as
// protection the user does not have.** It is the same requirement as naming unanalyzed files:
// never let the reader infer. An ignore list is most often wrong in exactly the way that is
// invisible — a glob written against the repository's layout, evaluated against a changeset
// rooted somewhere else — so the check is not hypothetical, it is how this defect was found.
//
// A nil *Policy yields a matcher with nothing to report, so callers need no branch.
type IgnoreMatcher struct {
	policy  *Policy
	matched map[string]bool
}

// Matcher returns an IgnoreMatcher over this policy's ignore list.
func (p *Policy) Matcher() *IgnoreMatcher {
	return &IgnoreMatcher{policy: p, matched: map[string]bool{}}
}

// Ignores reports whether the path is excluded, recording which pattern excluded it.
func (m *IgnoreMatcher) Ignores(at domain.Located) bool {
	if m == nil || m.policy == nil {
		return false
	}

	ignored := false
	for _, pattern := range m.policy.Ignore {
		if Match(pattern, at) {
			m.matched[pattern] = true
			// Every matching pattern is recorded, not just the first. Stopping early would
			// report a second pattern covering the same file as dead when it is merely
			// redundant, and "your config does nothing" is too strong a claim to get wrong.
			ignored = true
		}
	}
	return ignored
}

// Dead returns the ignore patterns that matched no path at all, in the order they were written.
func (m *IgnoreMatcher) Dead() []string {
	if m == nil || m.policy == nil {
		return nil
	}

	var out []string
	for _, pattern := range m.policy.Ignore {
		if !m.matched[pattern] {
			out = append(out, pattern)
		}
	}
	return out
}

// Apply resolves the policy against a set of findings as of the given day.
//
// Order matters and is fixed: overrides tighten first, then waivers are matched against the
// result. Waiving first would let a waiver written for a mild classification swallow a finding
// the operator had separately decided to treat as severe.
// locate maps a finding's file into the namespace a waiver's `path:` glob is written in. A nil
// locator means the findings are already there, which is what domain.Identity says out loud.
func (p *Policy) Apply(findings []domain.Finding, today time.Time, locate domain.Locator) (Decision, error) {
	if p == nil {
		return Decision{All: findings, Findings: findings}, nil
	}

	if locate == nil {
		locate = domain.Identity()
	}

	tightened, err := p.applyOverrides(findings)
	if err != nil {
		return Decision{}, err
	}

	decision := Decision{All: tightened}
	fired := map[int]bool{}

	for _, f := range tightened {
		waiver, index, ok := p.waiverFor(f, today, locate)
		if !ok {
			decision.Findings = append(decision.Findings, f)
			continue
		}
		fired[index] = true

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

	// Reported in the order they are written in the file, so a reader can find the line.
	for i, w := range p.Waivers {
		if fired[i] {
			continue
		}
		decision.DeadWaivers = append(decision.DeadWaivers, w.describe())
	}

	return decision, nil
}

// describe names a waiver the way its author would recognise it.
//
// The rule and the path, because those are what they typed; not the reason, which can be a
// paragraph, and not the expiry, which is not why it matched nothing.
func (w Waiver) describe() string {
	if w.Path == "" {
		return w.Rule + " (every file)"
	}
	return w.Rule + " at " + w.Path
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

// waiverFor returns the live waiver covering a finding, its index in the policy, and whether one
// was found.
//
// The index is returned so Apply can report the waivers that fired nothing without matching them
// a second time by value — two waivers may be textually identical, and reporting one of them as
// dead because its twin fired would be a lie about which line is doing the work.
func (p *Policy) waiverFor(f domain.Finding, today time.Time, locate domain.Locator) (Waiver, int, bool) {
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
		return Waiver{}, 0, false
	}

	for i, w := range p.Waivers {
		if w.Rule != f.RuleID {
			continue
		}
		if w.Path != "" && !Match(w.Path, locate(f.File)) {
			continue
		}
		if w.expired(today) {
			continue
		}
		return w, i, true
	}

	return Waiver{}, 0, false
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
