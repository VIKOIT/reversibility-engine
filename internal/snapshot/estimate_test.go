// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package snapshot

import "testing"

// Every duration carries a tilde, and no duration claims more precision than a planner estimate
// and a hard-coded throughput assumption can support. A number the reader learns to distrust is
// worse than no number.
func TestEstimateFormatting(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	tests := []struct {
		name string
		size int64
		rate int64
		want string
	}{
		{"nothing to do", 0, rewriteBytesPerSecond, ""},
		{"negative size", -1, rewriteBytesPerSecond, ""},
		{"sub-second work is not given false precision", mib, scanBytesPerSecond, "~<1s"},
		{"seconds", 500 * mib, scanBytesPerSecond, "~3s"},
		{"minutes", 100 * 1024 * mib, scanBytesPerSecond, "~9m"},
		{"a 48 GiB rewrite", 48 * 1024 * mib, rewriteBytesPerSecond, "~16m"},
		{"hours keep one decimal", 1024 * 1024 * mib, rewriteBytesPerSecond, "~5.8h"},
		{"many hours drop the decimal", 8 * 1024 * 1024 * mib, rewriteBytesPerSecond, "~47h"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := estimate(tc.size, tc.rate); got != tc.want {
				t.Errorf("estimate(%d, %d) = %q, want %q", tc.size, tc.rate, got, tc.want)
			}
		})
	}
}

func TestEstimateAlwaysMarksItselfAsAnEstimate(t *testing.T) {
	t.Parallel()

	for size := int64(1); size < 1<<45; size *= 7 {
		for _, rate := range []int64{rewriteBytesPerSecond, scanBytesPerSecond} {
			got := estimate(size, rate)
			if got == "" {
				continue
			}
			if got[0] != '~' {
				t.Fatalf("estimate(%d, %d) = %q, which could be read as a measurement", size, rate, got)
			}
		}
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{51539607552, "48.0 GiB"},
		{2199023255552, "2.0 TiB"},
		{150 * (1 << 20), "150 MiB"},
		{-1, "an unknown size"},
	}

	for _, tc := range tests {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// reltuples is itself an estimate that ANALYZE refreshes lazily, so a row count is never printed
// to the digit. -1 is Postgres's "never analyzed".
func TestFormatRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int64
		want string
	}{
		{-1, "an unknown number of"},
		{0, "0"},
		{999, "999"},
		{12345, "12K"},
		{212000000, "212M"},
		{1500000, "1.5M"},
		{3200000000, "3.2B"},
	}

	for _, tc := range tests {
		if got := formatRows(tc.in); got != tc.want {
			t.Errorf("formatRows(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A column with one null in ten million still fails the migration. Rounding that to "0%" would
// be the single most misleading thing this package could print.
func TestFormatPercentNeverRoundsANonZeroFractionToZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want string
	}{
		{0, "0%"},
		{0.00000005, "<0.01%"},
		{0.0001, "0.01%"},
		{0.005, "0.50%"},
		{0.31, "31%"},
		{1, "100%"},
	}

	for _, tc := range tests {
		if got := formatPercent(tc.in); got != tc.want {
			t.Errorf("formatPercent(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for f := 1e-9; f < 0.5; f *= 3 {
		if got := formatPercent(f); got == "0%" {
			t.Fatalf("formatPercent(%v) = %q; a non-zero null fraction must never read as zero", f, got)
		}
	}
}
