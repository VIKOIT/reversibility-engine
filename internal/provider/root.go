// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/policy"
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

// projectMarkers are the entries that mark a directory as the root of a project.
//
// The three version-control names are the ones the enumeration walk already skips, so there is
// one idea of what a checkout is rather than two. A marker may be a file rather than a directory:
// a git worktree writes `.git` as a file holding a pointer.
//
// **`.reversibility.yml` is on this list and it is not an afterthought.** The decision namespace
// is not only what the engine classifies against, it is what the user's `ignore:` and waiver
// `path:` globs are written against — and those are written relative to the project, because the
// policy file sits at its root. Anchoring only at a checkout would mean that a tree without one
// resolved to absolute paths, and every relative glob in it would silently match nothing. That
// is the exact failure this whole namespace exists to remove, reappearing in the config.
//
// So the anchor is "where this project starts", and a policy file is as good evidence of that as
// a `.git` is. A tree with neither has no globs to break, because a glob comes from a policy file
// and a policy file would have been a marker.
var projectMarkers = []string{".git", ".hg", ".svn", policy.FileName}

// RootPrefix returns the prefix that puts an FS provider's paths back into the namespace Path
// documents — the file's location relative to the repository root.
//
// The caller joins it onto each changeset path before asking any question about *location*, and
// never before reporting one: the certificate keeps naming files exactly as the caller named
// them, so a finding's path is still one the reader can paste into the same command.
//
// **The anchor is the project, not the filesystem.** Both are self-consistent — the defect is an
// anchor that moves with the argument — and the project is the one the rules and the config are
// both written about: docs/RULES.md §3 asks whether a file "sits under a path segment named
// migrations", meaning in the repository rather than on the machine, and an `ignore:` glob means
// the same thing. Anchoring at the filesystem root would make a checkout parked under
// `~/migrate/` read every `.py` file beneath it as a migration, which is severity invented from
// where somebody keeps their source.
//
// Outside a project there is no better anchor than the absolute path, and that is what is used.
// It is consistent, which is the property being bought here, and it costs nothing that could
// have worked: the globs that would break are written in a policy file, and a policy file is
// itself a marker — see projectMarkers.
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

// QualifyPath maps a path the user typed on a command line into the decision namespace.
//
// RootPrefix answers "where is this analysis rooted"; this answers "where is this one file", and
// they need to agree or the two sides of a comparison are back in different namespaces. It is
// what `--terraform-plan` goes through: that flag names a file relative to the user's shell,
// while the engine knows the same file by its path in the changeset, and comparing those two
// spellings directly is what produced the suffix-matching workaround the Terraform analyzer used
// to carry.
//
// The path need not exist. A plan that has not been rendered yet still resolves, and the answer
// is what it will be once it does.
func QualifyPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		// Unresolvable: hand back what was given rather than a guess. It will not match, which
		// is the fail-closed direction — a plan the engine cannot locate is one it does not
		// claim, and an unclaimed .json is not graded against the catalog.
		return filepath.ToSlash(p)
	}

	if repo := projectRoot(filepath.Dir(abs)); repo != "" {
		if within, err := filepath.Rel(repo, abs); err == nil {
			return filepath.ToSlash(within)
		}
	}

	return filepath.ToSlash(abs)
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
	if repo := projectRoot(abs); repo != "" {
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

// projectRoot walks up from dir to the project that contains it, or returns "" if there is none.
//
// The walk is on directory metadata only and stops at the first marker, so it costs a handful of
// stats on the way to a root that is almost always one or two levels up.
func projectRoot(dir string) string {
	for {
		for _, marker := range projectMarkers {
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
