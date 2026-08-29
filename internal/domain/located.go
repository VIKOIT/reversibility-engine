// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

import (
	"path"
	"strings"
)

// This file exists because one decision surface held two path namespaces, and that is what the
// Django P0 was — three times.
//
//	**Path-keyed decisions use one namespace, and it is not the one the caller typed.**
//	A decision that depends on how the analysis root was named is a decision that changes
//	answer for the same files.
//
// A changeset path is relative to whatever the provider was pointed at. For git and GitHub that
// is the repository, and the path is already in the namespace the rules are written about. For
// the filesystem provider it is a directory somebody chose on a command line — so
// `revctl check ./migrations` reports `0001_initial.py`, having dropped the one segment that
// says these files are migrations.
//
// Every decision that reads a path was therefore reading a different path depending on an
// argument nobody thought of as an input. Four of them were, in the order they were found:
//
//  1. Candidate detection — the P0 itself: NO_CANDIDATES and exit 0 for the documented
//     invocation, UNSUPPORTED_CONTENT and exit 2 one directory up.
//  2. Policy `ignore:` globs — `django/**/migrations/*.py` matched nothing under
//     `check ./migrations`, so an ignore list read as configured and was inert.
//  3. Waiver `path:` globs — the same, and worse, because a waiver that matches nothing looks
//     identical to a waiver that has not expired yet.
//  4. `--terraform-plan` — which had already met this and answered it with bidirectional suffix
//     matching, a workaround whose own comment says the two spellings "differ". It also
//     over-claimed: `--terraform-plan a/plan.json` matched `b/a/plan.json` too.
//
// The type is the fix, not the discipline. `Candidate(f.Path)` no longer compiles; the caller
// has to write a conversion, and a conversion is a visible, greppable act where a forgotten
// qualification was nothing at all.

// Located is a path in the one namespace every path-keyed decision uses: where the file sits in
// the repository, not how the caller happened to name its root.
//
// It is deliberately not `string`. Every function that decides something from a path takes this
// type, so handing one a raw changeset path is a compile error rather than a wrong answer on a
// changeset nobody looks at twice.
//
// **It is never rendered.** Outside a checkout a Located is an absolute path, and a certificate
// carrying one would stop being byte-identical between machines — the same class of input as a
// timestamp or a hostname, which the determinism rule excludes. The caller's naming survives in
// exactly one place, and that place is output.
type Located string

// String returns the path. It exists so a Located can be formatted in a test failure or a log
// line without a conversion at every call site — never so one can reach a certificate.
func (l Located) String() string { return string(l) }

// Dir returns the directory holding this path, in the same namespace.
func (l Located) Dir() Located { return Located(path.Dir(string(l))) }

// Ext returns the lower-cased extension, or "" if there is none.
func (l Located) Ext() string { return strings.ToLower(path.Ext(string(l))) }

// Segments returns the path's segments, lower-cased, for the rules that ask whether a named
// directory appears above a file.
func (l Located) Segments() []string {
	return strings.Split(strings.ToLower(string(l)), "/")
}

// Locator maps a changeset path into the namespace decisions are made in.
//
// It is a value rather than a package-level function because the mapping is a property of one
// run: the same file is `0001.sql` to a run rooted at its directory and `db/migrate/0001.sql`
// to a run rooted at the repository, and both runs may happen in one process.
type Locator func(string) Located

// NewLocator returns the Locator for an analysis rooted at the given repository-relative prefix.
//
// An empty prefix is the identity, and that is the correct answer rather than a permissive
// default: git, GitHub and the fake all report repository-relative paths already, so for three
// of the four providers there is nothing to restore. The prefix comes from
// `provider.RootPrefix`, which is the only thing that resolves a command-line root, because
// resolving one requires touching the filesystem and this package touches nothing.
func NewLocator(prefix string) Locator {
	clean := normalizeSlashes(prefix)
	if clean == "" || clean == "." {
		return func(p string) Located { return Located(normalizeSlashes(p)) }
	}

	return func(p string) Located {
		return Located(path.Join(clean, normalizeSlashes(p)))
	}
}

// Identity is the Locator for paths that are already in the decision namespace.
//
// It is named rather than written as `NewLocator("")` at each call site so that a reader can
// tell "this provider reports repository-relative paths" from "somebody forgot to pass one".
func Identity() Locator { return NewLocator("") }

func normalizeSlashes(p string) string {
	slashed := strings.ReplaceAll(p, "\\", "/")
	if slashed == "" {
		return ""
	}
	return path.Clean(slashed)
}
