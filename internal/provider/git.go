// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Errors specific to resolving a changeset from git.
//
// They are distinguishable because the operator's next action differs for each: install git,
// deepen the clone, or fix the ref. None of them is ever recoverable into a grade — every one
// reaches the caller as a failure to fetch the changeset, which is grade F.
var (
	// ErrGitUnavailable means the git binary was not found on PATH.
	ErrGitUnavailable = errors.New("provider: git is not available")

	// ErrNotARepository means the working directory is not inside a git repository.
	ErrNotARepository = errors.New("provider: not a git repository")

	// ErrAmbiguousRef means a ref names more than one object — typically a branch and a tag
	// sharing a name. Picking one of them silently would certify a comparison the user did not
	// ask for.
	ErrAmbiguousRef = errors.New("provider: ambiguous ref")

	// ErrUnknownRef means a ref does not resolve to a commit.
	ErrUnknownRef = errors.New("provider: unknown ref")

	// ErrShallowClone means the repository does not contain the history needed to compare the
	// two refs. It is the most common failure in CI, where the default checkout is shallow.
	ErrShallowClone = errors.New("provider: shallow clone")
)

// GitOptions identifies the comparison a Git provider resolves.
type GitOptions struct {
	// Dir is the directory to run git in. Empty means the process working directory, which is
	// what the CLI passes: the user's shell already selected the repository.
	Dir string

	// Base is the ref the change is measured against. Required.
	Base string

	// Head is the ref holding the change. Empty means HEAD.
	Head string

	// Paths are git pathspecs scoping the comparison to a subtree. Empty means the whole tree.
	Paths []string
}

// Git resolves a changeset from two git refs by shelling out to the git binary.
//
// It reads blobs out of the object database, never out of the working tree. A dirty checkout is
// therefore invisible to it: the certificate describes the refs it names, which is the only
// thing that makes a certificate reproducible by someone else on another machine.
//
// Shelling out rather than linking a git library is deliberate. git's own resolution of a ref,
// a merge base, and a rename is the definition of what those words mean here, and a second
// implementation of them would be a second thing that can disagree with the pull request.
type Git struct {
	bin     string
	dir     string
	base    string
	head    string
	paths   []string
	include Include
}

// DefaultHead is the ref used when none is given.
const DefaultHead = "HEAD"

// NewGit returns a git provider, or an error if git is not usable.
//
// include decides which paths are worth reading; it comes from the engine so that the list of
// interesting extensions lives in one place. A nil include reads every changed file.
func NewGit(opts GitOptions, include Include) (*Git, error) {
	if strings.TrimSpace(opts.Base) == "" {
		return nil, fmt.Errorf("git provider: no base ref given: %w", domain.ErrProviderFailed)
	}

	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("%w: install git or use --before to compare two directories: %w",
			ErrGitUnavailable, err)
	}

	head := strings.TrimSpace(opts.Head)
	if head == "" {
		head = DefaultHead
	}

	return &Git{
		bin:     bin,
		dir:     opts.Dir,
		base:    strings.TrimSpace(opts.Base),
		head:    head,
		paths:   append([]string(nil), opts.Paths...),
		include: include,
	}, nil
}

// ChangedFiles implements FileProvider.
//
// The ref argument is ignored: the refs to compare were given at construction, because they are
// command-line input rather than something an event carries. The parameter stays because the
// interface is shared with the GitHub provider, where a ref is the only thing identifying the
// change.
func (g *Git) ChangedFiles(ctx context.Context, _ domain.ChangeRef) ([]domain.ChangedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("git provider: %w", err)
	}

	if err := g.ensureRepository(ctx); err != nil {
		return nil, err
	}

	baseSHA, err := g.resolve(ctx, g.base)
	if err != nil {
		return nil, err
	}
	headSHA, err := g.resolve(ctx, g.head)
	if err != nil {
		return nil, err
	}

	// The three-dot form compares head against the merge base, which is what a pull request
	// shows. Blobs on the previous side must therefore be read at the merge base too — reading
	// them at the base ref would pair each old file with a newer sibling and describe a
	// transition nobody is proposing.
	mergeBase, err := g.mergeBase(ctx, baseSHA, headSHA)
	if err != nil {
		return nil, err
	}

	changes, err := g.diff(ctx, baseSHA, headSHA)
	if err != nil {
		return nil, err
	}

	files, err := g.readChanges(ctx, changes, mergeBase, headSHA)
	if err != nil {
		return nil, err
	}

	contextFiles, err := g.readContext(ctx, files, headSHA)
	if err != nil {
		return nil, err
	}
	files = append(files, contextFiles...)

	sortByPath(files)
	return files, nil
}

// gitChange is one entry of `git diff --name-status`.
type gitChange struct {
	status byte
	src    string // the path on the previous side
	dst    string // the path on the new side; equal to src unless renamed or copied
}

// ensureRepository fails before anything else if there is no repository to read.
func (g *Git) ensureRepository(ctx context.Context) error {
	if _, stderr, err := g.run(ctx, "rev-parse", "--show-toplevel"); err != nil {
		return fmt.Errorf("%w: %s: %s", ErrNotARepository, g.where(), stderr)
	}
	return nil
}

// resolve turns a ref into the commit SHA it names.
//
// Resolving up front and then using SHAs for everything downstream means a branch that moves
// mid-run cannot produce a changeset assembled from two different commits.
func (g *Git) resolve(ctx context.Context, ref string) (string, error) {
	// ^{commit} rejects a ref that names a tree or a blob, which would otherwise reach the diff
	// and fail there with a far less obvious message.
	out, stderr, err := g.run(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		if g.isShallow(ctx) {
			return "", fmt.Errorf("%w: ref %q is not in this shallow clone; "+
				"set fetch-depth: 0 on actions/checkout, or fetch the ref before running: %s",
				ErrShallowClone, ref, stderr)
		}
		return "", fmt.Errorf("%w: %q does not name a commit: %s", ErrUnknownRef, ref, stderr)
	}

	// git resolves an ambiguous ref by precedence and warns. Accepting the warning would mean
	// certifying whichever object git happened to prefer.
	if strings.Contains(stderr, "ambiguous") {
		return "", fmt.Errorf("%w: %q matches more than one object; qualify it as "+
			"refs/heads/%s or refs/tags/%s: %s", ErrAmbiguousRef, ref, ref, ref, stderr)
	}

	return trimmed(out), nil
}

// mergeBase returns the commit the two refs diverged from.
func (g *Git) mergeBase(ctx context.Context, baseSHA, headSHA string) (string, error) {
	out, stderr, err := g.run(ctx, "merge-base", baseSHA, headSHA)
	if err != nil {
		if g.isShallow(ctx) {
			return "", fmt.Errorf("%w: %s and %s have no common ancestor in this shallow clone; "+
				"set fetch-depth: 0 on actions/checkout so the base commit is present: %s",
				ErrShallowClone, short(baseSHA), short(headSHA), stderr)
		}
		return "", fmt.Errorf("git provider: %s and %s have no common ancestor: %s: %w",
			short(baseSHA), short(headSHA), stderr, domain.ErrProviderFailed)
	}
	return trimmed(out), nil
}

// isShallow reports whether the repository was cloned with truncated history.
//
// It is only ever consulted on a path that has already failed, to turn an unhelpful "unknown
// revision" into the fix the operator actually needs.
func (g *Git) isShallow(ctx context.Context) bool {
	out, _, err := g.run(ctx, "rev-parse", "--is-shallow-repository")
	return err == nil && trimmed(out) == "true"
}

// diff lists the changes between the merge base of the two commits and head.
func (g *Git) diff(ctx context.Context, baseSHA, headSHA string) ([]gitChange, error) {
	args := []string{
		"diff", "--name-status", "-z", "--no-color",
		// Rename detection is on by default in current git but configurable per repository.
		// Asking for it explicitly keeps the result from depending on the user's config.
		"--find-renames",
		baseSHA + "..." + headSHA,
	}
	if len(g.paths) > 0 {
		args = append(args, "--")
		args = append(args, g.paths...)
	}

	out, stderr, err := g.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("git provider: diffing %s...%s: %s: %w",
			short(baseSHA), short(headSHA), stderr, domain.ErrProviderFailed)
	}

	return parseNameStatus(out)
}

// parseNameStatus decodes the NUL-separated output of `git diff --name-status -z`.
//
// The -z form is used rather than the human one because a path may contain a quote, a backslash,
// or a newline, and the quoted form would have to be unescaped by hand — a parser whose bugs
// would silently drop files from the changeset.
func parseNameStatus(out []byte) ([]gitChange, error) {
	fields := strings.Split(string(out), "\x00")

	var changes []gitChange

	for i := 0; i < len(fields); {
		field := fields[i]
		if field == "" {
			i++
			continue
		}

		status := field[0]
		switch status {
		case 'R', 'C':
			// Renames and copies carry both paths: "R100\0old\0new".
			if i+2 >= len(fields) {
				return nil, truncatedDiff(field)
			}
			src, dst := fields[i+1], fields[i+2]
			if src == "" || dst == "" {
				return nil, truncatedDiff(field)
			}
			changes = append(changes, gitChange{status: status, src: src, dst: dst})
			i += 3

		case 'A', 'M', 'D', 'T':
			if i+1 >= len(fields) {
				return nil, truncatedDiff(field)
			}
			p := fields[i+1]
			if p == "" {
				return nil, truncatedDiff(field)
			}
			changes = append(changes, gitChange{status: status, src: p, dst: p})
			i += 2

		default:
			// U (unmerged) and X (a bug in git, by git's own documentation) both land here.
			// Neither can be classified, and guessing at one would put a file into the
			// changeset with the wrong side populated.
			return nil, fmt.Errorf("git provider: unrecognised diff status %q: %w",
				field, domain.ErrProviderFailed)
		}
	}

	return changes, nil
}

func truncatedDiff(field string) error {
	return fmt.Errorf("git provider: truncated diff output at status %q: %w", field, domain.ErrProviderFailed)
}

// readChanges turns diff entries into changed files with both sides populated.
func (g *Git) readChanges(ctx context.Context, changes []gitChange, previousRev, currentRev string) ([]domain.ChangedFile, error) {
	var out []domain.ChangedFile

	appendAdded := func(p string) error {
		if !g.included(p) {
			return nil
		}
		content, err := g.readBlob(ctx, currentRev, p)
		if err != nil {
			return err
		}
		out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusAdded, Current: content})
		return nil
	}

	appendRemoved := func(p string) error {
		if !g.included(p) {
			return nil
		}
		content, err := g.readBlob(ctx, previousRev, p)
		if err != nil {
			return err
		}
		out = append(out, domain.ChangedFile{Path: p, Status: domain.StatusRemoved, Previous: content})
		return nil
	}

	for _, c := range changes {
		switch c.status {
		case 'A':
			if err := appendAdded(c.dst); err != nil {
				return nil, err
			}

		case 'D':
			if err := appendRemoved(c.src); err != nil {
				return nil, err
			}

		case 'M', 'T':
			if !g.included(c.dst) {
				continue
			}
			previous, err := g.readBlob(ctx, previousRev, c.src)
			if err != nil {
				return nil, err
			}
			current, err := g.readBlob(ctx, currentRev, c.dst)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.ChangedFile{
				Path:     c.dst,
				Status:   domain.StatusModified,
				Previous: previous,
				Current:  current,
			})

		case 'R':
			// A rename is reported as a delete plus an add, and deliberately not as
			// StatusRenamed. The Kubernetes rules compare whole objects: moving a manifest
			// removes an object from one path and introduces it at another, and K8S003 and
			// K8S009 need to see the removal to ask what still depends on it. Collapsing the
			// two into one entry would hide exactly that.
			if err := appendRemoved(c.src); err != nil {
				return nil, err
			}
			if err := appendAdded(c.dst); err != nil {
				return nil, err
			}

		case 'C':
			// A copy leaves the source in place, so only the new path is a change.
			if err := appendAdded(c.dst); err != nil {
				return nil, err
			}
		}
	}

	return out, nil
}

// readContext reads unchanged files sitting alongside the changed ones.
//
// Some rules cannot be decided from the changed files alone: K8S003 needs the StorageClass
// behind a deleted claim, and K8S009 needs the workload that still mounts a deleted ConfigMap.
// Neither appears in a diff, because neither was edited. The GitHub provider already supplies
// them (CLAUDE.md §11c); without the same behaviour here, the same pull request would grade
// differently from the CLI than from the app, and the more permissive of the two answers would
// be the one a developer saw first.
//
// The search is bounded to the directories the change already touches. A rule whose context lies
// outside that scope still sees nothing and must return UNKNOWN rather than assume safety.
func (g *Git) readContext(ctx context.Context, changed []domain.ChangedFile, rev string) ([]domain.ChangedFile, error) {
	known := make(map[string]bool, len(changed))
	dirs := map[string]bool{}

	for _, f := range changed {
		known[f.Path] = true
		dirs[path.Dir(f.Path)] = true
	}

	if len(dirs) == 0 {
		return nil, nil
	}

	candidates, err := g.treePaths(ctx, rev)
	if err != nil {
		return nil, err
	}

	var out []domain.ChangedFile

	for _, candidate := range candidates {
		if known[candidate] || !dirs[path.Dir(candidate)] || !g.included(candidate) {
			continue
		}
		known[candidate] = true

		content, err := g.readBlob(ctx, rev, candidate)
		if err != nil {
			return nil, err
		}

		// Reported as MODIFIED with identical sides, matching the fake, filesystem, and
		// GitHub providers. The analyzers treat a file whose content did not change as
		// context and generate no findings for it.
		out = append(out, domain.ChangedFile{
			Path:     candidate,
			Status:   domain.StatusModified,
			Previous: content,
			Current:  content,
		})

		if len(out) > maxContextFiles {
			return nil, fmt.Errorf("%w: more than %d context files", ErrChangesetTooLarge, maxContextFiles)
		}
	}

	return out, nil
}

// emptyTree is git's constant hash of the empty tree object.
//
// Diffing it against a revision lists every file at that revision. It is a documented constant
// of the SHA-1 object format rather than a value this package invented.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// treePaths lists every file at a revision that the caller's pathspecs select.
//
// It is a diff against the empty tree rather than an ls-tree walk because ls-tree does not
// support exclude pathspecs (`:!vendor/**`), and `git diff` does. Context files have to obey the
// same scoping as the changeset: a path the user excluded must not come back through the side
// door, which is precisely what happens when an excluded manifest shares a directory with a
// changed one. Using the same command for both means one definition of what a pathspec selects,
// and it is git's.
func (g *Git) treePaths(ctx context.Context, rev string) ([]string, error) {
	args := []string{"diff", "--name-only", "-z", "--no-color", "--no-renames", "--diff-filter=A", emptyTree, rev}
	if len(g.paths) > 0 {
		args = append(args, "--")
		args = append(args, g.paths...)
	}

	out, stderr, err := g.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("git provider: listing the tree at %s: %s: %w",
			short(rev), stderr, domain.ErrProviderFailed)
	}

	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}

	sort.Strings(paths)
	return paths, nil
}

// readBlob reads one file's content at a revision, out of the object database.
func (g *Git) readBlob(ctx context.Context, rev, filePath string) ([]byte, error) {
	spec := rev + ":" + filePath

	// The size is checked before the content is read. A blob is bounded only by what somebody
	// committed, and reading it to find out how big it is defeats the limit.
	sizeOut, stderr, err := g.run(ctx, "cat-file", "-s", spec)
	if err != nil {
		return nil, fmt.Errorf("git provider: reading %s: %s: %w", spec, stderr, domain.ErrProviderFailed)
	}
	size, err := parseSize(trimmed(sizeOut))
	if err != nil {
		return nil, fmt.Errorf("git provider: reading the size of %s: %w", spec, err)
	}
	if size > maxFileBytes {
		return nil, fmt.Errorf("git provider: %s is %d bytes, over the %d limit: %w",
			spec, size, maxFileBytes, domain.ErrProviderFailed)
	}

	// --no-textconv: a repository may configure a textconv filter for an extension, and running
	// one would analyze somebody's rendering of the file rather than the file.
	content, stderr, err := g.run(ctx, "show", "--no-textconv", spec)
	if err != nil {
		return nil, fmt.Errorf("git provider: reading %s: %s: %w", spec, stderr, domain.ErrProviderFailed)
	}

	return content, nil
}

func parseSize(s string) (int, error) {
	size := 0
	if s == "" {
		return 0, fmt.Errorf("empty size: %w", domain.ErrProviderFailed)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-numeric size %q: %w", s, domain.ErrProviderFailed)
		}
		size = size*10 + int(r-'0')
		if size > maxFileBytes {
			// Larger than anything that will be accepted; stop before overflowing.
			return size, nil
		}
	}
	return size, nil
}

// run executes git and returns its stdout, its trimmed stderr, and the process error.
//
// stderr is returned rather than logged because git puts both the reason a command failed and
// its ambiguity warnings there, and both are decisions this provider has to make.
func (g *Git) run(ctx context.Context, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = g.dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// GIT_OPTIONAL_LOCKS=0 keeps a read from taking the index lock or refreshing it. Analysis
	// must never modify the repository it is describing, and a concurrent build holding the
	// lock must never turn into a failed certificate.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")

	err := cmd.Run()
	return stdout.Bytes(), strings.TrimSpace(stderr.String()), err
}

func (g *Git) included(p string) bool {
	if g.include == nil {
		return true
	}
	return g.include(p)
}

// where names the directory git was run in, for an error a user can act on.
func (g *Git) where() string {
	if g.dir != "" {
		return g.dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "the working directory"
}

func trimmed(b []byte) string { return strings.TrimSpace(string(b)) }

// short abbreviates a SHA for a message. Errors are read by people.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
