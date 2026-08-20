// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package kubernetes

import (
	"math"
	"strings"
	"testing"
)

// Document boundaries come from a real YAML stream decoder, not from splitting bytes on "---".
//
// Two failure modes are guarded here. The first is silent truncation: sigs.k8s.io/yaml used
// alone decodes only the first document of a stream and returns a nil error, which would hide
// every object after the first in any rendered Helm output. The second is mis-splitting: a
// separator-looking line inside a scalar, or a "..." end-of-document marker, which byte
// splitting cannot reason about at all.
func TestParseManifestDocumentBoundaries(t *testing.T) {
	t.Parallel()

	obj := func(name string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
	}

	tests := []struct {
		name    string
		in      string
		wantLen int
	}{
		{"single document", obj("a"), 1},
		{"two documents", obj("a") + "---\n" + obj("b"), 2},
		{"leading separator", "---\n" + obj("a"), 1},
		{"trailing separator", obj("a") + "---\n", 1},
		{"separator with comment", obj("a") + "--- # Source: chart/templates/x.yaml\n" + obj("b"), 2},
		{"empty documents between real ones", "---\n\n---\n" + obj("a") + "---\n\n", 1},
		{"five documents", obj("a") + "---\n" + obj("b") + "---\n" + obj("c") + "---\n" + obj("d") + "---\n" + obj("e"), 5},

		// The "..." end-of-document marker starts a new document without a "---". Byte
		// splitting does not know this; a stream decoder does.
		{"end-of-document marker", obj("a") + "...\n---\n" + obj("b"), 2},

		// A separator-looking line inside a scalar is content, not a boundary. This is the
		// case that makes byte splitting unsafe.
		{"--- inside a block scalar", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  app.yaml: |\n    key: value\n    ---\n    other: value\n", 1},
		{"--- inside a quoted string", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  note: \"--- not a break\"\n", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseManifest("x.yaml", []byte(tt.in))
			if err != nil {
				t.Fatalf("parseManifest: %v", err)
			}
			if len(got) != tt.wantLen {
				names := make([]string, 0, len(got))
				for _, o := range got {
					names = append(names, o.name)
				}
				t.Errorf("got %d documents %v, want %d", len(got), names, tt.wantLen)
			}
		})
	}
}

// A block scalar containing a separator must survive decoding intact, not merely avoid being
// split. If the embedded content were truncated the ConfigMap would compare unequal on every
// run for no reason.
func TestParseManifestPreservesEmbeddedSeparators(t *testing.T) {
	t.Parallel()

	const manifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: embedded
data:
  bundle.yaml: |
    kind: First
    ---
    kind: Second
`

	got, err := parseManifest("cm.yaml", []byte(manifest))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d documents, want 1", len(got))
	}

	embedded := stringAt(got[0].doc, "data", "bundle.yaml")
	for _, want := range []string{"kind: First", "---", "kind: Second"} {
		if !strings.Contains(embedded, want) {
			t.Errorf("embedded content lost %q; got %q", want, embedded)
		}
	}
}

func TestParseManifestMultiDocument(t *testing.T) {
	t.Parallel()

	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: web
---
apiVersion: v1
kind: Service
metadata:
  name: b
  namespace: web
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: c
  namespace: web
`

	got, err := parseManifest("manifests.yaml", []byte(manifest))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d objects, want 3 — a multi-document manifest is being silently truncated", len(got))
	}

	for i, want := range []string{"ConfigMap", "Service", "Deployment"} {
		if got[i].kind != want {
			t.Errorf("object %d kind = %q, want %q", i, got[i].kind, want)
		}
		if got[i].namespace != "web" {
			t.Errorf("object %d namespace = %q, want web", i, got[i].namespace)
		}
	}
}

func TestParseManifestRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"broken indentation", "kind: Deployment\nmetadata:\n  name: a\n    namespace: b\n"},
		{"unterminated flow sequence", "kind: Deployment\nspec:\n replicas: [3\n"},
		{"no kind", "apiVersion: v1\nmetadata:\n  name: a\n"},
		{"one bad document among good ones", "kind: ConfigMap\n---\nspec:\n replicas: [3\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseManifest("x.yaml", []byte(tt.in))
			if err == nil {
				t.Errorf("parseManifest(%q) returned nil error and %d objects; malformed input must fail closed", tt.in, len(got))
			}
		})
	}
}

// Empty documents are ordinary in rendered output — a template whose condition was false emits
// nothing between two separators — and must not be mistaken for an error.
func TestParseManifestSkipsEmptyDocuments(t *testing.T) {
	t.Parallel()

	got, err := parseManifest("x.yaml", []byte("---\n\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n\n"))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1", len(got))
	}
}

// Only a cryptographic digest pins an image.
//
// Static analysis cannot prove a tag still points at the same bytes on the remote registry —
// tags are mutable by design, and semver spelling changes nothing about that. Every tag,
// however version-like, is therefore K8S008/COSTLY rather than pinned.
func TestIsPinned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		image string
		want  bool
	}{
		{"ghcr.io/acme/api@sha256:3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea", true},
		{"acme/api@sha512:3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea", true},

		// A digest still pins when a tag is carried alongside it.
		{"ghcr.io/acme/api:1.4.2@sha256:3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea", true},

		// Every tag is a mutable pointer, semver included. This is the owner's ruling.
		{"acme/api:1.4.2", false},
		{"acme/api:v2", false},
		{"acme/api:2024-01-15", false},
		{"acme/api:1.4.2-alpine", false},
		{"registry.example.com:5000/acme/api:1.4.2", false},

		{"acme/api:latest", false},
		{"acme/api:main", false},
		{"acme/api:stable", false},

		// No tag resolves to :latest at pull time.
		{"acme/api", false},
		{"registry.example.com:5000/acme/api", false},

		{"", false},
		{"acme/api:", false},

		// Malformed digest references must not be mistaken for pinned ones.
		{"@sha256:abc", false},
		{"acme/api@sha256:", false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			t.Parallel()
			if got := isPinned(tt.image); got != tt.want {
				t.Errorf("isPinned(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}

// A registry port is a colon that is not a tag separator. Reading it as one would call every
// image on a private registry unpinned.
func TestTagOfIgnoresRegistryPort(t *testing.T) {
	t.Parallel()

	if tag, ok := tagOf("registry.example.com:5000/acme/api"); ok {
		t.Errorf("tagOf found tag %q in a registry-port reference, want none", tag)
	}
	if tag, ok := tagOf("registry.example.com:5000/acme/api:1.4.2"); !ok || tag != "1.4.2" {
		t.Errorf("tagOf = %q/%v, want 1.4.2/true", tag, ok)
	}
}

func TestParseQuantity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want float64
	}{
		{"10Gi", 10 * (1 << 30)},
		{"20Gi", 20 * (1 << 30)},
		{"512Mi", 512 * (1 << 20)},
		{"1Ti", 1 << 40},
		{"1G", 1e9},
		{"1000000", 1e6},
		{"500m", 0.5},
		{"1.5Gi", 1.5 * (1 << 30)},
		{"  10Gi  ", 10 * (1 << 30)},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := parseQuantity(tt.in)
			if err != nil {
				t.Fatalf("parseQuantity(%q): %v", tt.in, err)
			}
			if math.Abs(got-tt.want) > 1 {
				t.Errorf("parseQuantity(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// 1Gi is 2^30 and 1G is 10^9. Treating them as equal would call a shrink an increase.
func TestQuantityScalesAreDistinct(t *testing.T) {
	t.Parallel()

	binary, err := parseQuantity("1Gi")
	if err != nil {
		t.Fatalf("parseQuantity: %v", err)
	}
	decimal, err := parseQuantity("1G")
	if err != nil {
		t.Fatalf("parseQuantity: %v", err)
	}

	if binary <= decimal {
		t.Errorf("1Gi (%v) should exceed 1G (%v)", binary, decimal)
	}
}

func TestParseQuantityRejectsGarbage(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "  ", "Gi", "10Zi", "ten", "10 Gi", "1e999", "--5Gi"} {
		if got, err := parseQuantity(in); err == nil {
			t.Errorf("parseQuantity(%q) = %v with nil error; an unreadable quantity must fail, not guess", in, got)
		}
	}
}
