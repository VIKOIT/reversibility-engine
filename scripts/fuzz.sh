#!/bin/sh
# Runs every fuzz target in turn.
#
# Go fuzzes one target at a time, so a repository with several needs a loop. Discovering targets
# with `go test -list` rather than listing them here means a new FuzzXxx is picked up the moment
# it is written — a fuzz target nobody remembered to add to a list is a fuzz target that never
# runs.

set -eu

duration="${1:-30s}"
shift 2>/dev/null || true

packages="$*"
if [ -z "$packages" ]; then
    packages="./internal/analyzer/postgres ./internal/analyzer/postgres/parser ./internal/analyzer/kubernetes ./internal/engine ./internal/delivery/github"
fi

status=0

for pkg in $packages; do
    for target in $(go test -list='^Fuzz' "$pkg" 2>/dev/null | grep '^Fuzz' || true); do
        echo "==> $target  ($pkg, ${duration})"

        # -run='^$' selects no ordinary tests, so only the fuzz target runs.
        if ! go test -run='^$' -fuzz="^${target}\$" -fuzztime="$duration" "$pkg"; then
            echo "!!! $target FAILED in $pkg" >&2
            status=1
        fi
    done
done

if [ "$status" -eq 0 ]; then
    echo "fuzz: all targets survived ${duration} each"
fi

exit "$status"
