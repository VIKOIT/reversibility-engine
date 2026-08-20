package github_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

// FuzzWebhookRejection is the security fuzz target.
//
// It feeds arbitrary signature headers and bodies at the handler and asserts the one property
// that matters: nothing an attacker controls can reach the processor unless it carries a
// signature computed with the real secret. This is the boundary between the open internet and
// an engine that comments on private repositories.
func FuzzWebhookRejection(f *testing.F) {
	seeds := []struct{ signature, event, body string }{
		{"", "pull_request", `{"action":"opened"}`},
		{"sha256=", "pull_request", `{}`},
		{"sha256=00", "pull_request", `{}`},
		{"sha1=abcd", "pull_request", `{}`},
		{"SHA256=abcd", "pull_request", `{}`},
		{"sha256=" + strings.Repeat("a", 64), "pull_request", `{}`},
		{"sha256=" + strings.Repeat("f", 1000), "ping", ``},
		{"garbage", "push", `not json`},
		{"sha256=\x00\xff", "pull_request", "\x00\xff"},
		{strings.Repeat("sha256=", 100), "pull_request", `{}`},
	}

	for _, seed := range seeds {
		f.Add(seed.signature, seed.event, seed.body)
	}

	f.Fuzz(func(t *testing.T, signature, event, body string) {
		processor := &recordingProcessor{}
		handler := gh.NewHandler(testSecret, processor, gh.WithSynchronousProcessing())

		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		if signature != "" {
			// A header value containing a newline would be rejected by the transport before it
			// ever reached a real server, so it is not a case worth asserting on.
			if strings.ContainsAny(signature, "\r\n\x00") {
				return
			}
			req.Header.Set("X-Hub-Signature-256", signature)
		}

		rec := httptest.NewRecorder()

		// A panic here would be a denial of service reachable by an unauthenticated caller.
		handler.ServeHTTP(rec, req)

		// The only signature that may be accepted is the correct one.
		if signature == gh.Sign([]byte(body), testSecret) {
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Errorf("a correctly signed delivery was rejected with %d", rec.Code)
			}
			return
		}

		if len(processor.jobs) != 0 {
			t.Fatalf("an unauthenticated delivery reached the processor: signature=%q", signature)
		}
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("an unauthenticated delivery answered %d, want 401 or 403", rec.Code)
		}
	})
}

// FuzzAuthenticPayload covers what happens after the signature verifies: the payload is trusted
// to be from GitHub, but not to be well formed. A field GitHub stops sending, or sends empty,
// must not crash the server or dispatch a job against the wrong commits.
func FuzzAuthenticPayload(f *testing.F) {
	f.Add(`{"action":"opened"}`)
	f.Add(`{"action":"opened","pull_request":{"number":1,"base":{"sha":"a"},"head":{"sha":"b"}}}`)
	f.Add(`{"action":"opened","repository":{"name":"r","owner":{"login":"o"}}}`)
	f.Add(`{"action":null}`)
	f.Add(`[]`)
	f.Add(`{"action":"opened","number":-1}`)
	f.Add(`{"pull_request":{"number":9999999999999999}}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, body string) {
		processor := &recordingProcessor{}
		handler := gh.NewHandler(testSecret, processor, gh.WithSynchronousProcessing())

		rec := deliver(t, handler, "pull_request", gh.Sign([]byte(body), testSecret), body)

		if rec.Code >= 500 {
			t.Errorf("an authentic payload produced a %d; a malformed body is the caller's problem, not a server fault", rec.Code)
		}

		// Any job that was dispatched must be complete enough to analyze. Comparing the wrong
		// commits would certify a change nobody made.
		for _, job := range processor.jobs {
			if job.Base == "" || job.Head == "" {
				t.Errorf("dispatched a job with an empty commit: base=%q head=%q", job.Base, job.Head)
			}
			if job.Number() == 0 {
				t.Error("dispatched a job with no pull request number")
			}
			if job.Repository() == "/" {
				t.Error("dispatched a job with no repository")
			}
		}
	})
}
