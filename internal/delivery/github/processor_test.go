package github_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	ghapi "github.com/google/go-github/v66/github"

	gh "github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

// stubGitHub is a stand-in GitHub covering both halves of a run: the files the provider fetches
// and the comments the processor posts.
type stubGitHub struct {
	*httptest.Server
	mux *http.ServeMux

	mu       sync.Mutex
	posted   []string
	edited   []string
	existing []*ghapi.IssueComment
}

func newStubGitHub(t *testing.T) *stubGitHub {
	t.Helper()

	s := &stubGitHub{mux: http.NewServeMux()}
	s.Server = httptest.NewServer(s.mux)
	t.Cleanup(s.Close)

	s.mux.HandleFunc("GET /repos/acme/widgets/issues/42/comments", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.existing)
	})

	s.mux.HandleFunc("POST /repos/acme/widgets/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		var comment ghapi.IssueComment
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &comment)

		s.mu.Lock()
		s.posted = append(s.posted, comment.GetBody())
		s.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(comment)
	})

	s.mux.HandleFunc("PATCH /repos/acme/widgets/issues/comments/{id}", func(w http.ResponseWriter, r *http.Request) {
		var comment ghapi.IssueComment
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &comment)

		s.mu.Lock()
		s.edited = append(s.edited, comment.GetBody())
		s.mu.Unlock()

		_ = json.NewEncoder(w).Encode(comment)
	})

	return s
}

func (s *stubGitHub) factory(t *testing.T) gh.ClientFactory {
	t.Helper()

	base, err := url.Parse(s.URL + "/")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}

	return func(context.Context, int64) (*ghapi.Client, error) {
		client := ghapi.NewClient(nil)
		client.BaseURL = base
		return client, nil
	}
}

func (s *stubGitHub) comments() (posted, edited []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.posted...), append([]string(nil), s.edited...)
}

// serveChangeset wires the compare and contents endpoints for a set of files.
func (s *stubGitHub) serveChangeset(files map[string]string) {
	var listed []map[string]any
	for name := range files {
		listed = append(listed, map[string]any{"filename": name, "status": "added"})
	}

	s.mux.HandleFunc("GET /repos/acme/widgets/compare/{spec...}", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ahead", "files": listed})
	})

	for name, content := range files {
		name, content := name, content
		s.mux.HandleFunc("GET /repos/acme/widgets/contents/"+name, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "name": name, "path": name, "size": len(content),
				"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content)),
			})
		})
	}

	// Directory listings for context files; empty is fine for these tests.
	s.mux.HandleFunc("GET /repos/acme/widgets/contents/{dir...}", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
}

func runProcessor(t *testing.T, stub *stubGitHub) {
	t.Helper()

	processor := gh.NewProcessor(stub.factory(t), nil)
	handler := gh.NewHandler(testSecret, processor, gh.WithSynchronousProcessing())

	body := prEvent("opened", nil)
	rec := deliver(t, handler, "pull_request", sign(body), body)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

// The whole path: signed webhook, files fetched, changeset graded, certificate posted.
func TestEndToEndPostsACertificate(t *testing.T) {
	t.Parallel()

	stub := newStubGitHub(t)
	stub.serveChangeset(map[string]string{
		"migrations/0001_drop.up.sql": "DROP TABLE legacy_orders;\n",
	})

	runProcessor(t, stub)

	posted, edited := stub.comments()
	if len(posted) != 1 {
		t.Fatalf("got %d posted comments, want 1 (edited: %d)", len(posted), len(edited))
	}

	comment := posted[0]
	if !strings.Contains(comment, "Grade F") {
		t.Errorf("the certificate does not report grade F:\n%s", comment)
	}
	if !strings.Contains(comment, "PG001") {
		t.Errorf("the certificate does not name the blocking rule:\n%s", comment)
	}
	if !strings.Contains(comment, "<!-- reversibility-engine:certificate -->") {
		t.Error("the comment carries no marker, so the next run will duplicate it")
	}
}

// A reversible change must reach grade A end to end, or the gate can never open.
func TestEndToEndGradeA(t *testing.T) {
	t.Parallel()

	stub := newStubGitHub(t)
	stub.serveChangeset(map[string]string{
		"migrations/0001_i.up.sql":   "CREATE INDEX CONCURRENTLY i ON orders (status);\n",
		"migrations/0001_i.down.sql": "DROP INDEX CONCURRENTLY i;\n",
	})

	runProcessor(t, stub)

	posted, _ := stub.comments()
	if len(posted) != 1 {
		t.Fatalf("got %d posted comments, want 1", len(posted))
	}
	if !strings.Contains(posted[0], "Grade A") {
		t.Errorf("a fully reversible change did not reach grade A:\n%s", posted[0])
	}
	if !strings.Contains(posted[0], "✅ PASS") {
		t.Errorf("the gate did not pass:\n%s", posted[0])
	}
}

// THE FAIL-CLOSED NETWORK CONTRACT, end to end. When the API fails during the fetch, the pull
// request must still receive a grade F naming the failure. Silence is the one unacceptable
// outcome: a pull request with no certificate looks exactly like one that was never analyzed.
func TestAPIFailureStillPostsGradeF(t *testing.T) {
	t.Parallel()

	stub := newStubGitHub(t)
	stub.mux.HandleFunc("GET /repos/acme/widgets/compare/{spec...}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})

	runProcessor(t, stub)

	posted, _ := stub.comments()
	if len(posted) != 1 {
		t.Fatalf("a rate-limited run posted %d comments, want 1", len(posted))
	}

	comment := posted[0]
	if !strings.Contains(comment, "Grade F") {
		t.Errorf("a failed fetch did not grade F:\n%s", comment)
	}
	if !strings.Contains(comment, "could not be retrieved") {
		t.Errorf("the certificate does not explain that the changeset was unavailable:\n%s", comment)
	}
	if !strings.Contains(comment, "❌ FAIL") {
		t.Errorf("the gate did not fail:\n%s", comment)
	}
	if !strings.Contains(comment, "NO COMPLETE UNDO") {
		t.Errorf("the undo plan claims completeness for a change that was never read:\n%s", comment)
	}
}

// A failure fetching one file, after the comparison succeeded, must fail the same way. Grading
// the files that did arrive would be a confident answer about a change half seen.
func TestPartialFetchFailureStillPostsGradeF(t *testing.T) {
	t.Parallel()

	stub := newStubGitHub(t)
	stub.mux.HandleFunc("GET /repos/acme/widgets/compare/{spec...}", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ahead", "files": []map[string]any{
			{"filename": "migrations/0001_safe.up.sql", "status": "added"},
			{"filename": "migrations/0002_unreadable.up.sql", "status": "added"},
		}})
	})
	stub.mux.HandleFunc("GET /repos/acme/widgets/contents/migrations/0001_safe.up.sql", func(w http.ResponseWriter, _ *http.Request) {
		content := "CREATE INDEX CONCURRENTLY i ON t (c);\n"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "file", "path": "migrations/0001_safe.up.sql", "size": len(content),
			"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
	})
	stub.mux.HandleFunc("GET /repos/acme/widgets/contents/migrations/0002_unreadable.up.sql", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	runProcessor(t, stub)

	posted, _ := stub.comments()
	if len(posted) != 1 {
		t.Fatalf("got %d posted comments, want 1", len(posted))
	}
	if !strings.Contains(posted[0], "Grade F") {
		t.Errorf("a partial fetch produced a passing grade:\n%s", posted[0])
	}
}

// The second push to a pull request must update the existing certificate, not add another.
func TestSecondRunUpdatesRatherThanDuplicates(t *testing.T) {
	t.Parallel()

	stub := newStubGitHub(t)
	stub.serveChangeset(map[string]string{
		"migrations/0001_drop.up.sql": "DROP TABLE t;\n",
	})

	runProcessor(t, stub)

	posted, _ := stub.comments()
	if len(posted) != 1 {
		t.Fatalf("first run posted %d comments, want 1", len(posted))
	}

	// The pull request now carries the comment the first run wrote.
	stub.mu.Lock()
	stub.existing = []*ghapi.IssueComment{{ID: ghapi.Int64(77), Body: ghapi.String(posted[0])}}
	stub.mu.Unlock()

	runProcessor(t, stub)

	posted, edited := stub.comments()
	if len(posted) != 1 {
		t.Errorf("the second run posted a duplicate: %d comments total", len(posted))
	}
	if len(edited) != 1 {
		t.Errorf("the second run made %d edits, want 1", len(edited))
	}
}

// Authentication failing means nothing can be posted either, so it can only be reported upward.
func TestAuthenticationFailureIsReported(t *testing.T) {
	t.Parallel()

	failing := func(context.Context, int64) (*ghapi.Client, error) {
		return nil, io.ErrUnexpectedEOF
	}

	processor := gh.NewProcessor(failing, nil)
	handler := gh.NewHandler(testSecret, processor, gh.WithSynchronousProcessing())

	body := prEvent("opened", nil)
	rec := deliver(t, handler, "pull_request", sign(body), body)

	// GitHub still gets its acknowledgement; the failure is only actionable in the logs.
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
}
