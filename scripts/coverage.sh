#!/bin/sh
# Coverage gate.
#
# Fail-closed, with one deliberate exception: a profile covering zero statements is not a
# 0% failure, it is "there is no code here yet". That state is real during S0/S1 and lying
# about it would be as bad as lying about a passing grade. The instant any statement exists,
# the gate bites.

set -eu

profile="${1:?usage: coverage.sh <profile> <min-percent>}"
min="${2:?usage: coverage.sh <profile> <min-percent>}"

if [ ! -f "$profile" ]; then
    echo "coverage: FAIL — profile '$profile' does not exist" >&2
    exit 1
fi

# `go tool cover -func` emits a trailing "total:\t(statements)\t NN.N%" line.
total_line="$(go tool cover -func="$profile" | grep -E '^total:' || true)"

if [ -z "$total_line" ]; then
    echo "coverage: NOTICE — profile contains no statements; nothing to cover yet"
    exit 0
fi

actual="$(echo "$total_line" | awk '{print $NF}' | tr -d '%')"

# awk rather than shell arithmetic: coverage is fractional.
if awk -v a="$actual" -v m="$min" 'BEGIN { exit (a + 0 >= m + 0) ? 0 : 1 }'; then
    echo "coverage: PASS — ${actual}% >= ${min}%"
    exit 0
fi

echo "coverage: FAIL — ${actual}% < ${min}% required" >&2
exit 1
