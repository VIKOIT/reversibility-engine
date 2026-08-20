// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/provider"
)

// fakeAPI is a stand-in GitHub, so the provider is exercised over real HTTP with real JSON
// decoding rather than against a mocked-out client.
type fakeAPI struct {
	*httptest.Server
	mux *http.ServeMux

	// calls records every path requested, so a test can assert what the provider did and did
	// not fetch.
	calls []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{mux: http.NewServeMux()}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.calls = append(api.calls, r.URL.Path)
		api.mux.ServeHTTP(w, r)
	}))

	t.Cleanup(api.Close)
	return api
}

func (a *fakeAPI) client(t *testing.T) *github.Client {
	t.Helper()

	base, err := url.Parse(a.URL + "/")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}

	client := github.NewClient(nil)
	client.BaseURL = base
	return client
}

func (a *fakeAPI) handle(pattern string, handler http.HandlerFunc) {
	a.mux.HandleFunc(pattern, handler)
}

func (a *fakeAPI) json(pattern string, payload any) {
	a.handle(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}

// contentsResponse builds the payload GitHub returns for a single file.
func contentsResponse(path, content string) map[string]any {
	return map[string]any{
		"type":     "file",
		"name":     path,
		"path":     path,
		"size":     len(content),
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func comparison(files ...map[string]any) map[string]any {
	return map[string]any{"status": "ahead", "files": files}
}

func changedFile(name, status string) map[string]any {
	return map[string]any{"filename": name, "status": status}
}

// contentsPath is the API path for a file, which the provider hits once per commit.
func contentsPath(name string) string { return "/repos/acme/widgets/contents/" + name }

func sqlInclude(path string) bool {
	return strings.HasSuffix(path, ".sql") || strings.HasSuffix(path, ".yaml")
}

func TestGitHubFetchesBothSidesOfAChange(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.json("/repos/acme/widgets/compare/base...head", comparison(
		changedFile("migrations/0001.up.sql", "modified"),
		changedFile("migrations/0002.up.sql", "added"),
		changedFile("migrations/0003.up.sql", "removed"),
		changedFile("README.md", "modified"),
	))

	api.handle(contentsPath("migrations/0001.up.sql"), func(w http.ResponseWriter, r *http.Request) {
		content := "-- head\n"
		if r.URL.Query().Get("ref") == "base" {
			content = "-- base\n"
		}
		_ = json.NewEncoder(w).Encode(contentsResponse("migrations/0001.up.sql", content))
	})
	api.json(contentsPath("migrations/0002.up.sql"), contentsResponse("migrations/0002.up.sql", "-- added\n"))
	api.json(contentsPath("migrations/0003.up.sql"), contentsResponse("migrations/0003.up.sql", "-- removed\n"))
	api.json("/repos/acme/widgets/contents/migrations", []any{})

	got, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	byPath := map[string]domain.ChangedFile{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	// README.md is excluded by the predicate, so it must never have been fetched.
	if _, ok := byPath["README.md"]; ok {
		t.Error("a file the engine does not support was fetched")
	}

	modified := byPath["migrations/0001.up.sql"]
	if modified.Status != domain.StatusModified {
		t.Errorf("status = %q, want MODIFIED", modified.Status)
	}
	if string(modified.Previous) != "-- base\n" || string(modified.Current) != "-- head\n" {
		t.Errorf("both sides not fetched: previous=%q current=%q", modified.Previous, modified.Current)
	}

	added := byPath["migrations/0002.up.sql"]
	if added.Status != domain.StatusAdded || added.Previous != nil {
		t.Errorf("added file: status %q, previous %q", added.Status, added.Previous)
	}

	removed := byPath["migrations/0003.up.sql"]
	if removed.Status != domain.StatusRemoved || removed.Current != nil {
		t.Errorf("removed file: status %q, current %q", removed.Status, removed.Current)
	}
	if string(removed.Previous) != "-- removed\n" {
		t.Errorf("a removed file must carry its previous content, got %q", removed.Previous)
	}
}

// CLAUDE.md §16.4: K8S003 needs the StorageClass behind a deleted claim and K8S009 needs the
// workload still mounting a deleted ConfigMap. Neither is in the diff, because neither was
// edited. Without this fetch both rules go blind.
func TestGitHubFetchesUnchangedContextFiles(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.json("/repos/acme/widgets/compare/base...head", comparison(
		changedFile("k8s/pvc.yaml", "removed"),
	))

	api.json(contentsPath("k8s/pvc.yaml"), contentsResponse("k8s/pvc.yaml", "kind: PersistentVolumeClaim\n"))

	// The directory listing at base reveals a sibling nobody touched.
	api.json("/repos/acme/widgets/contents/k8s", []any{
		map[string]any{"type": "file", "name": "pvc.yaml", "path": "k8s/pvc.yaml"},
		map[string]any{"type": "file", "name": "storageclass.yaml", "path": "k8s/storageclass.yaml"},
		map[string]any{"type": "file", "name": "notes.txt", "path": "k8s/notes.txt"},
		map[string]any{"type": "dir", "name": "sub", "path": "k8s/sub"},
	})
	api.json(contentsPath("k8s/storageclass.yaml"),
		contentsResponse("k8s/storageclass.yaml", "kind: StorageClass\nreclaimPolicy: Delete\n"))

	got, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	byPath := map[string]domain.ChangedFile{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	context, ok := byPath["k8s/storageclass.yaml"]
	if !ok {
		t.Fatalf("the unchanged StorageClass was not fetched; got %v", paths(got))
	}

	// Reported as MODIFIED with identical sides, so the analyzers treat it as context and
	// generate no findings for it.
	if context.Status != domain.StatusModified {
		t.Errorf("context file status = %q, want MODIFIED", context.Status)
	}
	if string(context.Previous) != string(context.Current) {
		t.Error("a context file must have identical sides, or it reads as a change")
	}

	// A file the engine does not analyze is not worth an API call.
	if _, ok := byPath["k8s/notes.txt"]; ok {
		t.Error("an unsupported context file was fetched")
	}
}

// THE FAIL-CLOSED NETWORK CONTRACT. Every one of these must surface as an error, because the
// alternative is grading whichever files happened to arrive.
func TestGitHubFailsClosedOnAPIErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeAPI)
		want  string
	}{
		{
			name: "rate limited on compare",
			setup: func(api *fakeAPI) {
				api.handle("/repos/acme/widgets/compare/base...head", func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Limit", "60")
					w.Header().Set("X-RateLimit-Reset", "1700000000")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
				})
			},
			want: "rate limit",
		},
		{
			name: "server error on compare",
			setup: func(api *fakeAPI) {
				api.handle("/repos/acme/widgets/compare/base...head", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
			},
			want: "comparing",
		},
		{
			name: "file fetch fails",
			setup: func(api *fakeAPI) {
				api.json("/repos/acme/widgets/compare/base...head", comparison(changedFile("a.sql", "added")))
				api.handle(contentsPath("a.sql"), func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
			},
			want: "fetching",
		},
		{
			name: "context listing fails",
			setup: func(api *fakeAPI) {
				api.json("/repos/acme/widgets/compare/base...head", comparison(changedFile("k8s/a.yaml", "added")))
				api.json(contentsPath("k8s/a.yaml"), contentsResponse("k8s/a.yaml", "kind: X\n"))
				api.handle("/repos/acme/widgets/contents/k8s", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
			},
			want: "listing directory",
		},
		{
			name: "file is too large to fetch",
			setup: func(api *fakeAPI) {
				api.json("/repos/acme/widgets/compare/base...head", comparison(changedFile("a.sql", "added")))
				api.json(contentsPath("a.sql"), map[string]any{
					"type": "file", "name": "a.sql", "path": "a.sql",
					"size": 50 << 20, "encoding": "none", "content": "",
				})
			},
			want: "over the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(t)
			tt.setup(api)

			got, err := provider.NewGitHub(api.client(t), sqlInclude).
				ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))

			if err == nil {
				t.Fatalf("no error; the provider returned %d files from a failed fetch", len(got))
			}
			if got != nil {
				t.Errorf("files were returned alongside an error: %v", paths(got))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// A directory that does not exist at base is not a failure: the change may have created it.
func TestGitHubToleratesAMissingBaseDirectory(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.json("/repos/acme/widgets/compare/base...head", comparison(changedFile("new/a.yaml", "added")))
	api.json(contentsPath("new/a.yaml"), contentsResponse("new/a.yaml", "kind: X\n"))
	api.handle("/repos/acme/widgets/contents/new", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	got, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))
	if err != nil {
		t.Fatalf("a directory absent at base was treated as a failure: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d files, want 1: %v", len(got), paths(got))
	}
}

// Truncating a huge pull request and grading the part that fit would be a confident answer about
// something the engine only half looked at.
func TestGitHubRefusesAnOversizedChangeset(t *testing.T) {
	t.Parallel()

	files := make([]map[string]any, 0, 600)
	for i := 0; i < 600; i++ {
		files = append(files, changedFile(fmt.Sprintf("m/%04d.sql", i), "added"))
	}

	api := newFakeAPI(t)
	api.json("/repos/acme/widgets/compare/base...head", comparison(files...))

	_, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))

	if err == nil {
		t.Fatal("an oversized changeset was accepted")
	}
	if !strings.Contains(err.Error(), "exceeds the fetch limit") {
		t.Errorf("error %q does not explain the limit", err)
	}
}

func TestGitHubRejectsMalformedRefs(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	p := provider.NewGitHub(api.client(t), sqlInclude)

	for _, ref := range []domain.ChangeRef{
		"", "acme/widgets", "acme/widgets@base", "acme@base...head",
		"acme/widgets@...head", "acme/widgets@base...", "/widgets@base...head",
	} {
		if _, err := p.ChangedFiles(context.Background(), ref); err == nil {
			t.Errorf("ref %q was accepted", ref)
		}
	}
}

func TestGitHubRequiresAClient(t *testing.T) {
	t.Parallel()

	if _, err := provider.NewGitHub(nil, nil).ChangedFiles(context.Background(), "a/b@c...d"); err == nil {
		t.Error("a provider with no client returned no error")
	}
}

func TestGitHubRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	api := newFakeAPI(t)
	if _, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(ctx, provider.Ref("acme", "widgets", "base", "head")); err == nil {
		t.Error("a cancelled context returned no error")
	}
}

// The interface promises sorted output because InputDigest is computed from it.
func TestGitHubOutputIsSorted(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.json("/repos/acme/widgets/compare/base...head", comparison(
		changedFile("z.sql", "added"),
		changedFile("a.sql", "added"),
		changedFile("m.sql", "added"),
	))
	for _, name := range []string{"z.sql", "a.sql", "m.sql"} {
		api.json(contentsPath(name), contentsResponse(name, "SELECT 1;"))
	}
	api.json("/repos/acme/widgets/contents/", []any{})

	got, err := provider.NewGitHub(api.client(t), sqlInclude).
		ChangedFiles(context.Background(), provider.Ref("acme", "widgets", "base", "head"))
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	want := []string{"a.sql", "m.sql", "z.sql"}
	for i, p := range paths(got) {
		if i >= len(want) || p != want[i] {
			t.Fatalf("paths = %v, want %v", paths(got), want)
		}
	}
}
