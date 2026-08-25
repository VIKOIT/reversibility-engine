#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Posts the certificate to the pull request, replacing this action's own previous comment.
#
# A failure to comment is reported but never changes the verdict: the grade is the product and
# the comment is only its delivery.

set -euo pipefail

readonly MARKER='<!-- reversibility-engine:certificate -->'
readonly API="${GITHUB_API_URL:-https://api.github.com}"
readonly STAGE="${RUNNER_TEMP:-/tmp}/reversibility"

log()   { printf '%s\n' "$*" >&2; }
error() { printf '::error::%s\n' "$*" >&2; }

mkdir -p "$STAGE"

if ! command -v jq > /dev/null 2>&1 || ! command -v curl > /dev/null 2>&1; then
    error 'jq and curl are required to post the certificate. Both are preinstalled on GitHub-hosted runners.'
    exit 0
fi

if [ ! -s "${MARKDOWN_PATH:-}" ]; then
    log 'No certificate to post.'
    exit 0
fi

PR_NUMBER=''
if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; then
    PR_NUMBER="$(jq -r '.pull_request.number // .number // empty' "$GITHUB_EVENT_PATH")"
fi

if [ -z "$PR_NUMBER" ]; then
    log 'Not a pull request; nothing to comment on.'
    exit 0
fi

if [ -z "${GH_TOKEN:-}" ]; then
    error 'No token supplied, so the certificate cannot be posted.'
    exit 0
fi

api() {
    curl --silent --show-error --fail-with-body \
        --header "Authorization: Bearer $GH_TOKEN" \
        --header 'Accept: application/vnd.github+json' \
        "$@"
}

# comment_id finds this action's own comment by its marker.
#
# Every page is searched. On a long-lived pull request the certificate gets buried under review
# chatter, and stopping at the first page would post a duplicate on every run. Matching on an
# invisible marker rather than on author or position means the certificate's wording can change
# freely without the action losing track of its own comment.
comment_id() {
    local page=1 body count id
    while :; do
        body="$(api "$API/repos/$GITHUB_REPOSITORY/issues/$PR_NUMBER/comments?per_page=100&page=$page" 2> /dev/null || true)"

        count="$(printf '%s' "$body" | jq 'if type == "array" then length else 0 end' 2> /dev/null || echo 0)"
        if [ "$count" -eq 0 ]; then
            return 0
        fi

        id="$(printf '%s' "$body" \
            | jq -r --arg m "$MARKER" '.[] | select((.body // "") | startswith($m)) | .id' \
            2> /dev/null | head -n 1)"
        if [ -n "$id" ]; then
            printf '%s' "$id"
            return 0
        fi

        page=$((page + 1))
        if [ "$page" -gt 100 ]; then
            return 0
        fi
    done
}

payload="$STAGE/comment.json"
{ printf '%s\n\n' "$MARKER"; cat "$MARKDOWN_PATH"; } | jq -Rs '{body: .}' > "$payload"

id="$(comment_id)"
if [ -n "$id" ]; then
    method='PATCH'
    url="$API/repos/$GITHUB_REPOSITORY/issues/comments/$id"
else
    method='POST'
    url="$API/repos/$GITHUB_REPOSITORY/issues/$PR_NUMBER/comments"
fi

if api --request "$method" \
    --header 'Content-Type: application/json' \
    --data @"$payload" "$url" > /dev/null; then
    log "Certificate posted ($method)."
else
    # A fork's GITHUB_TOKEN is read-only, and so is a job that did not ask for the permission.
    error 'Could not post the certificate comment. A pull request from a fork receives a read-only token, and the job needs permissions: pull-requests: write.'
fi
