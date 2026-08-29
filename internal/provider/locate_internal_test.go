// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package provider

import (
	"testing"
)

// These are internal tests because the defect they pin is not reachable from the exported API on
// every platform, and pinning it only where it reproduces would pin it nowhere useful.
//
// `ResolveRoot` and `QualifyPath` are two sides of one comparison, and they disagreed on Linux:
// the root resolver split a path into segments, dropped the empty one a leading `/` produces, and
// rejoined — so `/tmp/x` came back as `tmp/x` while `QualifyPath` returned `/tmp/x`. An absolute
// Windows path opens with a drive letter and has no empty leading segment, so on the development
// machine the two agreed and the whole suite passed. CI is Linux, and `--terraform-plan` stopped
// claiming the file it named.
//
// The exported tests below (TestTheTwoSidesOfTheComparisonAgree) hold the property end to end and
// would have caught this on Linux. These hold the string logic underneath it, with POSIX inputs
// written out literally, so the platform the test runs on cannot hide the answer.

// TestCommonPathKeepsAPathAbsolute is the specific regression.
//
// The empty segment a leading `/` produces is what makes a POSIX path absolute. Dropping it turns
// a common prefix into a relative path naming a directory that does not exist.
func TestCommonPathKeepsAPathAbsolute(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a, b string
		want string
	}{
		{"two absolute POSIX siblings", "/tmp/build/a", "/tmp/build/b", "/tmp/build"},
		{"an absolute root and itself", "/tmp/build", "/tmp/build", "/tmp/build"},
		{"an absolute root and its parent", "/tmp/build/a", "/tmp/build", "/tmp/build"},
		{"absolute paths sharing only the root", "/tmp/a", "/var/b", ""},
		{"two relative siblings", "db/migrate/a", "db/migrate/b", "db/migrate"},
		{"relative paths sharing nothing", "db/migrate", "src/app", ""},
		{"one empty", "", "db/migrate", ""},
		{"both empty", "", "", ""},
		{"a Windows drive path", "C:/work/a", "C:/work/b", "C:/work"},
		{"different drives", "C:/work", "D:/work", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := commonPath(tc.a, tc.b); got != tc.want {
				t.Errorf("commonPath(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestCleanNamespacedMapsHereToTheEmptyPrefix.
//
// filepath.Rel returns "." when a path is the project root, and "." as a prefix would prepend a
// segment to every path in the changeset. The empty prefix is the identity, which is what "the
// analysis root is the project root" means.
func TestCleanNamespacedMapsHereToTheEmptyPrefix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{".", ""},
		{"", ""},
		{"db/migrate", "db/migrate"},
		{"./db/migrate", "db/migrate"},
		{"/tmp/build", "/tmp/build"},
		{"/tmp/./build", "/tmp/build"},
		{"C:/work", "C:/work"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := cleanNamespaced(tc.in); got != tc.want {
				t.Errorf("cleanNamespaced(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
