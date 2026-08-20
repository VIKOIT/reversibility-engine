// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package domain holds the vocabulary of the Reversibility Engine: the change model, the
// classification enums, findings, and the certificate.
//
// This package imports nothing outside the standard library, and it never will. Every other
// package depends on it; it depends on nothing. That constraint is enforced in CI by
// scripts/check-domain-imports.sh, because a spine that acquires dependencies stops being a
// spine.
//
// Types only. No I/O, no parsing, no scoring.
package domain
