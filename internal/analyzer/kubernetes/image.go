// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package kubernetes

import "strings"

// digestSeparators are the forms a content-addressed image reference can take.
var digestSeparators = []string{"@sha256:", "@sha512:"}

// isPinned reports whether an image reference identifies exactly one immutable artifact.
//
// Only a cryptographic digest counts. Static analysis cannot prove that a tag — even a semver
// one like :v2.1.0 — still points at the same bytes on the remote registry tomorrow; tags are
// mutable pointers by design, and a registry operator can move one at any time. A digest is the
// only reference that guarantees bit-for-bit rollback determinism.
//
// Everything else falls under K8S008/COSTLY, per the owner's ruling recorded in CLAUDE.md §10.
func isPinned(image string) bool {
	image = strings.TrimSpace(image)
	if image == "" {
		return false
	}

	for _, sep := range digestSeparators {
		if idx := strings.Index(image, sep); idx > 0 && len(image) > idx+len(sep) {
			return true
		}
	}
	return false
}

// tagOf extracts the tag from an image reference, taking care not to mistake a registry port
// for one: "registry:5000/app" has a colon but no tag.
//
// The tag does not decide pinning — only a digest does — but naming it makes the K8S008
// rationale specific about what is mutable.
func tagOf(image string) (string, bool) {
	// A digest reference may carry a tag before the "@"; the tag is not what identifies it.
	for _, sep := range digestSeparators {
		if idx := strings.Index(image, sep); idx > 0 {
			image = image[:idx]
		}
	}

	colon := strings.LastIndex(image, ":")
	if colon < 0 {
		return "", false
	}

	// A colon that appears before the last slash belongs to a registry host and port.
	if slash := strings.LastIndex(image, "/"); slash > colon {
		return "", false
	}

	tag := image[colon+1:]
	if tag == "" {
		return "", false
	}
	return tag, true
}

// describeReference names how an image is identified, for the K8S008 rationale.
func describeReference(image string) string {
	if tag, ok := tagOf(image); ok {
		return "the mutable tag " + tag
	}
	return "no tag at all, which resolves to :latest at pull time"
}
