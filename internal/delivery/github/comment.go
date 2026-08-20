// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"
)

// commentMarker identifies a comment this app owns.
//
// It is an HTML comment, so it is invisible in the rendered pull request but survives a
// round trip through the API verbatim. Matching on a marker rather than on the comment text
// means the certificate's wording can change freely without the app losing track of its own
// comment and starting to duplicate it.
const commentMarker = "<!-- reversibility-engine:certificate -->"

// CommentPoster is the slice of the GitHub API this package needs to manage its comment.
//
// It is an interface so the posting logic can be tested without a network, and so the server
// depends on the behaviour rather than on go-github's concrete client.
type CommentPoster interface {
	ListComments(ctx context.Context, owner, repo string, number int, opts *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error)
	CreateComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error)
	EditComment(ctx context.Context, owner, repo string, commentID int64, comment *github.IssueComment) (*github.IssueComment, *github.Response, error)
}

// issueCommentService adapts go-github's client to CommentPoster.
type issueCommentService struct {
	client *github.Client
}

// The three methods below are pure delegation, existing only so the concrete client satisfies
// CommentPoster. Their errors are wrapped by upsertComment and findOwnComment with the pull
// request they concern; wrapping again here would produce "listing comments on acme/widgets#42:
// listing comments on acme/widgets#42: ..." and tell a reader nothing extra.

//nolint:wrapcheck // delegation only; the caller wraps with the pull request identity.
func (s issueCommentService) ListComments(ctx context.Context, owner, repo string, number int, opts *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error) {
	return s.client.Issues.ListComments(ctx, owner, repo, number, opts)
}

//nolint:wrapcheck // delegation only; the caller wraps with the pull request identity.
func (s issueCommentService) CreateComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return s.client.Issues.CreateComment(ctx, owner, repo, number, comment)
}

//nolint:wrapcheck // delegation only; the caller wraps with the pull request identity.
func (s issueCommentService) EditComment(ctx context.Context, owner, repo string, commentID int64, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return s.client.Issues.EditComment(ctx, owner, repo, commentID, comment)
}

// upsertComment posts the certificate, replacing this app's previous comment if it has one.
//
// A pull request that is pushed to twenty times should carry one certificate showing the current
// state, not twenty showing its history. Comment spam is how a bot gets muted, and a muted gate
// protects nobody.
func upsertComment(ctx context.Context, poster CommentPoster, target pullRequestTarget, body string) error {
	marked := commentMarker + "\n" + body

	existing, err := findOwnComment(ctx, poster, target)
	if err != nil {
		return err
	}

	if existing != nil {
		_, _, err := poster.EditComment(ctx, target.owner, target.repo, existing.GetID(),
			&github.IssueComment{Body: github.String(marked)})
		if err != nil {
			return fmt.Errorf("updating comment %d on %s: %w", existing.GetID(), target, err)
		}
		return nil
	}

	if _, _, err := poster.CreateComment(ctx, target.owner, target.repo, target.number,
		&github.IssueComment{Body: github.String(marked)}); err != nil {
		return fmt.Errorf("creating comment on %s: %w", target, err)
	}
	return nil
}

// findOwnComment locates this app's existing certificate comment, or nil if there is none.
//
// Every page is walked rather than stopping at the first: on a long-running pull request the
// app's comment can be buried under later discussion, and giving up early would post a duplicate.
func findOwnComment(ctx context.Context, poster CommentPoster, target pullRequestTarget) (*github.IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}

	for {
		comments, resp, err := poster.ListComments(ctx, target.owner, target.repo, target.number, opts)
		if err != nil {
			return nil, fmt.Errorf("listing comments on %s: %w", target, err)
		}

		for _, comment := range comments {
			if strings.Contains(comment.GetBody(), commentMarker) {
				return comment, nil
			}
		}

		if resp == nil || resp.NextPage == 0 {
			return nil, nil
		}
		opts.Page = resp.NextPage
	}
}
