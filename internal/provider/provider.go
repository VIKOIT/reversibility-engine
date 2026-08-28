// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// docs/SPECIFICATION.md §16.9, and the reason this interface has two methods instead of one:
//
//	Enumeration and retrieval are separate concerns. A provider must be able to report what
//	exists without fetching it. Any check that describes the shape of a changeset depends on
//	enumeration; any check that describes content depends on retrieval. Conflating them makes
//	the first check unable to see what it is measuring.
//
// The single-method version took a per-path predicate and returned only what it admitted, so a
// path the predicate rejected left no trace anywhere. Three defects came out of that, each found
// and fixed separately before the pattern was visible:
//
//  1. A docs-only pull request and thirteen unreadable Django migrations reached the engine as
//     the same empty file list, because the predicate admitted neither.
//  2. Coverage counted only the files an analyzer wanted, which made its numerator and its
//     denominator the same number.
//  3. Renaming migrations/ to db/sql/ turned strict coverage off, because "read this file
//     because a sibling is a .sql" is not a decision a per-path predicate can express.
//
// Nothing here is newly computed. Every implementation already knew the listing before it
// fetched anything: the filesystem walk visits paths without reading them, git diff
// --name-status returns names before blobs, the GitHub files API returns the listing in the same
// response, and the fake has the fixture directory. The old contract simply refused to expose
// what the providers already knew.

// Path is one file in a changeset, known by name before any content is fetched.
//
// It carries everything that can be decided without I/O and nothing that cannot. A caller
// choosing what to read gets the whole listing of these, which is the point of the split.
type Path struct {
	// Path is the file's location as it appears in the changeset, slash-separated and relative
	// to the repository root — never an absolute path on the machine doing the analysis. A
	// caller matching "legacy/**" means the directory in the repository, and testing that
	// against a temp-directory path would silently match nothing.
	Path string

	// PrevPath is where a renamed file used to be, and empty otherwise.
	PrevPath string

	Status domain.ChangeStatus
}

// Select chooses which of the listed paths to read.
//
// It receives the **complete** listing rather than one path at a time, and that is the entire
// difference from the predicate it replaces. Deciding "read this README because the directory it
// sits in also holds a .sql file" requires seeing the directory, and a func(path) bool never can.
//
// A nil Select reads everything listed.
type Select func(listed []Path) []Path

// FileProvider resolves a change reference into the files it touched.
//
// This is the only interface in the engine permitted to perform I/O. Everything downstream of it
// operates on bytes already in memory, which is what keeps the analyzers deterministic and their
// rule tables testable from fixtures.
type FileProvider interface {
	// List enumerates every path in the changeset without fetching any content.
	//
	// It is the cheap half and it must stay cheap: implementations may stat, walk, or call a
	// listing API, and may not read or download file bodies. The result is sorted by Path.
	List(ctx context.Context, ref domain.ChangeRef) ([]Path, error)

	// Read fetches content for exactly the paths given, and for no others.
	//
	// It takes the ref as well as the paths, symmetrically with List, so that a provider needs
	// no state between the two calls. The GitHub provider is the one that forced this: a
	// comparison ref is per-call there, where the filesystem and git providers get their
	// revisions at construction. Storing it on the provider between phases would make Read
	// depend on List having run first, on the same value, in the same goroutine.
	//
	// The result is sorted by path. Callers hash it into the certificate's InputDigest, and an
	// unstable order would produce a different digest for identical input.
	Read(ctx context.Context, ref domain.ChangeRef, paths []Path) ([]domain.ChangedFile, error)
}

// Resolve lists a changeset, chooses what to read from the complete listing, and reads it.
//
// It returns both halves. The listing is what coverage is measured against — a file that exists
// and was not read is exactly what strict coverage exists to report — and the files are what the
// analyzers see. Returning only the second is how the old contract lost the first.
func Resolve(
	ctx context.Context,
	p FileProvider,
	ref domain.ChangeRef,
	choose Select,
) (listed []Path, files []domain.ChangedFile, err error) {
	listed, err = p.List(ctx, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the changeset: %w", err)
	}

	wanted := listed
	if choose != nil {
		wanted = choose(listed)
	}

	files, err = p.Read(ctx, ref, wanted)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the changeset: %w", err)
	}
	return listed, files, nil
}

// SortPaths puts a listing in the canonical order every provider must return.
func SortPaths(in []Path) {
	sort.Slice(in, func(i, j int) bool { return in[i].Path < in[j].Path })
}

// PathSet indexes a listing by path, for a Select that needs to ask about siblings.
func PathSet(in []Path) map[string]Path {
	out := make(map[string]Path, len(in))
	for _, p := range in {
		out[p.Path] = p
	}
	return out
}

// All lists a changeset and reads every path in it.
//
// It is the honest name for what the old single-method contract did when no predicate was given,
// and it is what a caller that genuinely wants the whole changeset should use: fixture loaders,
// tests, and anything comparing two providers' output.
//
// It does not reintroduce what was removed. The defect in the old contract was the per-path
// predicate — a filter applied during retrieval, invisible afterwards, and unable to see the
// changeset it was filtering. Reading everything hides nothing.
func All(ctx context.Context, p FileProvider, ref domain.ChangeRef) ([]domain.ChangedFile, error) {
	_, files, err := Resolve(ctx, p, ref, nil)
	return files, err
}
