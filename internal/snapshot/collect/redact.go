// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package collect

import (
	"fmt"
	"regexp"
	"strings"
)

// Patterns for the two shapes a PostgreSQL password arrives in.
//
// A driver error frequently quotes the connection string it failed on, and this command is run
// in CI, where an error message is written to a log that many people can read. A leaked
// production password is a far worse outcome than an unhelpful error, so the message is scrubbed
// even at the cost of some diagnostic detail.
//
// Regex is used here and only here. docs/SPECIFICATION.md forbids regex for SQL parsing, which
// this is not: it is redaction of a text string, where a conservative over-match costs nothing
// and a principled parser would still have to guess at whatever the driver chose to print.
var (
	// A URL with credentials: postgres://user:secret@host/db
	urlCredentials = regexp.MustCompile(`(?i)\b(postgres(?:ql)?://)[^\s/@]*@`)

	// A keyword/value connection string: password=secret or password='secret'
	keywordPassword = regexp.MustCompile(`(?i)\bpassword\s*=\s*('[^']*'|"[^"]*"|\S+)`)
)

// redactDSN removes credentials from an error before it is shown or logged.
//
// It returns an error rather than a string so callers cannot forget to wrap: every path out of
// the collector that carries a driver message goes through this.
func redactDSN(err error) error {
	if err == nil {
		return nil
	}

	cleaned := Redact(err.Error())
	if cleaned == err.Error() {
		return err
	}
	return fmt.Errorf("%s", cleaned)
}

// Redact scrubs credentials from arbitrary text. Exported so the redaction test can assert
// against it directly rather than by inference.
func Redact(s string) string {
	s = urlCredentials.ReplaceAllString(s, "${1}REDACTED@")
	s = keywordPassword.ReplaceAllString(s, "password=REDACTED")
	return s
}

// looksLikeCredential reports whether a string plausibly carries a secret.
//
// It exists for the redaction test, which asserts that nothing in a produced snapshot trips it.
// Deliberately over-eager: a false positive costs a test author a moment, and a false negative
// costs somebody their database.
func looksLikeCredential(s string) bool {
	lower := strings.ToLower(s)
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "credential"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(lower, "://") && strings.Contains(lower, "@")
}
