// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package terraform

import (
	"sort"
	"strings"
)

// statefulEvidenceKeys are attributes whose presence on a resource marks it STATEFUL, whatever
// the catalog says or does not say.
//
// This is Layer 1 and it is checked BEFORE the catalog, so a type nobody has classified is still
// caught when it plainly holds something. Evidence may only RAISE severity: absence of evidence
// never implies stateless, it implies nothing at all.
//
// THE TYPE NAME IS NEVER MATCHED. aws_db_subnet_group contains "db" and holds nothing; names
// lie, and a regex over them would be a classification nobody could audit.
var statefulEvidenceKeys = []string{
	"allocated_storage",
	"backup_retention_period",
	"deletion_protection",
	"final_snapshot_identifier",
	"kms_key_id",
	"point_in_time_recovery",
	"snapshot_identifier",
	"storage_encrypted",
	"versioning",

	// Present and non-empty only on an instance that actually carries instance-store volumes.
	// aws_instance is catalogued STATELESS because a rule firing on every ordinary plan gets
	// the tool switched off — and this key is what lets the instances that do hold data raise
	// themselves without dragging the cattle along.
	"ephemeral_block_device",

	// Present and non-empty only on an IMPORTED certificate. A DNS-validated certificate is
	// re-issuable and stays quiet; one whose private key you may no longer hold does not.
	"private_key",
}

// disabledSafetyKeys elevate a destroyed resource straight to IRREVERSIBLE whatever its class,
// because the author explicitly switched off the mechanism that would have made it recoverable.
//
// These are checked on the BEFORE side of a destroy: the object being destroyed was already
// configured to leave nothing behind.
var disabledSafetyKeys = []string{
	"force_destroy",
	"skip_final_snapshot",
}

// safetyTransition is one entry of the TF004 closed list: a named path, and the transition on it
// that destroys a recovery capability.
//
// A NAMED PATH LIST, NOT RECURSION. A list of exact paths is auditable — a reader can check it
// against this table — and general recursion into a provider-defined object is not. Anything not
// named here is an ordinary in-place update and stays TF007/REVERSIBLE.
type safetyTransition struct {
	// path is the attribute, dotted. Two entries reach one level into a block; those are the
	// only nesting this list permits.
	path []string

	// from and to describe the transition that matters. A change in the other direction, or
	// between any other pair of values, is not this rule.
	fromTrue bool // true -> false is the dangerous direction
	toTrue   bool // false -> true is the dangerous direction
	fromPos  bool // a positive number -> 0 is the dangerous direction

	// why is the sentence the finding carries. It says what capability was destroyed, because a
	// user reading an F on a one-line boolean change has to be told why it belongs in the same
	// family as deleting a snapshot.
	why string
}

// safetyTransitions is the complete TF004 list. Nothing outside it fires TF004.
var safetyTransitions = []safetyTransition{
	{
		path:     []string{"deletion_protection"},
		fromTrue: true,
		why:      "deletion protection was switched off, so the guard that would have refused a later destroy is gone",
	},
	{
		path:     []string{"enable_deletion_protection"},
		fromTrue: true,
		why:      "deletion protection was switched off, so the guard that would have refused a later destroy is gone",
	},
	{
		path:   []string{"skip_final_snapshot"},
		toTrue: true,
		why:    "the final snapshot was disabled, so destroying this resource will now leave nothing to restore from",
	},
	{
		path:   []string{"force_destroy"},
		toTrue: true,
		why:    "force_destroy was enabled, so a later destroy will delete the contents rather than refuse",
	},
	{
		path:    []string{"backup_retention_period"},
		fromPos: true,
		why:     "automated backups were switched off, so the point-in-time recovery a rollback would have used no longer exists",
	},
	{
		path:    []string{"deletion_window_in_days"},
		fromPos: true,
		why:     "the key deletion waiting period was removed, so the window in which a scheduled deletion could be cancelled is gone",
	},
	{
		// Nested, and one of exactly two paths permitted to be.
		path:     []string{"versioning", "enabled"},
		fromTrue: true,
		why:      "bucket versioning was switched off, so overwritten and deleted objects are no longer recoverable",
	},
	{
		path:     []string{"point_in_time_recovery", "enabled"},
		fromTrue: true,
		why:      "point-in-time recovery was switched off, so the table can no longer be restored to an earlier moment",
	},
}

// hasStatefulEvidence reports whether the before object carries an attribute that marks the
// resource as holding something, and names the attribute that said so.
//
// Presence means PRESENT AND MEANINGFULLY SET. A plan emits a resource's whole schema, so a key
// that exists but is null, empty, or an empty collection says only that the provider defines the
// attribute — not that this resource uses it. Treating an empty ephemeral_block_device as
// evidence would raise every EC2 instance to STATEFUL and undo the reason aws_instance is
// catalogued STATELESS at all.
//
// A false boolean or a zero number IS evidence: the attribute existing at all is the schema
// signal that this type has storage, backups, or protection to speak of.
func hasStatefulEvidence(before map[string]any) (string, bool) {
	if before == nil {
		return "", false
	}

	// Sorted, so two runs over the same plan name the same attribute.
	keys := append([]string(nil), statefulEvidenceKeys...)
	sort.Strings(keys)

	for _, key := range keys {
		value, present := before[key]
		if present && meaningfullySet(value) {
			return key, true
		}
	}
	return "", false
}

// hasDisabledSafety reports whether the destroyed object had a safety mechanism explicitly
// switched off, and names it.
func hasDisabledSafety(before map[string]any) (string, bool) {
	if before == nil {
		return "", false
	}

	keys := append([]string(nil), disabledSafetyKeys...)
	sort.Strings(keys)

	for _, key := range keys {
		if enabled, ok := before[key].(bool); ok && enabled {
			return key, true
		}
	}
	return "", false
}

// meaningfullySet reports whether a value says the attribute is in use.
//
// null, "", [], and {} do not. false and 0 do — see hasStatefulEvidence.
func meaningfullySet(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// lookupPath resolves a named path in an object.
//
// A single-element list is unwrapped on the way through, because Terraform emits a MaxItems:1
// block — which is how versioning and point_in_time_recovery are written — as a list of one
// object. Nothing else about this is general: it walks the exact names it was given.
func lookupPath(obj map[string]any, path []string) (any, bool) {
	var current any = obj

	for _, key := range path {
		if list, ok := current.([]any); ok {
			if len(list) == 0 {
				return nil, false
			}
			current = list[0]
		}

		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}

		current, ok = m[key]
		if !ok {
			return nil, false
		}
	}

	return current, true
}

// fires reports whether this transition happened between the two sides of an update.
func (t safetyTransition) fires(before, after map[string]any) bool {
	from, hadBefore := lookupPath(before, t.path)
	to, hasAfter := lookupPath(after, t.path)
	if !hadBefore || !hasAfter {
		return false
	}

	switch {
	case t.fromTrue:
		wasOn, okBefore := from.(bool)
		isOn, okAfter := to.(bool)
		return okBefore && okAfter && wasOn && !isOn

	case t.toTrue:
		wasOn, okBefore := from.(bool)
		isOn, okAfter := to.(bool)
		return okBefore && okAfter && !wasOn && isOn

	case t.fromPos:
		wasSet, okBefore := asNumber(from)
		isSet, okAfter := asNumber(to)
		return okBefore && okAfter && wasSet > 0 && isSet == 0
	}

	return false
}

// asNumber reads a JSON number, which encoding/json gives as float64.
func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

// SuggestClass proposes a classification from a provider schema's attribute names.
//
// It exists for `revctl catalog scan`, which turns catalog maintenance from research into
// review. It uses exactly the evidence keys the analyzer uses at runtime, so a reviewer is
// checking the same signal the tool would have acted on rather than a second heuristic that
// could disagree with the first.
//
// The suggestion is never authoritative and nothing merges it automatically: a candidate has no
// evidence link, and an entry without one fails the build.
func SuggestClass(attributes []string) (Class, string) {
	present := make(map[string]bool, len(attributes))
	for _, a := range attributes {
		present[a] = true
	}

	matched := make([]string, 0, 2)
	for _, key := range statefulEvidenceKeys {
		if present[key] {
			matched = append(matched, key)
		}
	}
	sort.Strings(matched)

	if len(matched) > 0 {
		return ClassStateful, "schema declares " + strings.Join(matched, ", ")
	}
	return ClassStateless, ""
}
