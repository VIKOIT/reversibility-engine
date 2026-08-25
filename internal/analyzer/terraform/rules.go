// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform

import (
	"fmt"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// This file is the executable form of the authoritative table in docs/RULES.md §5:
//
//	TF001  delete of a stateful resource                 IRREVERSIBLE
//	TF002  forced replacement of a stateful resource     IRREVERSIBLE
//	TF003  RETIRED — never reused. See docs/RULES.md §5.
//	TF004  a recovery capability was switched off        IRREVERSIBLE
//	TF005  delete of a stateless resource                COSTLY
//	TF006  replacement of a stateless resource           COSTLY
//	TF007  in-place update                               REVERSIBLE
//	TF008  create                                        REVERSIBLE
//	TF009  unparseable plan or unknown format version    UNKNOWN
//	TF010  delete of an unclassified type                UNKNOWN
//
// THE CORE INSIGHT: only destruction is classified. A created or updated-in-place resource has a
// reverse by construction, which is what keeps the catalog finite — the problem was never
// "hundreds of AWS resource types", it is "the types whose destruction hurts". TF004 is the one
// deliberate exception, and it is bounded to a closed list of named paths.

// classification is one rule's verdict on one resource change.
type classification struct {
	ruleID        string
	reversibility domain.Reversibility
	rationale     string
	undo          domain.UndoStep

	// unclassifiedType is set only by TF010, and is what the growth loop collects to build one
	// aggregated policy snippet for the whole plan.
	unclassifiedType string
}

// classify maps one resource change onto exactly one rule.
func (a *Analyzer) classify(rc resourceChange) (classification, bool) {
	// Data sources are reads. They create nothing, destroy nothing, and have no reverse to
	// describe.
	if rc.Mode == "data" {
		return classification{}, false
	}

	switch {
	case rc.Change.destroys():
		return a.classifyDestroy(rc), true

	case rc.Change.updates():
		return a.classifyUpdate(rc), true

	case rc.Change.creates():
		return classification{
			ruleID:        "TF008",
			reversibility: domain.ReversibilityReversible,
			rationale:     fmt.Sprintf("Creating %s can be undone by destroying it, which is the reverse Terraform already generates.", rc.Address),
			undo:          domain.UndoStep(fmt.Sprintf("terraform destroy -target=%s", rc.Address)),
		}, true

	default:
		// no-op and read. Nothing changes, so there is nothing to classify.
		return classification{}, false
	}
}

// classifyDestroy handles every action containing "delete", which is the only shape that can
// lose anything.
//
// The order here is the layering: evidence from the plan first, the catalog second, and neither
// found means UNKNOWN. Evidence may only raise — a resource the catalog calls STATELESS becomes
// STATEFUL on evidence, and one the catalog calls STATEFUL is never talked down by its absence.
func (a *Analyzer) classifyDestroy(rc resourceChange) classification {
	replacement := rc.Change.replaces()

	// A safety mechanism the author explicitly switched off, on an object now being destroyed.
	// This outranks the class entirely: whatever the type is, it was configured to leave
	// nothing behind.
	if key, disabled := hasDisabledSafety(rc.Change.Before); disabled {
		return classification{
			ruleID:        pick(replacement, "TF002", "TF001"),
			reversibility: domain.ReversibilityIrreversible,
			rationale: fmt.Sprintf(
				"%s is being %s with %s set, so the mechanism that would have preserved its contents was explicitly disabled and nothing will remain to restore from.",
				rc.Address, destroyVerb(replacement), key),
		}
	}

	class, source, known := a.classOf(rc)
	if !known {
		return classification{
			ruleID:        "TF010",
			reversibility: domain.ReversibilityUnknown,
			rationale: fmt.Sprintf(
				"%s is being %s, and the resource type %s is not in the catalog, so whether destroying it loses anything is unknown.",
				rc.Address, destroyVerb(replacement), rc.Type),
			unclassifiedType: rc.Type,
		}
	}

	if class == ClassStateful {
		return classification{
			ruleID:        pick(replacement, "TF002", "TF001"),
			reversibility: domain.ReversibilityIrreversible,
			rationale: fmt.Sprintf(
				"%s is being %s. %s is stateful (%s): destroying it destroys data, an identity that re-applying cannot recreate, or a recovery capability a rollback would need.",
				rc.Address, destroyVerb(replacement), rc.Type, source),
		}
	}

	return classification{
		ruleID:        pick(replacement, "TF006", "TF005"),
		reversibility: domain.ReversibilityCostly,
		rationale: fmt.Sprintf(
			"%s is being %s. %s is stateless (%s), so re-applying recreates it — but everything depending on it is broken until that happens.",
			rc.Address, destroyVerb(replacement), rc.Type, source),
		undo: domain.UndoStep(fmt.Sprintf("terraform apply -target=%s", rc.Address)),
	}
}

// classifyUpdate is TF007 for everything except the closed TF004 list.
//
// Disabling deletion protection is trivially reversible in itself — set it back to true — so it
// is graded IRREVERSIBLE on the third clause of the discriminator rather than the first: what
// was destroyed is a recovery capability a later rollback would have depended on. It is the same
// family as deleting a snapshot, and the rationale says so, because a user reading an F on a
// one-line boolean change is owed that explanation.
func (a *Analyzer) classifyUpdate(rc resourceChange) classification {
	for _, t := range safetyTransitions {
		if !t.fires(rc.Change.Before, rc.Change.After) {
			continue
		}

		return classification{
			ruleID:        "TF004",
			reversibility: domain.ReversibilityIrreversible,
			rationale: fmt.Sprintf(
				"On %s, %s. The setting itself is one line to restore, but the recovery capability it protected is not — this is graded with deleting a snapshot rather than with an ordinary update.",
				rc.Address, t.why),
		}
	}

	return classification{
		ruleID:        "TF007",
		reversibility: domain.ReversibilityReversible,
		rationale:     fmt.Sprintf("Updating %s in place changes no identity and destroys nothing; re-applying the previous configuration reverses it.", rc.Address),
		undo:          domain.UndoStep(fmt.Sprintf("terraform apply -target=%s", rc.Address)),
	}
}

// classOf resolves a resource type to a class, and names where the answer came from.
//
// Layer 1 before Layer 2 before Layer 3's overrides being merged in at construction: evidence in
// the plan wins, then the resolved catalog.
func (a *Analyzer) classOf(rc resourceChange) (class Class, source string, known bool) {
	if key, ok := hasStatefulEvidence(rc.Change.Before); ok {
		return ClassStateful, fmt.Sprintf("the plan shows %s on it", key), true
	}

	if entry, ok := a.catalog.Lookup(rc.Type); ok {
		return entry.Class, a.sourceOf(rc.Type), true
	}

	return "", "", false
}

// sourceOf names whether a classification came from the catalog or from the user's policy, so a
// finding can be traced to the decision behind it.
func (a *Analyzer) sourceOf(resourceType string) string {
	if _, ok := a.overrides[resourceType]; ok {
		return "classified in .reversibility.yml"
	}
	return fmt.Sprintf("catalog %s", a.catalog.Version)
}

func destroyVerb(replacement bool) string {
	if replacement {
		return "replaced, which destroys the existing object before creating its successor"
	}
	return "destroyed"
}

func pick(cond bool, whenTrue, whenFalse string) string {
	if cond {
		return whenTrue
	}
	return whenFalse
}
