// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// DefaultStaleAfter is when a snapshot starts being reported as old.
//
// Seven days is not a safety boundary — a stale snapshot is still used, and using it is still
// better than guessing. It is the point at which a reader should be told the numbers describe a
// database that has since moved on.
const DefaultStaleAfter = 7 * 24 * time.Hour

// Set is the merged production context for one run.
//
// A nil *Set is valid and means "no context": every method is safe to call on it and returns
// nothing, which is what keeps context optional without every caller growing a branch.
type Set struct {
	Postgres   *PostgresData
	Kubernetes *KubernetesData

	// Warnings describe what was wrong with the files that were loaded — a stale snapshot,
	// most often. They reach the certificate.
	Warnings []string

	// Digest is the SHA-256 over the resolved context, for the certificate's input digest.
	Digest string

	// fingerprints records the source each kind came from, so a second file for the same kind
	// from a different source can be refused.
	fingerprints map[Kind]string
}

// Options configures loading.
type Options struct {
	// Now is the clock, injected so that staleness is testable and a past run reproducible.
	Now time.Time

	// StaleAfter overrides DefaultStaleAfter. Zero means the default.
	StaleAfter time.Duration
}

// Load reads and merges snapshot files.
//
// A path that does not exist is NOT an error. Production context is an enhancement: the engine
// has to work exactly as it did before snapshots existed, and a missing file is the normal state
// for most repositories.
//
// Everything else is an error. A file that exists and cannot be read, or that comes from a
// different source than a previous file of the same kind, stops the run — context that is wrong
// is worse than context that is absent, because it is trusted.
func Load(paths []string, opts Options) (*Set, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.StaleAfter == 0 {
		opts.StaleAfter = DefaultStaleAfter
	}

	set := &Set{fingerprints: map[Kind]string{}}
	loaded := 0

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// The documented, ordinary case: a workflow passes --context unconditionally
				// and the snapshot has not been collected yet.
				continue
			}
			return nil, fmt.Errorf("%w: reading %s: %w", domain.ErrInvalidContext, path, err)
		}

		snap, err := Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", domain.ErrInvalidContext, path, err)
		}

		if err := set.merge(snap, path, opts); err != nil {
			return nil, err
		}
		loaded++
	}

	if loaded == 0 {
		return nil, nil
	}

	set.Digest = set.digest()
	sort.Strings(set.Warnings)
	return set, nil
}

// Decode parses a snapshot file, rejecting anything it cannot fully understand.
func Decode(raw []byte) (*Snapshot, error) {
	var snap Snapshot

	dec := json.NewDecoder(bytes.NewReader(raw))
	// An unknown field means the file was written by a version that collected something this
	// build does not know about. Reading the rest and ignoring that would be a best-effort
	// interpretation of production state, which is precisely what must not happen quietly.
	dec.DisallowUnknownFields()

	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}
	if err := snap.Validate(); err != nil {
		return nil, err
	}

	snap.Canonicalize()
	return &snap, nil
}

// merge folds one snapshot into the set.
func (s *Set) merge(snap *Snapshot, path string, opts Options) error {
	// A second file for the same kind from a different source is a configuration error. Two
	// databases merged into one view would answer questions about a table that exists in one of
	// them, and there would be no way to tell which.
	if previous, ok := s.fingerprints[snap.Kind]; ok && previous != snap.SourceFingerprint {
		return fmt.Errorf("%w: %s is a %s snapshot of a different source than the one already loaded "+
			"(fingerprint %s, expected %s); snapshots of two environments cannot be merged",
			domain.ErrInvalidContext, path, snap.Kind, short(snap.SourceFingerprint), short(previous))
	}
	s.fingerprints[snap.Kind] = snap.SourceFingerprint

	if age := snap.Age(opts.Now); age > opts.StaleAfter {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"the %s snapshot is %d days old (collected %s); its numbers describe a system that has since moved on",
			snap.Kind, int(age.Hours()/24), snap.CollectedAt.UTC().Format("2006-01-02")))
	}

	switch snap.Kind {
	case KindPostgres:
		if s.Postgres == nil {
			s.Postgres = &PostgresData{}
		}
		s.Postgres.Tables = append(s.Postgres.Tables, snap.Postgres.Tables...)
		s.Postgres.Indexes = append(s.Postgres.Indexes, snap.Postgres.Indexes...)
		s.Postgres.Columns = append(s.Postgres.Columns, snap.Postgres.Columns...)

	case KindKubernetes:
		if s.Kubernetes == nil {
			s.Kubernetes = &KubernetesData{}
		}
		s.Kubernetes.StorageClasses = append(s.Kubernetes.StorageClasses, snap.Kubernetes.StorageClasses...)
		s.Kubernetes.Claims = append(s.Kubernetes.Claims, snap.Kubernetes.Claims...)
		s.Kubernetes.Workloads = append(s.Kubernetes.Workloads, snap.Kubernetes.Workloads...)
	}

	return nil
}

// digest hashes the resolved context.
//
// CollectedAt is deliberately excluded. It moves on every collection while the facts it
// accompanies usually do not, and a digest that changed whenever somebody refreshed an unchanged
// snapshot would report a different certificate for an identical verdict. What is hashed is what
// the enrichment can actually read.
func (s *Set) digest() string {
	h := sha256.New()

	if s.Postgres != nil {
		writeField(h, []byte("postgres"))
		for _, t := range s.Postgres.Tables {
			writeField(h, []byte(qualify(t.Schema, t.Name)))
			writeInt(h, t.RowEstimate)
			writeInt(h, t.SizeBytes)
			writeInt(h, t.TotalSizeBytes)
		}
		for _, i := range s.Postgres.Indexes {
			writeField(h, []byte(qualify(i.Schema, i.Name)))
			writeField(h, []byte(i.Table))
			writeInt(h, i.SizeBytes)
			writeInt(h, i.Scans)
		}
		for _, c := range s.Postgres.Columns {
			writeField(h, []byte(qualify(c.Schema, c.Table)))
			writeField(h, []byte(c.Name))
			writeField(h, []byte(fmt.Sprintf("%g", c.NullFraction)))
			writeInt(h, int64(c.AverageWidth))
		}
	}

	if s.Kubernetes != nil {
		writeField(h, []byte("kubernetes"))
		for _, sc := range s.Kubernetes.StorageClasses {
			writeField(h, []byte(sc.Name))
			writeField(h, []byte(sc.ReclaimPolicy))
		}
		for _, c := range s.Kubernetes.Claims {
			writeField(h, []byte(c.Namespace))
			writeField(h, []byte(c.Name))
			writeField(h, []byte(c.Phase))
			writeField(h, []byte(c.StorageClass))
			writeField(h, []byte(c.Capacity))
		}
		for _, w := range s.Kubernetes.Workloads {
			writeField(h, []byte(w.Namespace))
			writeField(h, []byte(w.Kind))
			writeField(h, []byte(w.Name))
			writeInt(h, int64(w.Replicas))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))

	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}

func writeInt(h interface{ Write([]byte) (int, error) }, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	_, _ = h.Write(b[:])
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// Encode renders a snapshot as canonical JSON.
//
// Keys are ordered by encoding/json's struct field order, collections are sorted by
// Canonicalize, and the output is indented so a human can read what was collected about their
// database — which is the only way "metadata only" is verifiable rather than merely claimed.
func Encode(snap *Snapshot) ([]byte, error) {
	snap.Canonicalize()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(snap); err != nil {
		return nil, fmt.Errorf("encoding snapshot: %w", err)
	}
	return buf.Bytes(), nil
}
