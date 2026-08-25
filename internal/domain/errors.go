// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

import "errors"

// Sentinel errors for the engine.
//
// Every one of these ends in grade F. They are distinguishable so that operators can tell a
// broken toolchain from a broken migration — but never so that a caller can decide some errors
// are safe to ignore.
var (
	// ErrNotImplemented marks a code path that has not been built yet. It exists so that a
	// stub fails loudly instead of returning an empty, passing result.
	ErrNotImplemented = errors.New("domain: not implemented")

	// ErrParserUnavailable means the SQL parser could not be initialized — typically a build
	// without cgo. There is no regex fallback; see ADR/0001.
	ErrParserUnavailable = errors.New("domain: sql parser unavailable")

	// ErrParse means input could not be parsed. The corresponding finding is UNKNOWN.
	ErrParse = errors.New("domain: parse failed")

	// ErrUnsupportedFile means no analyzer claims the file.
	ErrUnsupportedFile = errors.New("domain: unsupported file")

	// ErrInvalidChangedFile means a provider produced a file that violates the change model,
	// such as a MODIFIED file with no previous content.
	ErrInvalidChangedFile = errors.New("domain: invalid changed file")

	// ErrProviderFailed means the changeset could not be fetched.
	ErrProviderFailed = errors.New("domain: file provider failed")

	// ErrAnalyzerPanic means an analyzer panicked and the engine's recover boundary caught it.
	ErrAnalyzerPanic = errors.New("domain: analyzer panicked")

	// ErrInvalidPolicy means a policy file is malformed, or asks for something a policy is not
	// permitted to ask for. It is never survivable: a run that could not resolve its own
	// configuration does not know what it was supposed to enforce.
	ErrInvalidPolicy = errors.New("domain: invalid policy")
)
