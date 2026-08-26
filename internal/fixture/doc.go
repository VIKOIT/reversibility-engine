// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package fixture loads the expectation files that accompany every fixture in testdata.
//
// It is a layout addition beyond docs/SPECIFICATION.md §5, made because the analyzer tests, the
// engine tests, and the renderer golden tests all need to read the same expectation format. Four
// copies of the same loader would drift, and a drifting test harness quietly weakens the
// guarantee it exists to protect.
//
// An expectation deliberately pins classification and nothing else: rule ID, file, line,
// reversibility, lock hazard, and whether an undo step should exist. It does not pin rationale
// or undo-step wording, so that improving the prose a human reads does not break the tests.
package fixture
