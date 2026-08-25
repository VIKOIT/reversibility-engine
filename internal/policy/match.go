// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy

import (
	"fmt"
	"path"
	"strings"
)

// Match reports whether a slash-separated path matches a glob pattern.
//
// The syntax is the one people already expect from .gitignore-style tooling:
//
//	**   matches zero or more whole path segments
//	*    matches any run of characters within one segment
//	?    matches one character within one segment
//	[..] matches a character class, exactly as path.Match defines it
//
// Segment matching delegates to path.Match rather than reimplementing it. This is deliberately
// NOT git pathspec syntax and does not claim to be: pathspecs are what the git provider hands
// to git, and these are what the policy file matches. Two syntaxes with one name would be worse
// than two names.
//
// A pattern that reaches here is assumed to have passed ValidatePattern, which is what makes
// ignoring path.Match's error safe: a malformed pattern is rejected when the policy is loaded,
// so it can never silently match nothing at the moment somebody is relying on it.
func Match(pattern, name string) bool {
	return matchSegments(segments(pattern), segments(name))
}

// ValidatePattern reports whether a pattern is well formed.
//
// It is called when the policy is parsed, not when it is applied. A pattern with an unclosed
// bracket that silently matched nothing would present as a waiver that never fires or an ignore
// that never ignores — the kind of failure nobody notices until it matters.
func ValidatePattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("empty pattern")
	}

	for _, seg := range segments(pattern) {
		if seg == "**" {
			continue
		}
		// path.Match reports a malformed pattern regardless of what it is matched against.
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("segment %q: %w", seg, err)
		}
	}

	return nil
}

// segments splits a slash-separated string, dropping empty segments so that "a//b" and "/a/b"
// behave as "a/b", and collapsing runs of "**" so that "**/**" cannot make matching exponential.
func segments(s string) []string {
	parts := strings.Split(s, "/")

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, p)
	}

	return out
}

// matchSegments matches pattern segments against path segments, left to right.
func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Zero or more segments: try every split point. Consecutive ** were collapsed by
			// segments(), so this branch cannot recurse into itself without consuming input.
			for i := 0; i <= len(name); i++ {
				if matchSegments(pattern[1:], name[i:]) {
					return true
				}
			}
			return false
		}

		if len(name) == 0 {
			return false
		}

		ok, err := path.Match(pattern[0], name[0])
		if err != nil || !ok {
			return false
		}

		pattern, name = pattern[1:], name[1:]
	}

	return len(name) == 0
}
