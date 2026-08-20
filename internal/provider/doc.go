// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package provider fetches changesets. It is the only place in the engine permitted to perform
// I/O.
//
// FileProvider has three implementations: a filesystem provider for local runs, a GitHub
// provider for the App, and a fake provider that reads testdata fixtures. Tests use the fake —
// there is no simulated or placeholder fetch code anywhere in this repository.
package provider
