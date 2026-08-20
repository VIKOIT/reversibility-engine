// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SignatureHeader is the only signature header this server honours.
//
// GitHub also sends X-Hub-Signature, which is HMAC-SHA1. It is deliberately ignored: accepting
// it would let an attacker who can forge SHA-1 downgrade the check simply by omitting the
// stronger header. There is no negotiation here — SHA-256 or the request is dropped.
const SignatureHeader = "X-Hub-Signature-256"

// signaturePrefix is the algorithm marker GitHub prepends to the hex digest.
const signaturePrefix = "sha256="

// maxPayloadBytes caps how much of a request body will be read.
//
// The body is authenticated only after it has been read in full, so an unbounded read is an
// unauthenticated attacker's memory-exhaustion primitive. GitHub's own delivery limit is 25 MiB.
const maxPayloadBytes = 25 << 20

// Signature verification failures. They are distinguished for logging only — every one of them
// produces the same response, because telling a caller *why* their forgery failed helps them
// forge a better one.
var (
	ErrMissingSignature = errors.New("webhook: missing " + SignatureHeader)
	ErrInvalidSignature = errors.New("webhook: signature does not match")
	ErrMalformedBody    = errors.New("webhook: could not read request body")
)

// verifyPayload reads and authenticates a webhook body.
//
// It returns the body only when the signature is valid. Nothing downstream ever sees the bytes
// of an unauthenticated request — not to parse an event type, not to log a repository name.
func verifyPayload(r *http.Request, secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		// A server with no secret cannot authenticate anything, so it must not pretend to.
		// This is a misconfiguration, and it fails closed.
		return nil, fmt.Errorf("webhook: no signing secret configured: %w", ErrInvalidSignature)
	}

	signature := r.Header.Get(SignatureHeader)
	if signature == "" {
		return nil, ErrMissingSignature
	}

	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxPayloadBytes))
	if err != nil {
		// Both are wrapped: callers match on ErrMalformedBody, and the underlying cause
		// distinguishes a client that hung up from one that exceeded the size cap.
		return nil, fmt.Errorf("%w: %w", ErrMalformedBody, err)
	}

	if !validSignature(body, signature, secret) {
		return nil, ErrInvalidSignature
	}

	return body, nil
}

// validSignature reports whether the hex digest matches an HMAC-SHA256 of the body.
func validSignature(body []byte, signature string, secret []byte) bool {
	// The prefix is compared case-insensitively but must be present: a bare hex digest is not a
	// signature this server recognises.
	if !strings.HasPrefix(strings.ToLower(signature), signaturePrefix) {
		return false
	}

	provided, err := hex.DecodeString(signature[len(signaturePrefix):])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expected := mac.Sum(nil)

	// hmac.Equal is constant time. A byte-by-byte comparison would leak how much of a forged
	// signature was correct, which is enough to recover the rest one byte at a time.
	return hmac.Equal(provided, expected)
}

// Sign produces the header value GitHub would send for a payload.
//
// It exists so tests can exercise the real verification path with real signatures rather than
// stubbing it out — the one part of this server that must never be bypassed is also the one that
// must be exercised most.
func Sign(body, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
