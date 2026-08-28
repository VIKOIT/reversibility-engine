// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Limits on what a single pull request may drag in.
//
// These are not performance tuning. A provider with no ceiling lets one hostile or accidental
// pull request exhaust the API budget or the process's memory, and a provider that dies halfway
// through would hand the engine a partial changeset — which is exactly the incomplete analysis
// that must never produce a passing grade.
const (
	maxChangedFiles = 500
	maxContextFiles = 200
	maxFileBytes    = 2 << 20 // 2 MiB; a rendered manifest or migration far larger than this is not one.
)

// ErrChangesetTooLarge means the pull request exceeds what the provider will fetch.
//
// It is an error rather than a truncation on purpose: analyzing the first 500 files of a
// 900-file change and reporting a grade would be a confident answer about something the engine
// only half looked at.
var ErrChangesetTooLarge = errors.New("provider: changeset exceeds the fetch limit")

// GitHub resolves a changeset from the GitHub API.
//
// Every failure is returned, never swallowed. A rate limit, a network blip, or a file too large
// to fetch all end the same way: the caller gets an error and grades F. Silently analyzing an
// incomplete diff is the one outcome this provider must never produce.
type GitHub struct {
	client *github.Client
}

// NewGitHub returns a provider backed by the given client.
//
// It no longer takes a predicate: choosing what to fetch is the caller's decision, made against
// the complete listing. See Select.
//
// This provider is the one where the split pays for itself directly. The comparison API returns
// the filenames and the directory listing API returns sibling names, both without file bodies —
// so enumerating the whole changeset costs the calls it already made, and only the chosen paths
// turn into content requests. The alternative considered and rejected was to fetch every file in
// every touched directory so the engine could filter, which is an API-quota decision rather than
// an engineering one.
func NewGitHub(client *github.Client) *GitHub {
	return &GitHub{client: client}
}

// Ref builds a ChangeRef identifying a comparison between two commits in a repository.
func Ref(owner, repo, base, head string) domain.ChangeRef {
	return domain.ChangeRef(fmt.Sprintf("%s/%s@%s...%s", owner, repo, base, head))
}

// parsedRef is a decoded ChangeRef.
type parsedRef struct {
	owner, repo, base, head string
}

func parseRef(ref domain.ChangeRef) (parsedRef, error) {
	s := string(ref)

	at := strings.LastIndex(s, "@")
	if at < 0 {
		return parsedRef{}, fmt.Errorf("github provider: malformed ref %q: want owner/repo@base...head", ref)
	}

	repoPart, rangePart := s[:at], s[at+1:]

	slash := strings.Index(repoPart, "/")
	if slash < 0 {
		return parsedRef{}, fmt.Errorf("github provider: malformed ref %q: want owner/repo@base...head", ref)
	}

	base, head, found := strings.Cut(rangePart, "...")
	if !found || base == "" || head == "" {
		return parsedRef{}, fmt.Errorf("github provider: malformed ref %q: want owner/repo@base...head", ref)
	}

	p := parsedRef{owner: repoPart[:slash], repo: repoPart[slash+1:], base: base, head: head}
	if p.owner == "" || p.repo == "" {
		return parsedRef{}, fmt.Errorf("github provider: malformed ref %q: want owner/repo@base...head", ref)
	}
	return p, nil
}

// List implements FileProvider.
//
// The comparison API returns filenames, and the contents API is what costs a request per file.
// Enumerating the whole changeset therefore costs the calls this provider already made, and only
// the chosen paths become content requests.
func (g *GitHub) List(ctx context.Context, ref domain.ChangeRef) ([]Path, error) {
	if g.client == nil {
		return nil, fmt.Errorf("github provider: no client configured: %w", domain.ErrProviderFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("github provider: %w", err)
	}

	r, err := parseRef(ref)
	if err != nil {
		return nil, err
	}

	changed, err := g.compare(ctx, r)
	if err != nil {
		return nil, err
	}

	out := make([]Path, 0, len(changed))
	for _, f := range changed {
		out = append(out, Path{
			Path:     f.GetFilename(),
			PrevPath: f.GetPreviousFilename(),
			Status:   translateStatus(f.GetStatus()),
		})
	}

	siblings, err := g.listContext(ctx, r, out)
	if err != nil {
		return nil, err
	}
	out = append(out, siblings...)

	SortPaths(out)
	return out, nil
}

// Read implements FileProvider. It fetches content for exactly the paths given.
func (g *GitHub) Read(ctx context.Context, ref domain.ChangeRef, paths []Path) ([]domain.ChangedFile, error) {
	if g.client == nil {
		return nil, fmt.Errorf("github provider: no client configured: %w", domain.ErrProviderFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("github provider: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil
	}

	// Every Path carries the ref it belongs to via the listing, so Read needs the same ref the
	// listing was made against. It is recovered from the caller's paths rather than stored on
	// the provider, which keeps this value stateless and safe to share.
	r, err := parseRef(ref)
	if err != nil {
		return nil, err
	}

	var out []domain.ChangedFile

	for _, p := range paths {
		file := domain.ChangedFile{
			Path:         p.Path,
			Status:       p.Status,
			PreviousPath: p.PrevPath,
		}

		// The previous side is what makes a transition classifiable: whether an ALTER widens or
		// narrows, whether a PVC grew or shrank. Fetching only the new content would leave the
		// Kubernetes rules unable to answer anything.
		if p.Status != domain.StatusAdded {
			previousPath := p.Path
			if p.PrevPath != "" {
				previousPath = p.PrevPath
			}

			content, err := g.fetchFile(ctx, r, previousPath, r.base)
			if err != nil {
				return nil, err
			}
			file.Previous = content
		}

		if p.Status != domain.StatusRemoved {
			content, err := g.fetchFile(ctx, r, p.Path, r.head)
			if err != nil {
				return nil, err
			}
			file.Current = content
		}

		out = append(out, file)
	}

	sortByPath(out)
	return out, nil
}

// compare walks the paginated comparison between base and head.
func (g *GitHub) compare(ctx context.Context, r parsedRef) ([]*github.CommitFile, error) {
	var out []*github.CommitFile

	opts := &github.ListOptions{PerPage: 100}
	for {
		comparison, resp, err := g.client.Repositories.CompareCommits(ctx, r.owner, r.repo, r.base, r.head, opts)
		if err != nil {
			return nil, apiError("comparing %s...%s", err, r.base, r.head)
		}

		out = append(out, comparison.Files...)
		if len(out) > maxChangedFiles {
			return nil, fmt.Errorf("%w: %d files exceeds the limit of %d", ErrChangesetTooLarge, len(out), maxChangedFiles)
		}

		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// fetchContext fetches unchanged files that sit alongside the changed ones.
//
// Some rules cannot be decided from the changed files alone: K8S003 needs the StorageClass
// behind a deleted claim, and K8S009 needs the workload that still mounts a deleted ConfigMap.
// Neither appears in a pull request diff, because neither was edited.
//
// The search is bounded to the directories the change already touches. That covers the layout
// these rules actually care about — a chart or kustomize directory holding related manifests —
// without walking the repository. A rule whose context lies outside that scope still sees
// nothing, and per docs/SPECIFICATION.md §16.4 must return UNKNOWN rather than assume safety.
func (g *GitHub) listContext(ctx context.Context, r parsedRef, changed []Path) ([]Path, error) {
	known := make(map[string]bool, len(changed))
	dirs := map[string]bool{}

	for _, f := range changed {
		known[f.Path] = true
		if f.PrevPath != "" {
			known[f.PrevPath] = true
		}
		dirs[path.Dir(f.Path)] = true
	}

	var out []Path

	for _, dir := range sortedStrings(dirs) {
		siblings, err := g.listDirectory(ctx, r, dir)
		if err != nil {
			return nil, err
		}

		for _, sibling := range siblings {
			if known[sibling] {
				continue
			}
			known[sibling] = true

			// Reported as MODIFIED with identical sides once read, matching the fake and
			// filesystem providers. The analyzers treat a file whose content did not change as
			// context and generate no findings for it.
			out = append(out, Path{
				Path:   sibling,
				Status: domain.StatusModified,
			})

			if len(out) > maxContextFiles {
				return nil, fmt.Errorf("%w: more than %d context files", ErrChangesetTooLarge, maxContextFiles)
			}
		}
	}

	return out, nil
}

// listDirectory returns the file paths directly inside a directory at the base commit.
//
// A directory that does not exist at base is not an error: the change may have created it.
func (g *GitHub) listDirectory(ctx context.Context, r parsedRef, dir string) ([]string, error) {
	if dir == "." {
		dir = ""
	}

	_, entries, resp, err := g.client.Repositories.GetContents(ctx, r.owner, r.repo, dir,
		&github.RepositoryContentGetOptions{Ref: r.base})
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, apiError("listing directory %q at %s", err, dir, r.base)
	}

	var out []string
	for _, entry := range entries {
		if entry.GetType() != "file" {
			continue
		}
		out = append(out, entry.GetPath())
	}

	sort.Strings(out)
	return out, nil
}

// fetchFile reads one file's contents at a commit.
func (g *GitHub) fetchFile(ctx context.Context, r parsedRef, filePath, sha string) ([]byte, error) {
	file, _, _, err := g.client.Repositories.GetContents(ctx, r.owner, r.repo, filePath,
		&github.RepositoryContentGetOptions{Ref: sha})
	if err != nil {
		return nil, apiError("fetching %s at %s", err, filePath, sha)
	}
	if file == nil {
		return nil, fmt.Errorf("github provider: %s at %s is not a file: %w", filePath, sha, domain.ErrProviderFailed)
	}

	if size := file.GetSize(); size > maxFileBytes {
		return nil, fmt.Errorf("github provider: %s at %s is %d bytes, over the %d limit: %w",
			filePath, sha, size, maxFileBytes, domain.ErrProviderFailed)
	}

	// GetContent decodes the base64 payload. It fails for files GitHub declines to inline,
	// which is a fetch failure like any other and must not be mistaken for an empty file.
	content, err := file.GetContent()
	if err != nil {
		return nil, fmt.Errorf("github provider: decoding %s at %s: %w", filePath, sha, err)
	}

	return []byte(content), nil
}

// translateStatus maps GitHub's file status onto the change model.
//
// An unrecognised status becomes MODIFIED, which is the conservative choice: it makes the engine
// fetch and compare both sides rather than assume one of them is absent.
func translateStatus(status string) domain.ChangeStatus {
	switch status {
	case "added", "copied":
		return domain.StatusAdded
	case "removed":
		return domain.StatusRemoved
	case "renamed":
		return domain.StatusRenamed
	default:
		return domain.StatusModified
	}
}

// apiError wraps a GitHub failure, preserving the distinction between a rate limit and anything
// else so an operator can tell "wait" from "something is broken".
func apiError(format string, err error, args ...any) error {
	context := fmt.Sprintf(format, args...)

	var rateLimit *github.RateLimitError
	if errors.As(err, &rateLimit) {
		return fmt.Errorf("github provider: %s: rate limited until %s: %w",
			context, rateLimit.Rate.Reset.Time.UTC().Format("15:04:05Z"), err)
	}

	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		return fmt.Errorf("github provider: %s: secondary rate limit: %w", context, err)
	}

	return fmt.Errorf("github provider: %s: %w", context, err)
}

func sortedStrings(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
