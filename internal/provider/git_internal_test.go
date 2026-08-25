// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// The parser is tested from inside the package because it is the one part of the git provider
// with no observable behaviour of its own: a status letter it silently dropped would remove a
// file from the changeset, and the certificate would look identical.
func TestParseNameStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		out     string
		want    []gitChange
		wantErr bool
	}{
		{
			name: "empty diff",
			out:  "",
			want: nil,
		},
		{
			name: "one added file",
			out:  "A\x00migrations/0001.sql\x00",
			want: []gitChange{{status: 'A', src: "migrations/0001.sql", dst: "migrations/0001.sql"}},
		},
		{
			name: "every single-path status",
			out:  "M\x00a.sql\x00D\x00b.sql\x00T\x00c.sql\x00",
			want: []gitChange{
				{status: 'M', src: "a.sql", dst: "a.sql"},
				{status: 'D', src: "b.sql", dst: "b.sql"},
				{status: 'T', src: "c.sql", dst: "c.sql"},
			},
		},
		{
			name: "rename and copy carry both paths",
			out:  "R100\x00old.yaml\x00new.yaml\x00C075\x00src.yaml\x00copy.yaml\x00",
			want: []gitChange{
				{status: 'R', src: "old.yaml", dst: "new.yaml"},
				{status: 'C', src: "src.yaml", dst: "copy.yaml"},
			},
		},
		{
			// -z exists precisely so these arrive intact rather than quoted and escaped.
			name: "paths containing a quote and a newline survive",
			out:  "A\x00migrations/we\"ird\nname.sql\x00",
			want: []gitChange{{status: 'A', src: "migrations/we\"ird\nname.sql", dst: "migrations/we\"ird\nname.sql"}},
		},
		{
			// U is an unmerged entry and X is git reporting its own bug. Neither can be
			// classified, and inventing a side for one would put a half-populated file in front
			// of an analyzer.
			name:    "an unmerged entry is refused",
			out:     "U\x00conflicted.sql\x00",
			wantErr: true,
		},
		{
			name:    "an unknown status is refused",
			out:     "X\x00mystery.sql\x00",
			wantErr: true,
		},
		{
			name:    "a status with no path is refused",
			out:     "A\x00",
			wantErr: true,
		},
		{
			name:    "a rename missing its destination is refused",
			out:     "R100\x00old.yaml\x00",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseNameStatus([]byte(tc.out))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseNameStatus(%q) = %v, want an error", tc.out, got)
				}
				if !errors.Is(err, domain.ErrProviderFailed) {
					t.Errorf("error = %v, want one wrapping ErrProviderFailed", err)
				}
				if got != nil {
					t.Errorf("a failed parse returned %d changes; a partial diff must never be used", len(got))
				}
				return
			}

			if err != nil {
				t.Fatalf("parseNameStatus(%q): %v", tc.out, err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(gitChange{})); diff != "" {
				t.Errorf("changes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    int
		wantErr bool
		overCap bool
	}{
		{in: "0", want: 0},
		{in: "1024", want: 1024},
		{in: "", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "12x", wantErr: true},
		// A blob larger than anything that will be accepted stops early rather than overflowing
		// on the way to a number nobody needs.
		{in: "99999999999999999999", overCap: true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := parseSize(tc.in)

			switch {
			case tc.wantErr:
				if err == nil {
					t.Fatalf("parseSize(%q) = %d, want an error", tc.in, got)
				}
			case tc.overCap:
				if err != nil {
					t.Fatalf("parseSize(%q): %v", tc.in, err)
				}
				if got <= maxFileBytes {
					t.Errorf("parseSize(%q) = %d, want a value over the %d limit", tc.in, got, maxFileBytes)
				}
			default:
				if err != nil {
					t.Fatalf("parseSize(%q): %v", tc.in, err)
				}
				if got != tc.want {
					t.Errorf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestShortAbbreviatesSHAs(t *testing.T) {
	t.Parallel()

	full := "0123456789abcdef0123456789abcdef01234567"
	if got := short(full); len(got) != 12 || !strings.HasPrefix(full, got) {
		t.Errorf("short(%q) = %q, want the first 12 characters", full, got)
	}
	if got := short("main"); got != "main" {
		t.Errorf("short(%q) = %q, want it unchanged", "main", got)
	}
}
