// Package kubernetes classifies structural differences between rendered Kubernetes manifests
// against the authoritative K8S001-K8S014 table in CLAUDE.md.
//
// Objects are matched old-to-new by apiVersion, kind, namespace, and name. A manifest that
// fails to parse, or whose kind is unrecognized, is K8S014/UNKNOWN — the engine will not
// guess at the blast radius of something it cannot read.
package kubernetes
