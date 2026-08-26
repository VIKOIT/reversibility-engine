// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

// Package postgres classifies PostgreSQL migration statements against the authoritative
// PG001-PG027 table in docs/SPECIFICATION.md, and validates that a down migration exists for
// each up migration.
//
// Classification is driven by a real AST, never by regex: the difference between
// "DROP TABLE users" and a string literal containing those words is the difference between a
// correct verdict and a fabricated one. Anything the table does not name is PG027/UNKNOWN,
// which fails closed.
package postgres
