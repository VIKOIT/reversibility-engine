// Package engine orchestrates analyzers and turns their findings into a
// domain.ReversibilityCertificate.
//
// It owns the only recover() boundary in the codebase. A panic anywhere beneath it becomes a
// finding with RuleID ENGINE_PANIC and grade F — never a pass, never a silent success. The same
// applies to analyzer errors and to any UNKNOWN classification: this package is where
// fail-closed is actually enforced.
//
// Output is deterministic. Identical input produces a byte-identical certificate: no
// timestamps, no UUIDs, no hostnames, no map-iteration order.
package engine
