package kubernetes

import (
	"fmt"

	"github.com/abdo-s1/reversibility-engine/internal/domain"
)

// This file is the executable form of the authoritative Kubernetes table in CLAUDE.md §10.
//
// Per the owner's ruling in §15.2, every finding here carries LockHazard NONE: Kubernetes
// changes do not hold database locks. That is asserted by the tests, not just intended.

// change is one object as it appears on both sides of a changeset.
type change struct {
	key objectKey

	// old is nil when the object is being added; new is nil when it is being removed.
	old *object
	new *object

	// inChangeset distinguishes an object the pull request actually touches from one supplied
	// only as context. Context objects are needed to answer questions about the changed ones
	// — K8S003 needs a StorageClass nobody edited — but they must not generate findings of
	// their own, or every run would report the entire cluster.
	inChangeset bool
}

func (c change) added() bool   { return c.old == nil && c.new != nil }
func (c change) removed() bool { return c.old != nil && c.new == nil }

// object returns whichever side of the change exists, preferring the new one.
func (c change) object() *object {
	if c.new != nil {
		return c.new
	}
	return c.old
}

// index is the set of objects on one side of the changeset, for the rules that must look beyond
// the object they are classifying.
type index map[objectKey]*object

// finding builds a Kubernetes finding. Lock hazard is not a parameter because it is always NONE.
func (c change) finding(ruleID string, rev domain.Reversibility, rationale string, undo domain.UndoStep) domain.Finding {
	return domain.Finding{
		RuleID: ruleID,
		File:   c.object().file,

		// Line 0 means the finding is a property of the whole object rather than of one line.
		// A structural diff has no single line to blame, and inventing one would send readers
		// to the wrong place.
		Line: 0,

		Statement:     c.object().describe(),
		Reversibility: rev,
		LockHazard:    domain.LockNone,
		Rationale:     rationale,
		UndoStep:      undo,
	}
}

// classify applies every rule to one changed object.
//
// A changed object that matches no rule yields K8S014/UNKNOWN. That is the Kubernetes analogue
// of PG027: silence about a change the engine does not understand would be indistinguishable
// from a safe change, and the whole product rests on those two not being confused.
func classify(c change, oldIndex, newIndex index) []domain.Finding {
	obj := c.object()

	if !obj.isRecognized() {
		return []domain.Finding{c.finding(
			"K8S014",
			domain.ReversibilityUnknown,
			fmt.Sprintf("Kind %q has no classification rule, and a resource the engine does not understand may own anything, so its reversibility cannot be vouched for.", obj.kind),
			"",
		)}
	}

	var findings []domain.Finding

	// Order is fixed so that identical input produces an identical certificate.
	for _, rule := range []func(change, index, index) []domain.Finding{
		ruleVolumeClaimTemplates,  // K8S001
		ruleSelector,              // K8S002
		rulePVCRemoved,            // K8S003
		rulePVCStorageDecreased,   // K8S004
		rulePVCStorageClass,       // K8S005
		ruleNamespaceOrCRDRemoved, // K8S006
		ruleServiceChanged,        // K8S007
		ruleImageNotPinned,        // K8S008
		ruleConfigRemoved,         // K8S009
		ruleRecreateStrategy,      // K8S010
		ruleProbeRemoved,          // K8S011
		ruleTuningChanged,         // K8S012
		ruleNewWorkload,           // K8S013
		ruleImageChangedPinned,    // K8S015
	} {
		findings = append(findings, rule(c, oldIndex, newIndex)...)
	}

	if len(findings) == 0 {
		return []domain.Finding{c.finding(
			"K8S014",
			domain.ReversibilityUnknown,
			fmt.Sprintf("%s changed in a way no classification rule covers, so whether the change can be rolled back is unknown.", obj.describe()),
			"",
		)}
	}

	return findings
}

// K8S001: volumeClaimTemplates is immutable on a live StatefulSet.
func ruleVolumeClaimTemplates(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || c.new.kind != "StatefulSet" {
		return nil
	}
	if equalAt(c.old.doc, c.new.doc, "spec", "volumeClaimTemplates") {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S001",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("volumeClaimTemplates is immutable on a live StatefulSet, so applying this to %s does not resize anything: it forces a recreate that orphans or destroys the existing volumes, and reverting the manifest does not bring them back.", c.new.describe()),
		"",
	)}
}

// K8S002: spec.selector is immutable on a workload.
func ruleSelector(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || !c.new.isWorkload() {
		return nil
	}
	if equalAt(c.old.doc, c.new.doc, "spec", "selector") {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S002",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("spec.selector is immutable, so this apply is either rejected outright or forces a recreate that orphans the existing ReplicaSet of %s, leaving no owner able to roll it back.", c.new.describe()),
		"",
	)}
}

// K8S003: removing a PVC whose StorageClass reclaims by deletion destroys the volume.
func rulePVCRemoved(c change, oldIndex, _ index) []domain.Finding {
	if !c.removed() || c.old.kind != "PersistentVolumeClaim" {
		return nil
	}

	className := stringAt(c.old.doc, "spec", "storageClassName")
	policy, resolved := reclaimPolicy(oldIndex, className)

	// Per CLAUDE.md §10 the rule fires when the policy is Delete *or unknown*. Only an
	// explicit Retain is safe; an unresolvable StorageClass is treated exactly like Delete.
	if resolved && policy == "Retain" {
		return nil
	}

	why := fmt.Sprintf("its StorageClass %q reclaims volumes by deleting them", className)
	if !resolved {
		why = "its StorageClass could not be resolved from the changeset, and an unknown reclaim policy is treated as Delete"
	}

	return []domain.Finding{c.finding(
		"K8S003",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("Removing %s destroys the underlying volume because %s; re-applying the claim provisions an empty one.", c.old.describe(), why),
		"",
	)}
}

// reclaimPolicy looks up a StorageClass by name across the changeset.
func reclaimPolicy(idx index, className string) (policy string, resolved bool) {
	if className == "" {
		return "", false
	}

	for key, obj := range idx {
		if key.kind == "StorageClass" && key.name == className {
			p := stringAt(obj.doc, "reclaimPolicy")
			if p == "" {
				return "", false
			}
			return p, true
		}
	}
	return "", false
}

// K8S004: Kubernetes cannot shrink a bound volume.
func rulePVCStorageDecreased(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || c.new.kind != "PersistentVolumeClaim" {
		return nil
	}

	path := []string{"spec", "resources", "requests", "storage"}

	oldQ, oldOK, oldErr := quantityAt(c.old.doc, path...)
	newQ, newOK, newErr := quantityAt(c.new.doc, path...)

	if oldErr != nil || newErr != nil {
		// A quantity that cannot be read cannot be compared. Claiming the volume shrank would
		// be a false accusation; claiming it did not would be worse.
		return []domain.Finding{c.finding(
			"K8S014",
			domain.ReversibilityUnknown,
			fmt.Sprintf("The storage request on %s could not be read, so whether it shrank is unknown.", c.new.describe()),
			"",
		)}
	}
	if !oldOK || !newOK || newQ >= oldQ {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S004",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("The storage request on %s decreased, and Kubernetes cannot shrink a bound volume: the request is either rejected or satisfied by a replacement volume that does not carry the original data.", c.new.describe()),
		"",
	)}
}

// K8S005: storageClassName is immutable on a bound PVC.
func rulePVCStorageClass(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || c.new.kind != "PersistentVolumeClaim" {
		return nil
	}
	if equalAt(c.old.doc, c.new.doc, "spec", "storageClassName") {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S005",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("storageClassName is immutable on a bound claim, so changing it on %s means a new volume, and the data on the old one does not follow.", c.new.describe()),
		"",
	)}
}

// K8S006: deleting a Namespace or a CRD cascades to everything inside it.
func ruleNamespaceOrCRDRemoved(c change, _, _ index) []domain.Finding {
	if !c.removed() {
		return nil
	}
	if c.old.kind != "Namespace" && c.old.kind != "CustomResourceDefinition" {
		return nil
	}

	what := "every object inside it"
	if c.old.kind == "CustomResourceDefinition" {
		what = "every custom resource of that kind"
	}

	return []domain.Finding{c.finding(
		"K8S006",
		domain.ReversibilityIrreversible,
		fmt.Sprintf("Deleting %s cascades to %s, which is among the largest blast radii in Kubernetes and is not undone by re-applying the manifest.", c.old.describe(), what),
		"",
	)}
}

// K8S007: a Service's identity and its external address are not restored by reverting.
func ruleServiceChanged(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || c.new.kind != "Service" {
		return nil
	}

	typeChanged := !equalAt(c.old.doc, c.new.doc, "spec", "type")
	ipChanged := !equalAt(c.old.doc, c.new.doc, "spec", "clusterIP")
	if !typeChanged && !ipChanged {
		return nil
	}

	what := "spec.type"
	switch {
	case typeChanged && ipChanged:
		what = "spec.type and spec.clusterIP"
	case ipChanged:
		what = "spec.clusterIP"
	}

	return []domain.Finding{c.finding(
		"K8S007",
		domain.ReversibilityCostly,
		fmt.Sprintf("Changing %s on %s releases the address it was reachable at; reverting the manifest restores the spec but not the address, and anything pointing at the old one stays broken.", what, c.new.describe()),
		domain.UndoStep(fmt.Sprintf("kubectl apply -f %s  # then repoint DNS at the newly allocated address", c.object().file)),
	)}
}

// K8S008: an image that cannot be identified is not a rollback target.
func ruleImageNotPinned(c change, _, _ index) []domain.Finding {
	if c.new == nil || !c.new.isWorkload() {
		return nil
	}

	var findings []domain.Finding
	for _, container := range c.new.containers() {
		image, _ := container["image"].(string)
		if isPinned(image) {
			continue
		}

		name, _ := container["name"].(string)
		findings = append(findings, c.finding(
			"K8S008",
			domain.ReversibilityCostly,
			fmt.Sprintf("Container %q in %s runs image %q, identified by %s rather than a digest: static analysis cannot prove that reference still points at the same bytes, so re-applying the previous manifest may pull something other than what was running.", name, c.new.describe(), image, describeReference(image)),
			domain.UndoStep(fmt.Sprintf("kubectl set image %s/%s %s=<image@sha256:...>", c.new.kind, c.new.name, name)),
		))
	}
	return findings
}

// K8S015: a container image changed and the new image is pinned by a cryptographic digest.
//
// This is the ordinary deploy, and it is reversible precisely because a digest is
// content-addressed: re-applying the previous manifest pulls the identical bytes that were
// running before. An image that is *not* digest-pinned never reaches this rule — it is
// K8S008/COSTLY instead, because a tag is a mutable pointer no matter how it is spelled.
func ruleImageChangedPinned(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || !c.new.isWorkload() {
		return nil
	}

	oldImages := containerImages(c.old)

	var findings []domain.Finding
	for _, name := range sortedKeys(containerImages(c.new)) {
		newImage := containerImages(c.new)[name]

		previous, existed := oldImages[name]
		if !existed || previous == newImage || !isPinned(newImage) {
			continue
		}

		findings = append(findings, c.finding(
			"K8S015",
			domain.ReversibilityReversible,
			fmt.Sprintf("Container %q in %s moves to a digest-pinned image, so the previous digest identifies the exact bytes to roll back to.", name, c.new.describe()),
			domain.UndoStep(fmt.Sprintf("kubectl set image %s/%s %s=%s", c.new.kind, c.new.name, name, previous)),
		))
	}
	return findings
}

// containerImages maps container name to image reference.
func containerImages(obj *object) map[string]string {
	out := map[string]string{}
	for _, container := range obj.containers() {
		name, _ := container["name"].(string)
		image, _ := container["image"].(string)
		out[name] = image
	}
	return out
}

// K8S009: deleting configuration that a surviving workload still mounts.
func ruleConfigRemoved(c change, _, newIndex index) []domain.Finding {
	if !c.removed() {
		return nil
	}
	if c.old.kind != "ConfigMap" && c.old.kind != "Secret" {
		return nil
	}

	referents := referencingWorkloads(newIndex, c.old.kind, c.old.namespace, c.old.name)
	if len(referents) == 0 {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S009",
		domain.ReversibilityCostly,
		fmt.Sprintf("%s is deleted while %s still references it, so every pod that restarts afterwards fails to start; the object can be recreated, but a Secret's contents are not recoverable from the manifest.", c.old.describe(), joinDescriptions(referents)),
		domain.UndoStep(fmt.Sprintf("kubectl apply -f %s", c.old.file)),
	)}
}

// referencingWorkloads finds every workload that mounts or injects a named ConfigMap or Secret.
//
// This is why unchanged objects must reach the analyzer: the workload here is untouched by the
// changeset, and without it the deletion looks harmless.
func referencingWorkloads(idx index, kind, namespace, name string) []*object {
	var out []*object

	for key, obj := range idx {
		if !obj.isWorkload() || key.namespace != namespace {
			continue
		}
		if workloadReferences(obj, kind, name) {
			out = append(out, obj)
		}
	}

	sortObjects(out)
	return out
}

// workloadReferences reports whether a workload names a ConfigMap or Secret anywhere it can.
func workloadReferences(obj *object, kind, name string) bool {
	envFromKey, keyRef, volumeKey, volumeNameField := "configMapRef", "configMapKeyRef", "configMap", "name"
	if kind == "Secret" {
		envFromKey, keyRef, volumeKey, volumeNameField = "secretRef", "secretKeyRef", "secret", "secretName"
	}

	for _, container := range obj.containers() {
		for _, item := range sliceAt(container, "envFrom") {
			source, ok := item.(map[string]any)
			if ok && stringAt(source, envFromKey, "name") == name {
				return true
			}
		}

		for _, item := range sliceAt(container, "env") {
			entry, ok := item.(map[string]any)
			if ok && stringAt(entry, "valueFrom", keyRef, "name") == name {
				return true
			}
		}
	}

	for _, item := range sliceAt(obj.podSpec(), "volumes") {
		volume, ok := item.(map[string]any)
		if ok && stringAt(volume, volumeKey, volumeNameField) == name {
			return true
		}
	}

	return false
}

// K8S010: Recreate makes the rollback itself a second outage.
func ruleRecreateStrategy(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || c.new.kind != "Deployment" {
		return nil
	}

	newStrategy := stringAt(c.new.doc, "spec", "strategy", "type")
	oldStrategy := stringAt(c.old.doc, "spec", "strategy", "type")
	if newStrategy != "Recreate" || oldStrategy == "Recreate" {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S010",
		domain.ReversibilityCostly,
		fmt.Sprintf("The Recreate strategy tears down every pod of %s before starting replacements, so rolling back is a second full outage; the change is undoable but the downtime it causes is not.", c.new.describe()),
		domain.UndoStep(fmt.Sprintf("kubectl patch %s %s -p '{\"spec\":{\"strategy\":{\"type\":\"%s\"}}}'", c.new.kind, c.new.name, orDefault(oldStrategy, "RollingUpdate"))),
	)}
}

// K8S011: removing a probe removes the signal a rollout uses to notice it is failing.
func ruleProbeRemoved(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || !c.new.isWorkload() {
		return nil
	}

	newProbes := probesByContainer(c.new)

	var findings []domain.Finding
	for _, name := range sortedKeys(probesByContainer(c.old)) {
		for _, probe := range []string{"livenessProbe", "readinessProbe"} {
			had := probesByContainer(c.old)[name][probe]
			has := newProbes[name][probe]
			if !had || has {
				continue
			}

			findings = append(findings, c.finding(
				"K8S011",
				domain.ReversibilityCostly,
				fmt.Sprintf("The %s on container %q of %s is removed, so traffic reaches pods that are not ready and a failing rollout no longer signals that it is failing; restoring the probe is easy, but the bad rollout it would have caught is not.", probe, name, c.new.describe()),
				domain.UndoStep(fmt.Sprintf("kubectl apply -f %s  # restore the %s on container %s", c.object().file, probe, name)),
			))
		}
	}
	return findings
}

// probesByContainer records which probes each container declares.
func probesByContainer(obj *object) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, container := range obj.containers() {
		name, _ := container["name"].(string)
		out[name] = map[string]bool{
			"livenessProbe":  container["livenessProbe"] != nil,
			"readinessProbe": container["readinessProbe"] != nil,
		}
	}
	return out
}

// tuningFields are the four categories CLAUDE.md §10 names for K8S012. One finding per changed
// category, not per changed leaf: a request that adjusts both cpu and memory is one decision.
var tuningFields = []struct {
	name string
	path []string
}{
	{"replicas", []string{"spec", "replicas"}},
	{"resources", nil}, // container-level, handled separately
	{"env", nil},       // container-level, handled separately
	{"labels", []string{"spec", "template", "metadata", "labels"}},
}

// K8S012: the ordinary, fully reversible case — and the one that must not be over-reported.
func ruleTuningChanged(c change, _, _ index) []domain.Finding {
	if c.old == nil || c.new == nil || !c.new.isWorkload() {
		return nil
	}

	undo := domain.UndoStep(fmt.Sprintf("kubectl apply -f %s  # re-apply the previous manifest", c.object().file))

	var findings []domain.Finding
	for _, field := range tuningFields {
		var changed bool
		switch field.name {
		case "resources", "env":
			changed = containerFieldChanged(c.old, c.new, field.name)
		default:
			changed = !equalAt(c.old.doc, c.new.doc, field.path...)
		}
		if !changed {
			continue
		}

		findings = append(findings, c.finding(
			"K8S012",
			domain.ReversibilityReversible,
			fmt.Sprintf("The %s of %s changed; this is stateless and re-applying the previous manifest restores it exactly.", field.name, c.new.describe()),
			undo,
		))
	}
	return findings
}

// containerFieldChanged compares one field across the containers of both sides, matched by name.
func containerFieldChanged(oldObj, newObj *object, field string) bool {
	oldValues := containerField(oldObj, field)
	newValues := containerField(newObj, field)

	if len(oldValues) != len(newValues) {
		return true
	}
	for name, oldValue := range oldValues {
		newValue, ok := newValues[name]
		if !ok || !deepEqual(oldValue, newValue) {
			return true
		}
	}
	return false
}

func containerField(obj *object, field string) map[string]any {
	out := map[string]any{}
	for _, container := range obj.containers() {
		name, _ := container["name"].(string)
		out[name] = container[field]
	}
	return out
}

// K8S013: adding a workload that did not exist takes nothing away.
func ruleNewWorkload(c change, _, _ index) []domain.Finding {
	if !c.added() || !c.new.isWorkload() {
		return nil
	}

	return []domain.Finding{c.finding(
		"K8S013",
		domain.ReversibilityReversible,
		fmt.Sprintf("%s is new, so the change takes nothing away and the undo is a delete of an object nothing depended on yet.", c.new.describe()),
		domain.UndoStep(fmt.Sprintf("kubectl delete %s %s", c.new.kind, c.new.name)),
	)}
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
