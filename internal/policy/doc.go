// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package policy reads and applies .reversibility.yml.
//
// A gate with no legitimate escape hatch gets switched off entirely the first time it is wrong,
// and a gate that is switched off protects nothing. This package is that escape hatch, built so
// that using it is a recorded decision rather than a way to make the tool quiet:
//
//   - A waiver must say why it exists and when it lapses. Neither is optional, and a waiver
//     missing either is a configuration error rather than a warning — a warning in a CI log is
//     not read, and the waiver would take effect anyway.
//   - A waiver expires on a date, not after a duration. A duration is relative to a moment
//     nobody records, so it renews itself on every run and never expires at all.
//   - An expired waiver is inert. The finding comes back with no edit and no announcement.
//   - A waiver downgrades a finding to advisory; it never deletes one. Waived findings stay in
//     the certificate with their reason and expiry beside them, because a suppression nobody can
//     see is indistinguishable from a rule nobody wrote.
//   - A waiver may not cover UNKNOWN. Accepting a risk nobody has characterised is not a
//     decision anyone is in a position to make.
//   - An override may only make a rule stricter.
//
// What a policy may never do is improve the measurement. The certificate's Grade is computed
// from every finding, waived ones included, and the AI merge gate follows it — so a waiver can
// unblock a human's pipeline and can never authorise an agent to merge something nobody could
// undo. EffectiveGrade is the separate, waiver-aware number a CI threshold compares against.
package policy
