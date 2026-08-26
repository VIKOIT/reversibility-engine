// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package kubernetes classifies structural differences between rendered Kubernetes manifests
// against the authoritative K8S001-K8S014 table in docs/SPECIFICATION.md.
//
// Objects are matched old-to-new by apiVersion, kind, namespace, and name. A manifest that
// fails to parse, or whose kind is unrecognized, is K8S014/UNKNOWN — the engine will not
// guess at the blast radius of something it cannot read.
package kubernetes
