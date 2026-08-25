// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package policy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// FileName is the policy file the discovery walk looks for.
const FileName = ".reversibility.yml"

// Version is the only policy schema version this build understands.
//
// An unrecognised version is refused rather than read on a best-effort basis. A newer file
// almost certainly means something this build cannot honour, and honouring the half of it that
// happens to parse is how a waiver nobody wrote comes into effect.
const Version = 1

// MaxWaiverWindow is the furthest ahead a waiver may expire, measured from the day the policy
// is parsed.
//
// A waiver is a promise to come back to something. Six months is already generous for a promise
// nobody is tracking, and a waiver that outlives the person who wrote it is a deleted rule with
// extra paperwork.
const MaxWaiverWindow = 180 * 24 * time.Hour

// dateLayout is the only accepted spelling of an expiry date: a calendar day, no time, no zone.
//
// A duration was deliberately not accepted. "90d" is relative to a moment nobody records, so it
// renews itself every time the file is read and never expires at all.
const dateLayout = "2006-01-02"

// file is the on-disk shape. It is separate from Policy so that decoding cannot populate the
// resolved fields, and so unknown keys can be rejected against exactly the documented schema.
type file struct {
	Version   int        `json:"version"`
	Gate      string     `json:"gate"`
	Ignore    []string   `json:"ignore"`
	Waivers   []Waiver   `json:"waivers"`
	Overrides []Override `json:"overrides"`
}

// Discover walks up from a starting directory looking for a policy file.
//
// It returns "" when there is none, which is not an error: the tool must work exactly as it did
// before policies existed. The walk stops at the directory holding .git, and at the filesystem
// root. Without that boundary a repository with no policy would inherit whichever file happened
// to sit in a parent directory — someone's home directory, or /tmp during a test.
func Discover(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", start, err)
	}

	// A file was given rather than a directory: start from the directory holding it.
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		candidate := filepath.Join(dir, FileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Load reads and resolves a policy file.
//
// today is injected rather than read from the clock so that the expiry window is testable and
// so a caller can reproduce a past run. Every failure is an error: a run that could not resolve
// its own configuration does not know what it was meant to enforce, and continuing without it
// would enforce something nobody asked for.
func Load(path string, today time.Time) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", domain.ErrInvalidPolicy, path, err)
	}

	return Parse(raw, path, today)
}

// Parse resolves a policy from bytes. path is used only in messages.
func Parse(raw []byte, path string, today time.Time) (*Policy, error) {
	var f file

	// Strict: an unknown key is refused rather than ignored. A misspelled "expiress" that
	// decoded to nothing would leave a waiver with no expiry at all, which is the one shape of
	// this file that must not exist.
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", domain.ErrInvalidPolicy, path, err)
	}

	if f.Version != Version {
		return nil, fmt.Errorf("%w: %s: version %d is not supported; this build understands version %d",
			domain.ErrInvalidPolicy, path, f.Version, Version)
	}

	p := &Policy{Source: path}

	if f.Gate != "" {
		gate := domain.Grade(strings.ToUpper(strings.TrimSpace(f.Gate)))
		if !gate.Valid() {
			return nil, fmt.Errorf("%w: %s: gate %q is not a grade; want A, B, C, or F",
				domain.ErrInvalidPolicy, path, f.Gate)
		}
		p.Gate = gate
	}

	for i, pattern := range f.Ignore {
		if err := ValidatePattern(pattern); err != nil {
			return nil, fmt.Errorf("%w: %s: ignore[%d] %q: %w", domain.ErrInvalidPolicy, path, i, pattern, err)
		}
		p.Ignore = append(p.Ignore, pattern)
	}

	for i, w := range f.Waivers {
		resolved, err := validateWaiver(w, today)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: waivers[%d]: %w", domain.ErrInvalidPolicy, path, i, err)
		}
		p.Waivers = append(p.Waivers, resolved)
	}

	for i, o := range f.Overrides {
		resolved, err := validateOverride(o)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: overrides[%d]: %w", domain.ErrInvalidPolicy, path, i, err)
		}
		p.Overrides = append(p.Overrides, resolved)
	}

	p.Digest = digest(p)
	return p, nil
}

func validateWaiver(w Waiver, today time.Time) (Waiver, error) {
	w.Rule = strings.TrimSpace(w.Rule)
	w.Path = strings.TrimSpace(w.Path)
	w.Reason = strings.TrimSpace(w.Reason)
	w.Expires = strings.TrimSpace(w.Expires)
	w.ApprovedBy = strings.TrimSpace(w.ApprovedBy)

	if w.Rule == "" {
		return Waiver{}, fmt.Errorf("rule is required; a waiver that names no rule would cover everything")
	}

	// Required, and an error rather than a warning. A warning in a CI log is not read, and the
	// waiver would take effect anyway — which is exactly the unexplained suppression this is
	// meant to prevent.
	if w.Reason == "" {
		return Waiver{}, fmt.Errorf("reason is required for the waiver on %s", w.Rule)
	}
	if w.Expires == "" {
		return Waiver{}, fmt.Errorf("expires is required for the waiver on %s, as a date such as 2026-10-01", w.Rule)
	}

	expires, err := time.Parse(dateLayout, w.Expires)
	if err != nil {
		return Waiver{}, fmt.Errorf("expires %q on %s is not a date of the form YYYY-MM-DD", w.Expires, w.Rule)
	}

	if limit := truncateToDay(today).Add(MaxWaiverWindow); expires.After(limit) {
		return Waiver{}, fmt.Errorf("expires %s on %s is more than %d days away; the limit is %s",
			w.Expires, w.Rule, int(MaxWaiverWindow.Hours()/24), limit.Format(dateLayout))
	}

	if w.Path != "" {
		if err := ValidatePattern(w.Path); err != nil {
			return Waiver{}, fmt.Errorf("path %q on %s: %w", w.Path, w.Rule, err)
		}
	}

	return w, nil
}

func validateOverride(o Override) (Override, error) {
	o.Rule = strings.TrimSpace(o.Rule)
	o.Severity = domain.Reversibility(strings.ToUpper(strings.TrimSpace(string(o.Severity))))

	if o.Rule == "" {
		return Override{}, fmt.Errorf("rule is required")
	}
	if !o.Severity.Valid() {
		return Override{}, fmt.Errorf("severity %q on %s is not a classification; want REVERSIBLE, COSTLY, IRREVERSIBLE, or UNKNOWN",
			o.Severity, o.Rule)
	}

	// REVERSIBLE is the mildest verdict there is, so an override to it can only ever weaken a
	// finding or do nothing. Refusing it here catches the mistake in the file rather than on
	// the one pull request where the rule finally fires.
	if o.Severity == domain.ReversibilityReversible {
		return Override{}, fmt.Errorf("override for %s sets REVERSIBLE, which can only weaken a finding; an override may only make a rule stricter",
			o.Rule)
	}

	return o, nil
}

// digest hashes the resolved policy.
//
// It covers the resolved values rather than the file's bytes, so reformatting the YAML or
// editing a comment does not change a certificate. It deliberately does not cover which waivers
// are currently live: that varies with the date, and a digest that changed on its own overnight
// would stop being evidence about the configuration.
//
// Fields are length-prefixed for the same reason InputDigest length-prefixes: concatenating raw
// values lets two different policies collide, and a digest that can collide is not evidence.
func digest(p *Policy) string {
	h := sha256.New()

	writeField(h, []byte(fmt.Sprintf("v%d", Version)))
	writeField(h, []byte(p.Gate))

	// Declaration order is part of the policy: the first matching waiver wins.
	for _, pattern := range p.Ignore {
		writeField(h, []byte(pattern))
	}
	for _, w := range p.Waivers {
		writeField(h, []byte(w.Rule))
		writeField(h, []byte(w.Path))
		writeField(h, []byte(w.Reason))
		writeField(h, []byte(w.Expires))
		writeField(h, []byte(w.ApprovedBy))
	}
	for _, o := range p.Overrides {
		writeField(h, []byte(o.Rule))
		writeField(h, []byte(o.Severity))
	}

	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))

	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}
