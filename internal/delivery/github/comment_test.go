package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	gh "github.com/google/go-github/v66/github"
)

// fakePoster records what the upsert did, and can be told to paginate or fail.
type fakePoster struct {
	pages [][]*gh.IssueComment

	created []string
	edited  map[int64]string

	listErr   error
	createErr error
	editErr   error

	listCalls int
}

func (f *fakePoster) ListComments(_ context.Context, _, _ string, _ int, opts *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, nil, f.listErr
	}

	page := opts.Page
	if page == 0 {
		page = 1
	}
	if page > len(f.pages) {
		return nil, &gh.Response{}, nil
	}

	resp := &gh.Response{}
	if page < len(f.pages) {
		resp.NextPage = page + 1
	}
	return f.pages[page-1], resp, nil
}

func (f *fakePoster) CreateComment(_ context.Context, _, _ string, _ int, comment *gh.IssueComment) (*gh.IssueComment, *gh.Response, error) {
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
	f.created = append(f.created, comment.GetBody())
	return comment, &gh.Response{}, nil
}

func (f *fakePoster) EditComment(_ context.Context, _, _ string, id int64, comment *gh.IssueComment) (*gh.IssueComment, *gh.Response, error) {
	if f.editErr != nil {
		return nil, nil, f.editErr
	}
	if f.edited == nil {
		f.edited = map[int64]string{}
	}
	f.edited[id] = comment.GetBody()
	return comment, &gh.Response{}, nil
}

func comment(id int64, body string) *gh.IssueComment {
	return &gh.IssueComment{ID: gh.Int64(id), Body: gh.String(body)}
}

var testTarget = pullRequestTarget{owner: "acme", repo: "widgets", number: 42}

func TestUpsertCreatesTheFirstComment(t *testing.T) {
	t.Parallel()

	poster := &fakePoster{pages: [][]*gh.IssueComment{{
		comment(1, "someone else's review note"),
	}}}

	if err := upsertComment(context.Background(), poster, testTarget, "## Grade A"); err != nil {
		t.Fatalf("upsertComment: %v", err)
	}

	if len(poster.created) != 1 {
		t.Fatalf("got %d created comments, want 1", len(poster.created))
	}
	if len(poster.edited) != 0 {
		t.Errorf("an unrelated comment was edited: %v", poster.edited)
	}
	if !strings.Contains(poster.created[0], commentMarker) {
		t.Error("the created comment carries no marker, so the next run cannot find it")
	}
	if !strings.Contains(poster.created[0], "## Grade A") {
		t.Error("the certificate body is missing from the comment")
	}
}

// THE NOISE-REDUCTION CONTRACT. A pull request pushed to twenty times must carry one certificate
// showing the current state, not twenty showing its history. Comment spam is how a bot gets
// muted, and a muted gate protects nobody.
func TestUpsertUpdatesItsOwnComment(t *testing.T) {
	t.Parallel()

	poster := &fakePoster{pages: [][]*gh.IssueComment{{
		comment(1, "a human comment"),
		comment(7, commentMarker+"\n## Grade F (stale)"),
		comment(9, "another human comment"),
	}}}

	if err := upsertComment(context.Background(), poster, testTarget, "## Grade A (fresh)"); err != nil {
		t.Fatalf("upsertComment: %v", err)
	}

	if len(poster.created) != 0 {
		t.Errorf("a duplicate comment was posted: %v", poster.created)
	}
	if len(poster.edited) != 1 {
		t.Fatalf("got %d edits, want 1", len(poster.edited))
	}

	updated, ok := poster.edited[7]
	if !ok {
		t.Fatalf("the wrong comment was edited: %v", poster.edited)
	}
	if !strings.Contains(updated, "## Grade A (fresh)") {
		t.Error("the comment was not updated with the new certificate")
	}
	if strings.Contains(updated, "stale") {
		t.Error("the previous certificate survived the update")
	}
	if !strings.Contains(updated, commentMarker) {
		t.Error("the marker was dropped, so the next run will post a duplicate")
	}
}

// On a long-running pull request the app's comment gets buried under later discussion. Giving up
// at the first page would post a duplicate every time.
func TestUpsertSearchesEveryPage(t *testing.T) {
	t.Parallel()

	poster := &fakePoster{pages: [][]*gh.IssueComment{
		{comment(1, "page one"), comment(2, "still page one")},
		{comment(3, "page two")},
		{comment(4, commentMarker+"\nburied on page three")},
	}}

	if err := upsertComment(context.Background(), poster, testTarget, "## Grade B"); err != nil {
		t.Fatalf("upsertComment: %v", err)
	}

	if len(poster.created) != 0 {
		t.Errorf("a duplicate was posted despite the comment existing on page three: %v", poster.created)
	}
	if _, ok := poster.edited[4]; !ok {
		t.Errorf("the buried comment was not found: edits were %v", poster.edited)
	}
	if poster.listCalls != 3 {
		t.Errorf("listed %d pages, want 3", poster.listCalls)
	}
}

// Failing to post is not something to swallow: a pull request with no certificate looks exactly
// like one that was never analyzed.
func TestUpsertReportsFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		poster *fakePoster
		want   string
	}{
		{
			name:   "listing fails",
			poster: &fakePoster{listErr: errors.New("rate limited")},
			want:   "listing comments",
		},
		{
			name:   "creating fails",
			poster: &fakePoster{pages: [][]*gh.IssueComment{{}}, createErr: errors.New("forbidden")},
			want:   "creating comment",
		},
		{
			name: "editing fails",
			poster: &fakePoster{
				pages:   [][]*gh.IssueComment{{comment(5, commentMarker+"\nold")}},
				editErr: errors.New("gone"),
			},
			want: "updating comment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := upsertComment(context.Background(), tt.poster, testTarget, "body")
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "acme/widgets#42") {
				t.Errorf("error %q does not name the pull request", err)
			}
		})
	}
}

// The marker is an HTML comment so it is invisible in the rendered pull request. If it ever
// became visible, every certificate would carry a line of machine noise.
func TestCommentMarkerIsInvisible(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(commentMarker, "<!--") || !strings.HasSuffix(commentMarker, "-->") {
		t.Errorf("marker %q is not an HTML comment and would render visibly", commentMarker)
	}
}

// Repeated runs must converge on one comment, not accumulate.
func TestUpsertIsIdempotent(t *testing.T) {
	t.Parallel()

	poster := &fakePoster{pages: [][]*gh.IssueComment{{}}}

	// First run creates.
	if err := upsertComment(context.Background(), poster, testTarget, "## Grade A"); err != nil {
		t.Fatalf("upsertComment: %v", err)
	}

	// Subsequent runs see what the first one wrote, and must edit it.
	poster.pages = [][]*gh.IssueComment{{comment(11, poster.created[0])}}

	for i := 0; i < 5; i++ {
		body := fmt.Sprintf("## Grade A (run %d)", i)
		if err := upsertComment(context.Background(), poster, testTarget, body); err != nil {
			t.Fatalf("upsertComment: %v", err)
		}
	}

	if len(poster.created) != 1 {
		t.Errorf("got %d created comments across six runs, want 1", len(poster.created))
	}
	if got := poster.edited[11]; !strings.Contains(got, "run 4") {
		t.Errorf("the comment does not show the latest run: %q", got)
	}
}
