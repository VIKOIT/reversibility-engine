// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// InputDigest returns the SHA-256 over the analyzed changeset.
//
// It is what makes a certificate attributable to an exact input: two certificates with the same
// digest were produced from the same bytes, and a certificate whose digest does not match the
// changeset in front of you is a certificate for something else.
//
// Every field is length-prefixed before hashing. Concatenating raw values would let two
// different changesets collide — a file "ab" with content "c" would hash identically to a file
// "a" with content "bc" — and a digest that can collide is not evidence of anything.
//
// Both sides of every change are hashed, not just the new content. Reversibility is a property
// of a transition, so two changesets that reach the same final state from different starting
// points are different inputs and must not share a digest.
func InputDigest(files []domain.ChangedFile) string {
	sorted := make([]domain.ChangedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	h := sha256.New()
	for _, f := range sorted {
		writeField(h, []byte(f.Path))
		writeField(h, []byte(f.PreviousPath))
		writeField(h, []byte(f.Status))
		writeField(h, f.Previous)
		writeField(h, f.Current)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// writeField writes a length-prefixed field. hash.Hash never returns an error, which is why
// this can ignore one.
func writeField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))

	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}
