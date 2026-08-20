// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package kubernetes

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// quantitySuffixes maps the Kubernetes resource-quantity suffixes to their multipliers.
//
// Both scales exist because Kubernetes accepts both: 1Gi is 2^30 bytes and 1G is 10^9. A
// comparison that treated them as equal would call a shrink from 1Gi to 1G an increase.
var quantitySuffixes = map[string]float64{
	"":  1,
	"m": 1e-3,

	"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,

	"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30,
	"Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
}

// parseQuantity converts a Kubernetes resource quantity such as "10Gi" or "500m" to a number
// comparable against another quantity.
//
// It returns an error rather than a best guess for anything it does not recognise. The caller
// turns that into UNKNOWN: claiming a volume shrank when the quantity could not be read would
// be a false accusation, and claiming it did not would be worse.
func parseQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty quantity")
	}

	// The suffix is the longest trailing run of letters; the rest must be a number.
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			i--
			continue
		}
		break
	}

	number, suffix := s[:i], s[i:]

	multiplier, ok := quantitySuffixes[suffix]
	if !ok {
		return 0, fmt.Errorf("unrecognised quantity suffix %q in %q", suffix, s)
	}

	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("quantity %q: %w", s, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("quantity %q is not a finite number", s)
	}

	return value * multiplier, nil
}

// quantityAt reads a quantity from a decoded document.
//
// Quantities may be written unquoted, in which case the YAML decoder hands back a number rather
// than a string — "storage: 1000000" is as valid as "storage: 1Gi".
func quantityAt(m map[string]any, path ...string) (float64, bool, error) {
	v := valueAt(m, path...)
	if v == nil {
		return 0, false, nil
	}

	switch t := v.(type) {
	case string:
		q, err := parseQuantity(t)
		return q, true, err
	case float64:
		return t, true, nil
	default:
		return 0, true, fmt.Errorf("quantity at %s has unexpected type %T", strings.Join(path, "."), v)
	}
}
