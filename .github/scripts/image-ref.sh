#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# The one place that turns GITHUB_REPOSITORY into a container image reference.
#
# There were two, and that is the whole reason this file exists. publish-image.yml passed
# `ghcr.io/${{ github.repository }}` to docker/metadata-action, which lowercases the image name
# for you; restore-image-tag.yml built the same string by hand and passed it straight to
# `docker buildx imagetools`, which does not. This owner is `VIKOIT`, so one workflow worked and
# the other could never work at all:
#
#     ERROR: invalid reference format: repository name (VIKOIT/reversibility-engine)
#            must be lowercase
#
# Two producers of one value, agreeing until they do not -- docs/SPECIFICATION.md §13, the rule
# `ResolveRoot` and `QualifyPath` broke. The consequence here was worse than a disagreement: the
# repair workflow for the :v1 incident had never run to completion, so the incident it existed to
# close stayed open, and the repository read as though it were closed.
#
# Derived, never hardcoded. A repository rename must move this reference with it rather than
# silently leave it pointing at a name that no longer exists.
#
# Writes IMAGE to $GITHUB_ENV, and prints it. Reads: GITHUB_REPOSITORY, REGISTRY (default ghcr.io).

set -euo pipefail

fail() { printf '::error::%s\n' "$*" >&2; exit 1; }

REPOSITORY="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is unset; there is no repository to derive an image name from}"
REGISTRY="${REGISTRY:-ghcr.io}"

path="$(printf '%s' "$REPOSITORY" | tr '[:upper:]' '[:lower:]')"
image="${REGISTRY}/${path}"

# ASSERTED AGAINST THE REGISTRY'S OWN RULES, BEFORE ANYTHING USES IT.
#
# The registry told us in plain language what was wrong and nothing in this repository had asked
# it. These checks ask, at the one moment the answer is cheap -- before a login, before a pull,
# before a tag is mutated -- and name the offending value rather than leaving a reader to infer
# it from an `invalid reference format` three steps later.
#
# The rule for a path component is the OCI distribution spec's: lowercase alphanumerics,
# optionally separated by a single period, one or two underscores, or one or more dashes.

case "$path" in
    *[[:upper:]]*)
        fail "Image path '${path}' contains uppercase, derived from GITHUB_REPOSITORY='${REPOSITORY}'. A container repository name must be lowercase; this is the defect that made restore-image-tag.yml unable to run."
        ;;
esac

case "$path" in
    */) fail "Image path '${path}' ends with a separator." ;;
    /*) fail "Image path '${path}' begins with a separator." ;;
    *//*) fail "Image path '${path}' contains an empty component." ;;
esac

# Each `/`-separated component, checked on its own. `set -f` keeps a component containing a glob
# character from being expanded against the working directory before it is ever tested.
set -f
old_ifs="$IFS"
IFS='/'
# shellcheck disable=SC2086
set -- $path
IFS="$old_ifs"
set +f

for component in "$@"; do
    if ! printf '%s' "$component" | grep -qE '^[a-z0-9]+([._]|__|[-]+)?([a-z0-9]+([._]|__|[-]+)?)*$'; then
        fail "Image path component '${component}' is not a valid container repository name. Allowed: lowercase alphanumerics separated by a single period, one or two underscores, or one or more dashes. Derived from GITHUB_REPOSITORY='${REPOSITORY}'."
    fi
done

printf 'Image reference: %s\n' "$image"

if [ -n "${GITHUB_ENV:-}" ]; then
    printf 'IMAGE=%s\n' "$image" >> "$GITHUB_ENV"
fi
