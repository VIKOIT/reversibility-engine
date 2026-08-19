package kubernetes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"

	yamlv3 "gopkg.in/yaml.v3"
	"sigs.k8s.io/yaml"
)

// object is one Kubernetes manifest document, kept as decoded YAML rather than typed structs.
//
// Typed structs would require the full k8s.io/api dependency and would silently drop fields the
// vendored API version does not know about. A generic map keeps every field the author wrote,
// which matters when the question is "did anything change".
type object struct {
	// file is the path the document came from, used to attribute findings.
	file string

	apiVersion string
	kind       string
	namespace  string
	name       string

	doc map[string]any
}

// objectKey identifies an object across the old and new sides of a change, per CLAUDE.md §10.
type objectKey struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

func (k objectKey) String() string {
	ns := k.namespace
	if ns == "" {
		ns = "-"
	}
	return fmt.Sprintf("%s %s %s/%s", k.apiVersion, k.kind, ns, k.name)
}

func (o *object) key() objectKey {
	return objectKey{
		apiVersion: o.apiVersion,
		kind:       o.kind,
		namespace:  o.namespace,
		name:       o.name,
	}
}

// describe names the object the way a reviewer would refer to it.
func (o *object) describe() string {
	if o.namespace == "" {
		return fmt.Sprintf("%s %s", o.kind, o.name)
	}
	return fmt.Sprintf("%s %s/%s", o.kind, o.namespace, o.name)
}

// parseManifest decodes every document in a manifest file.
//
// Document boundaries come from a real YAML stream decoder, never from splitting bytes on
// "---". Byte splitting cannot know whether a separator-looking line is inside a scalar, and it
// does not understand the "..." end-of-document marker at all. Both mistakes silently change
// which objects the engine sees.
//
// The two-stage decode is deliberate. gopkg.in/yaml.v3 walks the stream and yields one document
// node at a time, which is the part that has to be correct. sigs.k8s.io/yaml then decodes each
// document through JSON, which is how Kubernetes itself round-trips manifests and is what gives
// the uniform JSON-compatible types the rules compare. Used alone, sigs.k8s.io/yaml decodes only
// the *first* document of a stream and returns a nil error — a silent truncation, and the exact
// fail-open this product cannot have.
func parseManifest(file string, content []byte) ([]object, error) {
	decoder := yamlv3.NewDecoder(bytes.NewReader(content))

	var out []object
	for i := 1; ; i++ {
		var node yamlv3.Node

		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		// An empty document is ordinary in rendered output: a template whose condition was
		// false emits nothing between two separators.
		if isEmptyDocument(&node) {
			continue
		}

		raw, err := yamlv3.Marshal(&node)
		if err != nil {
			return nil, fmt.Errorf("document %d: re-encoding: %w", i, err)
		}

		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		if doc == nil {
			continue
		}

		o := object{
			file:       file,
			doc:        doc,
			apiVersion: stringAt(doc, "apiVersion"),
			kind:       stringAt(doc, "kind"),
			namespace:  stringAt(doc, "metadata", "namespace"),
			name:       stringAt(doc, "metadata", "name"),
		}

		// A document with no kind is not a Kubernetes object. Refusing it here means the
		// caller reports K8S014 rather than silently indexing something meaningless.
		if o.kind == "" {
			return nil, fmt.Errorf("document %d has no kind", i)
		}

		out = append(out, o)
	}
	return out, nil
}

// isEmptyDocument reports whether a decoded document carries no content.
func isEmptyDocument(node *yamlv3.Node) bool {
	if node.Kind == 0 || len(node.Content) == 0 {
		return true
	}
	return node.Content[0].Tag == "!!null"
}

// The kinds the classification table gives the engine authority over. Anything else is
// K8S014/UNKNOWN: a kind with no rule is a kind whose reversibility the engine cannot vouch for,
// and a custom resource in particular can own arbitrary infrastructure.
var recognizedKinds = map[string]bool{
	"Deployment":               true,
	"StatefulSet":              true,
	"DaemonSet":                true,
	"Service":                  true,
	"PersistentVolumeClaim":    true,
	"StorageClass":             true,
	"ConfigMap":                true,
	"Secret":                   true,
	"Namespace":                true,
	"CustomResourceDefinition": true,
}

// workloadKinds are the kinds carrying a pod template, which is what the container-level rules
// (K8S008, K8S011, K8S012, K8S013) operate on.
var workloadKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
}

func (o *object) isWorkload() bool   { return workloadKinds[o.kind] }
func (o *object) isRecognized() bool { return recognizedKinds[o.kind] }

// podSpec returns the pod specification of a workload, which is where containers live.
func (o *object) podSpec() map[string]any {
	return mapAt(o.doc, "spec", "template", "spec")
}

// containers returns every container in a workload, init containers included: an init container
// with a floating image tag is just as unidentifiable a rollback target as a main one.
func (o *object) containers() []map[string]any {
	spec := o.podSpec()
	if spec == nil {
		return nil
	}

	var out []map[string]any
	for _, field := range []string{"initContainers", "containers"} {
		for _, item := range sliceAt(spec, field) {
			if c, ok := item.(map[string]any); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// valueAt walks a decoded document by key path, returning nil if any step is missing.
func valueAt(m map[string]any, path ...string) any {
	var cur any = m
	for _, key := range path {
		asMap, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = asMap[key]
		if !ok {
			return nil
		}
	}
	return cur
}

func mapAt(m map[string]any, path ...string) map[string]any {
	v, _ := valueAt(m, path...).(map[string]any)
	return v
}

func sliceAt(m map[string]any, path ...string) []any {
	v, _ := valueAt(m, path...).([]any)
	return v
}

func stringAt(m map[string]any, path ...string) string {
	v, _ := valueAt(m, path...).(string)
	return v
}

// equalAt reports whether the value at a path is identical on both sides.
//
// Decoded documents contain only JSON-compatible types, so DeepEqual is a reliable structural
// comparison here and does not depend on field ordering.
func equalAt(a, b map[string]any, path ...string) bool {
	return reflect.DeepEqual(valueAt(a, path...), valueAt(b, path...))
}
