#!/bin/sh
# Enforces docs/SPECIFICATION.md §6 rule 1: internal/domain imports nothing outside the standard
# library.
#
# The domain package is the spine every other package depends on. The moment it grows a
# third-party import, the "types only, depends on nothing" guarantee is gone and the
# dependency graph starts to rot from the centre. Cheap to check, so check it every build.

set -eu

pkg="./internal/domain/..."

# `go list` resolves the transitive import set; Deps rather than Imports so an indirect
# third-party dependency cannot sneak in through a local helper.
deps="$(go list -f '{{ join .Deps "\n" }}' "$pkg" 2>/dev/null | sort -u || true)"

if [ -z "$deps" ]; then
    echo "domain-imports: NOTICE — no packages under internal/domain yet"
    exit 0
fi

# Standard-library import paths are exactly those whose first path element has no dot.
# Anything with a dot in its first element is a module path, i.e. third party.
violations="$(echo "$deps" | awk -F/ '$1 ~ /\./ { print }')"

if [ -n "$violations" ]; then
    echo "domain-imports: FAIL — internal/domain must import only the standard library" >&2
    echo "$violations" | sed 's/^/  /' >&2
    exit 1
fi

echo "domain-imports: PASS — stdlib only"
