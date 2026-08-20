// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package github_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/VIKOIT/reversibility-engine/internal/delivery/github"
)

var testSecret = []byte("a-webhook-secret")

func sign(body string) string { return gh.Sign([]byte(body), testSecret) }

// deliver posts a body with an arbitrary signature header and returns the response.
func deliver(t *testing.T, handler http.Handler, event, signature, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery")
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// recordingProcessor captures the jobs a handler dispatches.
type recordingProcessor struct {
	jobs []gh.Job
	err  error
}

func (p *recordingProcessor) Process(_ context.Context, job gh.Job) error {
	p.jobs = append(p.jobs, job)
	return p.err
}

func newHandler(t *testing.T, processor gh.Processor) http.Handler {
	t.Helper()
	return gh.NewHandler(testSecret, processor, gh.WithSynchronousProcessing())
}

// THE SECURITY BOUNDARY. Every one of these must be rejected, and rejected before the payload is
// parsed or acted on — an unauthenticated request is a stranger's bytes.
func TestUnauthenticatedDeliveriesAreRejected(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	valid := sign(body)

	// A signature computed with the wrong secret: the exact forgery the HMAC exists to stop.
	wrongKey := hmac.New(sha256.New, []byte("not-the-secret"))
	wrongKey.Write([]byte(body))
	forged := "sha256=" + hex.EncodeToString(wrongKey.Sum(nil))

	tests := []struct {
		name      string
		signature string
		body      string
		want      int
	}{
		{"no signature at all", "", body, http.StatusUnauthorized},
		{"empty signature", "", body, http.StatusUnauthorized},
		{"signature from the wrong secret", forged, body, http.StatusForbidden},
		{"signature for a different body", valid, `{"action":"closed"}`, http.StatusForbidden},
		{"truncated digest", valid[:len(valid)-4], body, http.StatusForbidden},
		{"digest with no prefix", strings.TrimPrefix(valid, "sha256="), body, http.StatusForbidden},
		{"wrong algorithm prefix", "sha1=" + strings.TrimPrefix(valid, "sha256="), body, http.StatusForbidden},
		{"not hexadecimal", "sha256=zzzz", body, http.StatusForbidden},
		{"empty digest", "sha256=", body, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			processor := &recordingProcessor{}
			rec := deliver(t, newHandler(t, processor), "pull_request", tt.signature, tt.body)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if len(processor.jobs) != 0 {
				t.Errorf("an unauthenticated delivery reached the processor: %+v", processor.jobs)
			}
		})
	}
}

// GitHub also sends the legacy SHA-1 header. Honouring it would let an attacker downgrade the
// check by simply omitting the stronger one.
func TestSHA1SignatureIsNotAccepted(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	// A valid SHA-1 signature, and deliberately no SHA-256 header.
	req.Header.Set("X-Hub-Signature", "sha1=irrelevant")

	processor := &recordingProcessor{}
	rec := httptest.NewRecorder()
	newHandler(t, processor).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d — only SHA-256 is honoured", rec.Code, http.StatusUnauthorized)
	}
	if len(processor.jobs) != 0 {
		t.Error("a SHA-1 signed delivery reached the processor")
	}
}

// A server with no secret cannot authenticate anything, so it must reject everything rather than
// silently accepting every caller.
func TestHandlerWithNoSecretRejectsEverything(t *testing.T) {
	t.Parallel()

	processor := &recordingProcessor{}
	handler := gh.NewHandler(nil, processor, gh.WithSynchronousProcessing())

	rec := deliver(t, handler, "pull_request", sign(`{"action":"opened"}`), `{"action":"opened"}`)

	if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
		t.Errorf("status = %d; a server with no secret must not accept deliveries", rec.Code)
	}
	if len(processor.jobs) != 0 {
		t.Error("a delivery was processed by a server with no secret")
	}
}

// The rejection body must not describe what was wrong with the signature. Each distinction is a
// hint toward a working forgery.
func TestRejectionRevealsNothing(t *testing.T) {
	t.Parallel()

	const body = `{"action":"opened"}`

	missing := deliver(t, newHandler(t, &recordingProcessor{}), "pull_request", "", body)
	invalid := deliver(t, newHandler(t, &recordingProcessor{}), "pull_request", "sha256=abcd", body)

	if missing.Body.String() != invalid.Body.String() {
		t.Errorf("rejection bodies differ and leak the reason:\n  missing: %q\n  invalid: %q",
			missing.Body.String(), invalid.Body.String())
	}
	for _, word := range []string{"hmac", "secret", "expected", "computed"} {
		if strings.Contains(strings.ToLower(invalid.Body.String()), word) {
			t.Errorf("rejection body leaks %q: %q", word, invalid.Body.String())
		}
	}
}

// Sign must agree with the verifier, or every test above would be checking nothing.
func TestSignRoundTrips(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "{}", strings.Repeat("x", 100000), "unicode ✅ payload"} {
		processor := &recordingProcessor{}
		rec := deliver(t, newHandler(t, processor), "ping", gh.Sign([]byte(body), testSecret), body)

		if rec.Code != http.StatusOK {
			t.Errorf("a correctly signed ping was rejected with %d", rec.Code)
		}
	}
}

func TestNonPostIsRejected(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/webhook", nil)
		rec := httptest.NewRecorder()

		newHandler(t, &recordingProcessor{}).ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}
