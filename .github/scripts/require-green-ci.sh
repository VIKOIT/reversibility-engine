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

# THE ASSUMPTION, ASSERTED RATHER THAN ASSUMED.
#
# Everything below rests on one thing: that `runs?head_sha=<sha>` returns CI's verdict on the
# commit being released. That is exact for a `push` event, which is what a release tag produces
# and the only event this gate has ever been exercised under.
#
# It is not obviously exact for anything else, and the failure would be quiet: the query returns
# *a* run, the script reads *a* conclusion, and a wrong-but-green answer looks identical to a
# right one. That is the fail-open shape this whole release path keeps producing, so the script
# refuses rather than guesses.
#
# This is deliberately not open support for other events. A branch for an event nobody runs is an
# unrun code path inside the one component whose entire job is to be trusted -- a guard that looks
# like a guard and is not.
#
# So the permitted set is an explicit allowlist, defaulting to `push` alone, and a caller that
# needs another event has to name it at the call site where a reader will see it. There is no
# wildcard and no "any event" value.
#
# `workflow_dispatch` is permitted by exactly one caller: restore-image-tag.yml, which is a manual
# repair tool and cannot be anything but a dispatch. Its dispatch runs against a branch, so
# `github.sha` is that branch's head commit -- the same thing a push gives, resolved the same way
# by the same query. That reasoning is why it is allowed; it is not why it is trusted. What makes
# it exercised rather than assumed is that the restore itself runs through this gate.
EVENT="${GITHUB_EVENT_NAME:-}"
ALLOWED_EVENTS="${CI_GATE_EVENTS:-push}"

permitted=0
for allowed in $ALLOWED_EVENTS; do
    if [ "$EVENT" = "$allowed" ]; then
        permitted=1
        break
    fi
done

if [ "$permitted" -ne 1 ]; then
    fail "This gate is permitted for event(s) '${ALLOWED_EVENTS}' and this run is '${EVENT:-unset}'. It resolves CI by head_sha, and what head_sha means for a '${EVENT:-unset}' event here is an untested assumption -- so it refuses rather than return an answer nobody has checked. A release is cut by pushing a tag; do that. To permit another event, establish what head_sha resolves to under it, prove this gate still refuses a red CI under it, and then name it in CI_GATE_EVENTS at the call site."
fi

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
