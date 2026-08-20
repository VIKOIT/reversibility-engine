// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package analyzer defines the Analyzer contract that every language- or platform-specific
// checker implements.
//
// Analyzers are pure functions over a changeset: they receive []domain.ChangedFile and return
// findings. They never touch the network, disk, git, or GitHub — all fetching belongs to
// internal/provider. That separation is what makes the classification tables testable from
// fixtures alone.
package analyzer
