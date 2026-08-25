// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/internal/snapshot"
)

// A missing snapshot is the ordinary state of most repositories, and of every repository before
// the first collection. The tool has to work exactly as it did without one.
func TestAMissingSnapshotIsNotAnError(t *testing.T) {
	t.Parallel()

	set, err := snapshot.Load([]string{filepath.Join(t.TempDir(), "absent.json")}, snapshot.Options{Now: snapshotDay})
	if err != nil {
		t.Fatalf("a missing snapshot was an error: %v", err)
	}
	if set != nil {
		t.Errorf("a missing snapshot produced context: %+v", set)
	}

	// The same for no paths at all.
	if set, err := snapshot.Load(nil, snapshot.Options{Now: snapshotDay}); err != nil || set != nil {
		t.Errorf("Load(nil) = %+v, %v; want no context and no error", set, err)
	}
}

func TestLoadMergesByKind(t *testing.T) {
	t.Parallel()

	set := loadSet(t, "pg.json", "k8s.json")

	if set.Postgres == nil || len(set.Postgres.Tables) != 4 {
		t.Errorf("postgres data did not merge: %+v", set.Postgres)
	}
	if set.Kubernetes == nil || len(set.Kubernetes.Claims) != 3 {
		t.Errorf("kubernetes data did not merge: %+v", set.Kubernetes)
	}
	if set.Digest == "" {
		t.Error("a loaded context has no digest")
	}
}

// Two snapshots of the same kind from different sources would answer questions about a table
// that exists in one of them, with no way to tell which.
func TestSnapshotsOfDifferentSourcesAreRefused(t *testing.T) {
	t.Parallel()

	_, err := snapshot.Load(
		[]string{filepath.Join("testdata", "pg.json"), filepath.Join("testdata", "pg_other_source.json")},
		snapshot.Options{Now: snapshotDay},
	)
	if err == nil {
		t.Fatal("two postgres snapshots of different sources were merged")
	}
	if !errors.Is(err, domain.ErrInvalidContext) {
		t.Errorf("error = %v, want one wrapping ErrInvalidContext", err)
	}
	if !strings.Contains(err.Error(), "different source") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

func TestLoadRejectsUnreadableSnapshots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file    string
		mustSay string
	}{
		// A file written by a newer collector describes something this build cannot interpret.
		// Reading the rest of it would be a best-effort interpretation of production state.
		{"unknown_field.json", "unknown field"},
		{"bad_version.json", "not supported"},
		{"no_fingerprint.json", "sourceFingerprint"},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()

			set, err := snapshot.Load([]string{filepath.Join("testdata", tc.file)}, snapshot.Options{Now: snapshotDay})
			if err == nil {
				t.Fatalf("Load(%s) succeeded with %+v", tc.file, set)
			}
			if !errors.Is(err, domain.ErrInvalidContext) {
				t.Errorf("error = %v, want one wrapping ErrInvalidContext", err)
			}
			if !strings.Contains(err.Error(), tc.mustSay) {
				t.Errorf("error %q does not mention %q", err, tc.mustSay)
			}
		})
	}
}

// Stale context is used and flagged, never discarded. Falling back to no context would make the
// certificate quietly less informative at exactly the moment somebody stopped refreshing it.
func TestStaleSnapshotWarnsAndIsStillUsed(t *testing.T) {
	t.Parallel()

	// The fixture was collected on 2026-08-24; a month later it is well past the window.
	late := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	set, err := snapshot.Load([]string{filepath.Join("testdata", "pg.json")}, snapshot.Options{Now: late})
	if err != nil {
		t.Fatalf("a stale snapshot was rejected: %v", err)
	}
	if len(set.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want one staleness warning", set.Warnings)
	}
	if !strings.Contains(set.Warnings[0], "days old") {
		t.Errorf("the warning does not say how old: %q", set.Warnings[0])
	}
	if set.Postgres == nil || len(set.Postgres.Tables) == 0 {
		t.Error("a stale snapshot was discarded rather than used")
	}

	// Fresh on the day it was collected, and the window is configurable.
	fresh, err := snapshot.Load([]string{filepath.Join("testdata", "pg.json")}, snapshot.Options{Now: snapshotDay})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(fresh.Warnings) != 0 {
		t.Errorf("a one-day-old snapshot warned: %v", fresh.Warnings)
	}

	tight, err := snapshot.Load([]string{filepath.Join("testdata", "pg.json")},
		snapshot.Options{Now: snapshotDay, StaleAfter: time.Hour})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tight.Warnings) != 1 {
		t.Errorf("a one-hour staleness window did not warn: %v", tight.Warnings)
	}
}

// The digest is what makes a verdict attributable to the context that produced it.
func TestDigestDistinguishesContext(t *testing.T) {
	t.Parallel()

	pg := loadSet(t, "pg.json")
	both := loadSet(t, "pg.json", "k8s.json")

	if pg.Digest == both.Digest {
		t.Error("adding a kubernetes snapshot did not change the digest")
	}

	again := loadSet(t, "pg.json")
	if again.Digest != pg.Digest {
		t.Error("the same snapshot produced two digests")
	}

	// CollectedAt is deliberately excluded: it moves on every collection while the facts it
	// accompanies usually do not, and a digest that changed whenever somebody refreshed an
	// unchanged snapshot would report a different certificate for an identical verdict.
	raw, err := os.ReadFile(filepath.Join("testdata", "pg.json"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	refreshed := strings.Replace(string(raw), "2026-08-24T09:00:00Z", "2026-08-25T09:00:00Z", 1)
	if refreshed == string(raw) {
		t.Fatal("the fixture no longer contains the timestamp this test rewrites")
	}

	path := filepath.Join(t.TempDir(), "pg.json")
	if err := os.WriteFile(path, []byte(refreshed), 0o644); err != nil {
		t.Fatalf("writing the refreshed snapshot: %v", err)
	}

	later, err := snapshot.Load([]string{path}, snapshot.Options{Now: snapshotDay})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if later.Digest != pg.Digest {
		t.Errorf("re-collecting an unchanged database changed the digest:\n  before %s\n  after  %s",
			pg.Digest, later.Digest)
	}
}

// A snapshot is an input to a certificate, so its encoding has to be stable: two collections of
// the same database must produce the same bytes, whatever order the server returned rows in.
func TestEncodeIsCanonical(t *testing.T) {
	t.Parallel()

	shuffled := &snapshot.Snapshot{
		SchemaVersion:     snapshot.SchemaVersion,
		Kind:              snapshot.KindPostgres,
		CollectedAt:       snapshotDay,
		SourceFingerprint: "abcd",
		Postgres: &snapshot.PostgresData{
			Tables: []snapshot.Table{
				{Schema: "public", Name: "zebra"},
				{Schema: "archive", Name: "aardvark"},
				{Schema: "public", Name: "aardvark"},
			},
		},
	}

	ordered := &snapshot.Snapshot{
		SchemaVersion:     snapshot.SchemaVersion,
		Kind:              snapshot.KindPostgres,
		CollectedAt:       snapshotDay,
		SourceFingerprint: "abcd",
		Postgres: &snapshot.PostgresData{
			Tables: []snapshot.Table{
				{Schema: "archive", Name: "aardvark"},
				{Schema: "public", Name: "aardvark"},
				{Schema: "public", Name: "zebra"},
			},
		},
	}

	a, err := snapshot.Encode(shuffled)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := snapshot.Encode(ordered)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if string(a) != string(b) {
		t.Errorf("collection order changed the encoded bytes:\n%s\n---\n%s", a, b)
	}
}

// A file this build writes is a file this build reads. Without this, a schema change could break
// the round trip and only be noticed in production.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "pg.json"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	first, err := snapshot.Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	encoded, err := snapshot.Encode(first)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	second, err := snapshot.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode after Encode: %v", err)
	}

	reencoded, err := snapshot.Encode(second)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if string(encoded) != string(reencoded) {
		t.Errorf("the round trip is not stable:\n%s\n---\n%s", encoded, reencoded)
	}
}
