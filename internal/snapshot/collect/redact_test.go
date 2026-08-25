// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package collect

import (
	"errors"
	"strings"
	"testing"
)

// A driver error commonly quotes the connection string it failed on, and this command runs in CI
// where errors land in logs many people can read. Redaction runs even at the cost of diagnostic
// detail, because a leaked production password is a far worse outcome than an unhelpful error.
func TestRedact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        string
		mustNotBe string
		mustSay   string
	}{
		{
			name:      "url credentials",
			in:        `failed to connect to postgres://admin:hunter2@db.internal:5432/shop`,
			mustNotBe: "hunter2",
			mustSay:   "REDACTED@",
		},
		{
			name:      "postgresql scheme",
			in:        `dial error: postgresql://svc:s3cr3t@10.0.0.4/prod`,
			mustNotBe: "s3cr3t",
			mustSay:   "REDACTED@",
		},
		{
			name:      "keyword password",
			in:        `bad config: host=db user=admin password=hunter2 dbname=shop`,
			mustNotBe: "hunter2",
			mustSay:   "password=REDACTED",
		},
		{
			name:      "quoted keyword password",
			in:        `host=db password='hunter 2 with spaces' dbname=shop`,
			mustNotBe: "hunter 2 with spaces",
			mustSay:   "password=REDACTED",
		},
		{
			name:    "text with no credential is untouched",
			in:      `relation "orders" does not exist`,
			mustSay: `relation "orders" does not exist`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Redact(tc.in)

			if tc.mustNotBe != "" && strings.Contains(got, tc.mustNotBe) {
				t.Errorf("Redact(%q) = %q, which still contains %q", tc.in, got, tc.mustNotBe)
			}
			if !strings.Contains(got, tc.mustSay) {
				t.Errorf("Redact(%q) = %q, want it to contain %q", tc.in, got, tc.mustSay)
			}
		})
	}
}

func TestRedactDSNWrapsErrors(t *testing.T) {
	t.Parallel()

	if redactDSN(nil) != nil {
		t.Error("redactDSN(nil) is not nil")
	}

	// Identity, not equality: an untouched error must be returned as-is so callers keep any
	// sentinel wrapping it carries.
	clean := errors.New("relation does not exist")
	if got := redactDSN(clean); !errors.Is(got, clean) {
		t.Errorf("an error with no credential was rewritten: %v", got)
	}

	dirty := errors.New("connect: postgres://u:p@h/d failed")
	got := redactDSN(dirty)
	if strings.Contains(got.Error(), "u:p@") {
		t.Errorf("redactDSN left credentials in %q", got)
	}
}

// The marker list the redaction test relies on has to actually fire, or that test would pass by
// never noticing anything.
func TestLooksLikeCredential(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"user_password", "API_SECRET", "my-token", "apikey", "postgres://u:p@h/d",
	} {
		if !looksLikeCredential(s) {
			t.Errorf("looksLikeCredential(%q) = false, want true", s)
		}
	}

	for _, s := range []string{"orders", "idx_orders_status", "public", "shipped_at"} {
		if looksLikeCredential(s) {
			t.Errorf("looksLikeCredential(%q) = true, want false", s)
		}
	}
}
