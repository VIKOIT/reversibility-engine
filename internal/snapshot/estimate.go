// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot

import (
	"fmt"
	"math"
	"time"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Throughput assumptions behind every duration this package prints.
//
// THESE ARE ASSUMPTIONS, NOT MEASUREMENTS. They are deliberately conservative figures for
// network-attached block storage on a busy server, chosen so the estimate errs toward "this will
// take longer than you think" rather than the reverse — an operator who plans for ten minutes
// and finishes in three has lost nothing, and the opposite mistake is an outage.
//
// They are constants rather than configuration on purpose: a knob here would let somebody tune
// the estimate until it said what they wanted, and a number tuned to be reassuring is worse than
// no number. Changing them is a deliberate act with a note in docs/ESTIMATES.md.
const (
	// rewriteBytesPerSecond covers a full table rewrite: read every row, write a new heap, and
	// rebuild every index. The bottleneck is sustained write throughput plus index build cost.
	rewriteBytesPerSecond = 50 << 20 // 50 MiB/s

	// scanBytesPerSecond covers a sequential read of the main fork, as SET NOT NULL and
	// constraint validation do. No writing, so several times faster than a rewrite.
	scanBytesPerSecond = 200 << 20 // 200 MiB/s
)

// Band boundaries. They are the owner's, not this package's, and are written here in the units
// the rules were stated in.
const (
	negligibleUnder = 1 * time.Second
	noticeableUnder = 30 * time.Second
	disruptiveUnder = 5 * time.Minute
)

// rateFor picks the throughput assumption for a lock hazard.
//
// Only two rates are defined, because only two kinds of work are size-proportional: rewriting a
// table and scanning one. An EXCLUSIVE lock that is not one of those — dropping an index, say —
// is not slower for being large, and inventing a rate for it would produce an OUTAGE band for an
// operation that takes milliseconds.
func rateFor(lock domain.LockHazard) (int64, bool) {
	switch lock {
	case domain.LockTableRewrite:
		return rewriteBytesPerSecond, true
	case domain.LockFullScan:
		return scanBytesPerSecond, true
	default:
		return 0, false
	}
}

// bandFor buckets an estimated duration.
//
// A band is only ever computed when the lock is at least FULL_SCAN and a snapshot established a
// size. Everything else returns the zero band, which imposes no ceiling — the absence of a band
// is the absence of evidence, and that is never treated as evidence of safety.
func bandFor(sizeBytes int64, lock domain.LockHazard) domain.LockDurationBand {
	if !lock.AtLeast(domain.LockFullScan) || sizeBytes <= 0 {
		return ""
	}

	rate, ok := rateFor(lock)
	if !ok {
		return ""
	}

	d := time.Duration(float64(sizeBytes) / float64(rate) * float64(time.Second))

	switch {
	case d < negligibleUnder:
		return domain.BandNegligible
	case d < noticeableUnder:
		return domain.BandNoticeable
	case d < disruptiveUnder:
		return domain.BandDisruptive
	default:
		return domain.BandOutage
	}
}

// estimateFor renders the duration for a lock hazard, or "" when the hazard is not one whose
// cost scales with size.
func estimateFor(sizeBytes int64, lock domain.LockHazard) string {
	rate, ok := rateFor(lock)
	if !ok {
		return ""
	}
	return estimate(sizeBytes, rate)
}

// estimate renders an approximate duration for moving sizeBytes at the given rate.
//
// The leading tilde is not decoration. Every one of these numbers is derived from a planner
// estimate and a hard-coded throughput assumption, and the tilde is what stops a reader
// remembering it as a measurement. A value the user learns to distrust is worse than no value,
// so the format never implies precision it does not have: two significant figures at most, and
// anything under a second is reported as under a second rather than as a suspiciously exact
// number of milliseconds.
func estimate(sizeBytes int64, bytesPerSecond int64) string {
	if sizeBytes <= 0 || bytesPerSecond <= 0 {
		return ""
	}

	seconds := float64(sizeBytes) / float64(bytesPerSecond)

	switch {
	case seconds < 1:
		return "~<1s"
	case seconds < 90:
		return fmt.Sprintf("~%ds", int(math.Round(seconds)))
	case seconds < 90*60:
		return fmt.Sprintf("~%dm", int(math.Round(seconds/60)))
	default:
		hours := seconds / 3600
		if hours < 10 {
			return fmt.Sprintf("~%.1fh", hours)
		}
		return fmt.Sprintf("~%dh", int(math.Round(hours)))
	}
}

// EstimatedDuration exposes the same calculation for tests and for documentation examples.
func EstimatedDuration(sizeBytes int64, kind string) time.Duration {
	rate := int64(scanBytesPerSecond)
	if kind == "rewrite" {
		rate = rewriteBytesPerSecond
	}
	if sizeBytes <= 0 {
		return 0
	}
	return time.Duration(float64(sizeBytes) / float64(rate) * float64(time.Second))
}

// formatBytes renders a size the way an operator reads one.
//
// Binary units, because that is what Postgres and Kubernetes both report, and one decimal place
// at most — a table is never "48.37 GB" in a sentence a human is meant to act on.
func formatBytes(b int64) string {
	if b < 0 {
		return "an unknown size"
	}
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}

	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(b)
	unit := ""

	for _, u := range units {
		value /= 1024
		unit = u
		if value < 1024 {
			break
		}
	}

	if value >= 100 {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

// formatRows renders a row count at the precision the estimate deserves.
//
// reltuples is itself an estimate that ANALYZE refreshes lazily, so printing "212,481,993" would
// claim a precision the source does not have. -1 is Postgres's "never analyzed".
func formatRows(n int64) string {
	switch {
	case n < 0:
		return "an unknown number of"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	case n < 1_000_000_000:
		return trimZero(float64(n)/1_000_000) + "M"
	default:
		return trimZero(float64(n)/1_000_000_000) + "B"
	}
}

func trimZero(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// formatPercent renders a fraction as a percentage, never rounding a non-zero fraction down to
// "0%" — a column with one null in ten million still fails the migration, and reporting that as
// zero would be the single most misleading thing this package could print.
func formatPercent(f float64) string {
	switch {
	case f <= 0:
		return "0%"
	case f < 0.0001:
		return "<0.01%"
	case f < 0.01:
		return fmt.Sprintf("%.2f%%", f*100)
	default:
		return fmt.Sprintf("%.0f%%", f*100)
	}
}
