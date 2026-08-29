// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// This file answers one question, and getting it wrong was a P0 in its own right:
//
//	Where do this provider's paths sit in the repository?
//
// Every other provider already knows. git and GitHub report paths relative to the repository
// root, which is what Path documents. The filesystem provider reports them relative to whatever
// directory it was pointed at — so `revctl check django/contrib/auth/migrations` produced the
// path `0001_initial.py`, having stripped the one segment that says these files are migrations.
// Candidate detection then found nothing to detect, and the documented invocation reached
// NO_CANDIDATES and exit 0 over thirteen unreadable Django migrations, while
// `revctl check django/contrib/auth` reached UNSUPPORTED_CONTENT and exit 2 over the same files.
//
//	**Candidate detection must not depend on how the analysis root was named.**
//
// See docs/SPECIFICATION.md §16.10.

// vcsMarkers are the entries that mark a directory as the root of a checkout.
//
// The same three names the enumeration walk already skips, so there is one idea of what a
// repository is rather than two. A marker may be a file rather than a directory: a git worktree
// writes `.git` as a file holding a pointer.
var vcsMarkers = []string{".git", ".hg", ".svn"}

// RootPrefix returns the prefix that puts an FS provider's paths back into the namespace Path
// documents — the file's location relative to the repository root.
//
// The caller joins it onto each changeset path before asking any question about *location*, and
// never before reporting one: the certificate keeps naming files exactly as the caller named
// them, so a finding's path is still one the reader can paste into the same command.
//
// **The anchor is the repository, not the filesystem.** Both are self-consistent — the defect is
// an anchor that moves with the argument — and the repository is the one the rules are written
// about: docs/RULES.md §3 asks whether a file "sits under a path segment named migrations",
// meaning in the repository, not on the machine. Anchoring at the filesystem root instead would
// make a checkout parked under `~/migrate/` read every `.py` file beneath it as a migration,
// which is severity invented from where somebody keeps their source.
//
// Outside a checkout there is no better anchor than the absolute path, and that is what is used.
// It is consistent, which is the property being bought here; it is only unqualified by a
// repository boundary because there is none to qualify it.
//
// Several roots contribute only the prefix they share. A segment true of one root and not of
// another is not true of the changeset, and asserting it would classify files by a directory
// half of them are not in.
func RootPrefix(roots []string) string {
	var common []string
	first := true

	for _, root := range roots {
		segments, ok := rootSegments(root)
		if !ok {
			// A root that cannot be resolved anchors nothing. Guessing a prefix would classify
			// files by a location the provider never established, and the empty prefix is the
			// behaviour every caller had before this existed.
			return ""
		}

		if first {
			common, first = segments, false
			continue
		}
		common = commonSegments(common, segments)
	}

	return path.Join(common...)
}

// rootSegments resolves one command-line root to the segments it contributes.
//
// It resolves rather than reading the argument, because the argument is exactly what must not
// matter: `./migrations`, `../auth/migrations`, an absolute path, and `.` from inside the
// directory all name the same place and must all classify the same way.
func rootSegments(root string) ([]string, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}

	// A root naming one file contributes its directory. The provider keys that file by its base
	// name alone — so that `revctl check migrations/0001.sql` reports the path the user typed —
	// which means the directory is exactly what relativising dropped.
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	rel := abs
	if repo := repositoryRoot(abs); repo != "" {
		within, err := filepath.Rel(repo, abs)
		if err != nil {
			return nil, false
		}
		rel = within
	}

	var out []string
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		switch segment {
		case "", ".", "..":
			// Neither a traversal step nor the empty segment of a leading slash names a
			// directory, so neither can contribute one.
		default:
			out = append(out, segment)
		}
	}
	return out, true
}

// repositoryRoot walks up from dir to the checkout that contains it, or returns "" if there is
// none.
//
// The walk is on directory metadata only and stops at the first marker, so it costs a handful of
// stats on the way to a repository root that is almost always one or two levels up.
func repositoryRoot(dir string) string {
	for {
		for _, marker := range vcsMarkers {
			if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// commonSegments returns the leading segments a and b agree on.
func commonSegments(a, b []string) []string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}

	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}
