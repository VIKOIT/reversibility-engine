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

// ResolveRoot returns where an analysis is rooted: the prefix that puts an FS provider's paths
// back into the namespace Path
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
//
// **The nearest marker walking up wins.** A monorepo has one `.git` at the top and a
// `.reversibility.yml` per package, and those disagree about where the project starts — so the
// rule has to be stated rather than left to the order of a list. The nearest one is right,
// because a package's policy is written about that package: its globs say `db/migrate/**`, not
// `packages/api/db/migrate/**`, and the run that reads it is a run about that package.
//
// It follows that the answer is sometimes not what a user expects — an outer policy above a
// nearer `.git`, a submodule, a package that acquired a checkout of its own. **That is why the
// anchor is reported.** A user who cannot see which root a glob was resolved against cannot
// debug a pattern that matches nothing, which is the same reason PolicyWarnings exists.
func ResolveRoot(roots []string) Root {
	var (
		common  string
		anchor  string
		first   = true
		agreed  = true
		located = false
	)

	for _, root := range roots {
		at, marker, ok := rootLocation(root)
		if !ok {
			// A root that cannot be resolved anchors nothing. Guessing a prefix would classify
			// files by a location the provider never established, and the empty prefix is the
			// behaviour every caller had before this existed.
			return Root{}
		}

		if first {
			common, anchor, first, located = at, marker, false, true
			continue
		}

		common = commonPath(common, at)
		if marker != anchor {
			// Two roots in two different projects. Neither anchor describes the run, and naming
			// one of them would be worse than naming none.
			agreed = false
		}
	}

	if !located || !agreed {
		anchor = ""
	}

	return Root{Prefix: common, Anchor: anchor}
}

// Root describes where an analysis is rooted, in the namespace decisions are made in.
type Root struct {
	// Prefix is the analysis root's own path, joined onto each changeset path before any
	// path-keyed decision. Empty when the analysis root is the project root.
	//
	// **It is only machine-independent when Anchor is set.** With no project marker there is
	// nothing to be relative to and this is an absolute path, which is why the certificate
	// carries it only alongside an anchor — see the determinism rule in §2.
	Prefix string

	// Anchor names the marker that established the project root — `.git`, `.reversibility.yml`,
	// and so on — or "" when none was found, or when several roots resolved into different
	// projects and no single anchor describes the run.
	//
	// It is the marker's name and never its directory: the directory is a path on this machine
	// and a certificate may not carry one.
	Anchor string
}

// Anchored reports whether a project root was found.
//
// When it is false, paths resolved absolutely and **a project-relative glob cannot match
// anything** — which is worth saying out loud rather than leaving a user to infer from an
// ignore list that does nothing.
func (r Root) Anchored() bool { return r.Anchor != "" }

// QualifyPath maps a path the user typed on a command line into the decision namespace.
//
// ResolveRoot answers "where is this analysis rooted"; this answers "where is this one file", and
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

	at, _ := locate(abs)
	return at
}

// rootSegments resolves one command-line root to the segments it contributes.
//
// It resolves rather than reading the argument, because the argument is exactly what must not
// matter: `./migrations`, `../auth/migrations`, an absolute path, and `.` from inside the
// directory all name the same place and must all classify the same way.
// locate puts one absolute filesystem path into the decision namespace.
//
// **This is the only function that answers that question, and it being the only one is the
// point.** ResolveRoot and QualifyPath are the two sides of a single comparison — where the
// changeset is rooted, and where one named file sits — so a second implementation of the mapping
// is a second chance for the two sides to disagree.
//
// They did disagree, on Linux only, and it is worth recording because it is this session's own
// invariant broken by this session's own code. ResolveRoot split the path into segments, dropped
// the empty one that a leading `/` produces, and rejoined — so `/tmp/x` came back as `tmp/x`.
// QualifyPath returned `/tmp/x`. On Windows an absolute path opens with a drive letter and has no
// empty leading segment, so the two agreed and the defect was invisible; on Linux
// `--terraform-plan` stopped claiming the file it named, and the changeset graded N/A.
//
// The lesson is the one already written in §2: two implementations of one namespace is the defect,
// and the fix is to have one. Not to make the second one more careful.
func locate(abs string) (at, marker string) {
	if project, m := projectRoot(abs); project != "" {
		if within, err := filepath.Rel(project, abs); err == nil {
			return cleanNamespaced(filepath.ToSlash(within)), m
		}
	}
	return cleanNamespaced(filepath.ToSlash(abs)), ""
}

// cleanNamespaced normalizes a namespaced path, mapping "here" to the empty prefix.
//
// path.Clean is what preserves a leading slash, and preserving it is the whole fix: an absolute
// path in this namespace stays absolute, so the two callers of locate produce byte-identical
// strings for the same file.
func cleanNamespaced(p string) string {
	if clean := path.Clean(p); clean != "." {
		return clean
	}
	return ""
}

// rootLocation resolves one command-line root into the decision namespace.
//
// It resolves rather than reading the argument, because the argument is exactly what must not
// matter: `./migrations`, `../auth/migrations`, an absolute path, and `.` from inside the
// directory all name the same place and must all classify the same way.
func rootLocation(root string) (at, marker string, ok bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", false
	}

	// A root naming one file contributes its directory. The provider keys that file by its base
	// name alone — so that `revctl check migrations/0001.sql` reports the path the user typed —
	// which means the directory is exactly what relativising dropped.
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	at, marker = locate(abs)
	return at, marker, true
}

// projectRoot walks up from dir to the project that contains it, or returns "" if there is none.
//
// The walk is on directory metadata only and stops at the first marker, so it costs a handful of
// stats on the way to a root that is almost always one or two levels up.
func projectRoot(dir string) (root, marker string) {
	for {
		// Every marker at this level is considered before ascending, so the NEAREST project root
		// wins over any above it. That is the monorepo ruling: one .git at the top and a
		// .reversibility.yml per package disagree, and the package is right, because the
		// package is what the run is about.
		for _, m := range projectMarkers {
			if _, err := os.Lstat(filepath.Join(dir, m)); err == nil {
				return dir, m
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// commonPath returns the leading path segments a and b agree on.
//
// **Empty segments are compared, not dropped.** The empty segment a leading `/` produces is what
// makes a POSIX path absolute, so discarding it here would turn `/tmp/a` and `/tmp/b` into a
// common prefix of `tmp` — a relative path naming a directory that does not exist, and one that
// no longer matches what QualifyPath returns for the same tree. That was the Linux-only defect;
// see locate.
func commonPath(a, b string) string {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")

	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}

	i := 0
	for i < n && as[i] == bs[i] {
		i++
	}

	return strings.Join(as[:i], "/")
}
