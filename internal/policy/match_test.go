// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy_test

import (
	"testing"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/policy"
)

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// The two patterns the documented example uses. If these ever stop working, every
		// policy file written from the README stops working with them.
		{"legacy/**", "legacy/0001.sql", true},
		{"legacy/**", "legacy/deep/nested/0001.sql", true},
		{"legacy/**", "legacy", true},
		{"legacy/**", "current/0001.sql", false},
		{"legacy/**", "not-legacy/0001.sql", false},
		{"**/*.generated.sql", "db/schema.generated.sql", true},
		{"**/*.generated.sql", "a/b/c/schema.generated.sql", true},
		{"**/*.generated.sql", "schema.generated.sql", true},
		{"**/*.generated.sql", "schema.sql", false},

		// A single star stays inside one segment, which is what stops "migrations/*.sql" from
		// quietly covering a subdirectory nobody reviewed.
		{"migrations/*.sql", "migrations/0031_backfill.sql", true},
		{"migrations/*.sql", "migrations/nested/0031.sql", false},
		{"migrations/0031_*.sql", "migrations/0031_backfill.sql", true},
		{"migrations/0031_*.sql", "migrations/0032_other.sql", false},

		{"?.sql", "a.sql", true},
		{"?.sql", "ab.sql", false},
		{"db/[0-9]*.sql", "db/1_init.sql", true},
		{"db/[0-9]*.sql", "db/init.sql", false},

		{"**", "anything/at/all.sql", true},
		{"**", "top.sql", true},

		// Collapsing runs of ** must not change what matches.
		{"**/**/x.sql", "a/b/x.sql", true},
		{"**/**/x.sql", "x.sql", true},

		{"a/**/b.sql", "a/b.sql", true},
		{"a/**/b.sql", "a/x/y/b.sql", true},
		{"a/**/b.sql", "a/x/y/c.sql", false},

		// Leading and doubled separators are normalised on both sides, so a pattern copied out
		// of a shell does not silently match nothing.
		{"/legacy/**", "legacy/a.sql", true},
		{"legacy//**", "legacy/a.sql", true},
		{"./legacy/**", "legacy/a.sql", true},

		{"exact.sql", "exact.sql", true},
		{"exact.sql", "dir/exact.sql", false},
	}

	for _, tc := range tests {
		t.Run(tc.pattern+" vs "+tc.path, func(t *testing.T) {
			t.Parallel()

			if got := policy.Match(tc.pattern, domain.Located(tc.path)); got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestValidatePattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		wantErr bool
	}{
		{"legacy/**", false},
		{"**/*.generated.sql", false},
		{"migrations/0031_*.sql", false},
		{"db/[0-9]*.sql", false},

		{"", true},
		{"   ", true},
		// An unclosed class would otherwise match nothing at all, presenting as a waiver that
		// never fires — a failure nobody notices until the day it matters.
		{"db/[0-9.sql", true},
		{"[", true},
	}

	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()

			err := policy.ValidatePattern(tc.pattern)
			if tc.wantErr && err == nil {
				t.Errorf("ValidatePattern(%q) = nil, want an error", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidatePattern(%q) = %v, want nil", tc.pattern, err)
			}
		})
	}
}
