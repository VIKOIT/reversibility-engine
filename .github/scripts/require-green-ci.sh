#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Refuses to continue unless the CI workflow concluded SUCCESS for this exact commit.
#
# The release path is production code, and this is the invariant it was missing: nothing reaches
# "published" without proving what it published. v1.2.0 was tagged over a red CI run and shipped
# a fail-open — --terraform-plan stopped claiming the file it named on POSIX, so a destructive
# plan could pass a gate. The release workflow does not run the test suite (four native targets,
# and duplicating it per target would triple release time), so it has to consult the run that
# does.
#
# SUCCESS, not "not failure". Pending, cancelled, skipped, and no run at all every one mean no:
# each of them is the absence of evidence, and absence of evidence is the thing this whole
# product exists to refuse. There is deliberately no override flag — if a release must go out
# over a red CI, that is a human deciding to make CI green first.
#
# Requires: gh, and a token with `actions: read`.
# Reads: GITHUB_REPOSITORY, and the SHA as $1.

set -euo pipefail

fail() { printf '::error::%s\n' "$*" >&2; exit 1; }

SHA="${1:?usage: require-green-ci.sh <sha>}"
WORKFLOW="${CI_WORKFLOW:-ci.yml}"

printf 'Requiring a successful %s run for %s\n' "$WORKFLOW" "$SHA"

# The most recent run of the CI workflow for this exact commit. head_sha rather than a branch:
# a tag and a branch push are different events over the same commit, and it is the commit that
# is being released.
runs="$(gh api \
    "repos/${GITHUB_REPOSITORY}/actions/workflows/${WORKFLOW}/runs?head_sha=${SHA}&per_page=1" \
    --jq '.workflow_runs[0] | "\(.status)\t\(.conclusion)\t\(.html_url)"' 2> /dev/null || true)"

if [ -z "$runs" ] || [ "$runs" = "null" ]; then
    fail "No ${WORKFLOW} run exists for ${SHA}. A commit whose tests never ran is not a commit to publish: push the branch, let CI finish, and tag the commit it went green on."
fi

status="$(printf '%s' "$runs" | cut -f1)"
conclusion="$(printf '%s' "$runs" | cut -f2)"
url="$(printf '%s' "$runs" | cut -f3)"

printf 'Found run: status=%s conclusion=%s\n%s\n' "$status" "$conclusion" "$url"

if [ "$status" != 'completed' ]; then
    fail "${WORKFLOW} for ${SHA} is '${status}', not completed. Pending is not success — wait for it to finish before tagging. ${url}"
fi

if [ "$conclusion" != 'success' ]; then
    fail "${WORKFLOW} for ${SHA} concluded '${conclusion}', not success. Nothing is published over a red CI, and there is no override: make CI green and tag the commit that passed. ${url}"
fi

printf 'CI is green for %s.\n' "$SHA"
