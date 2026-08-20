#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Entrypoint for the Reversibility Engine container action.
#
# It reconstructs the two trees revctl compares, runs the gate, and posts the certificate
# to the pull request. Every path out of this script that is not a clean analysis ends in a
# non-zero exit and, where a comment is possible, a posted explanation: a pull request with
# no certificate is indistinguishable from one that was never analyzed.

set -euo pipefail

readonly MARKER='<!-- reversibility-engine:certificate -->'
readonly API="${GITHUB_API_URL:-https://api.github.com}"
readonly WORKSPACE="${GITHUB_WORKSPACE:-/github/workspace}"
readonly STAGE="${RUNNER_TEMP:-/tmp}/reversibility"
readonly BASE_TREE="$STAGE/base"
readonly HEAD_TREE="$STAGE/head"
readonly CERT_MD="$WORKSPACE/reversibility-certificate.md"
readonly CERT_JSON="$STAGE/certificate.json"

log()   { printf '%s\n' "$*" >&2; }
error() { printf '::error::%s\n' "$*" >&2; }

# input reads an action input.
#
# GitHub exposes inputs to a container action as INPUT_<NAME>, uppercased. It replaces
# spaces with underscores but leaves dashes alone, so "min-grade" arrives as INPUT_MIN-GRADE
# -- a name no shell can expand as $INPUT_MIN-GRADE. printenv reads it by string instead.
# The underscore spelling is tried as well, so a future runner that normalises dashes does
# not silently start handing every input its default.
input() {
    local dashed underscored value
    dashed="INPUT_$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')"
    underscored="$(printf '%s' "$dashed" | tr '-' '_')"

    value="$(printenv "$dashed" 2>/dev/null || true)"
    if [ -z "$value" ]; then
        value="$(printenv "$underscored" 2>/dev/null || true)"
    fi
    printf '%s' "$value"
}

emit() {
    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"
    fi
}

# comment_id finds this action's own comment by its marker.
#
# Every page is searched. On a long-lived pull request the certificate gets buried under
# review chatter, and stopping at the first page would post a duplicate on every run.
comment_id() {
    local page=1 body count id
    while :; do
        body="$(curl --silent --show-error --fail-with-body \
            --header "Authorization: Bearer $TOKEN" \
            --header 'Accept: application/vnd.github+json' \
            "$API/repos/$GITHUB_REPOSITORY/issues/$PR_NUMBER/comments?per_page=100&page=$page" \
            2>/dev/null || true)"

        count="$(printf '%s' "$body" | jq 'if type == "array" then length else 0 end' 2>/dev/null || echo 0)"
        if [ "$count" -eq 0 ]; then
            return 0
        fi

        id="$(printf '%s' "$body" \
            | jq -r --arg m "$MARKER" '.[] | select((.body // "") | startswith($m)) | .id' \
            2>/dev/null | head -n 1)"
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

# post_certificate writes the given markdown file to the pull request, replacing this
# action's previous comment. A failure to comment is reported but never masks the verdict:
# the grade is the product, the comment is only its delivery.
post_certificate() {
    local file="$1" payload id url method
    if [ "$COMMENT" != 'true' ] || [ -z "$PR_NUMBER" ]; then
        return 0
    fi
    if [ -z "$TOKEN" ]; then
        error 'No token supplied, so the certificate cannot be posted.'
        return 0
    fi

    payload="$STAGE/comment.json"
    { printf '%s\n\n' "$MARKER"; cat "$file"; } | jq -Rs '{body: .}' > "$payload"

    id="$(comment_id)"
    if [ -n "$id" ]; then
        method='PATCH'
        url="$API/repos/$GITHUB_REPOSITORY/issues/comments/$id"
    else
        method='POST'
        url="$API/repos/$GITHUB_REPOSITORY/issues/$PR_NUMBER/comments"
    fi

    if curl --silent --show-error --fail-with-body --request "$method" \
        --header "Authorization: Bearer $TOKEN" \
        --header 'Accept: application/vnd.github+json' \
        --header 'Content-Type: application/json' \
        --data @"$payload" "$url" > /dev/null; then
        log "Certificate posted ($method)."
    else
        # A fork's GITHUB_TOKEN is read-only, and so is a job without pull-requests: write.
        error 'Could not post the certificate comment. A pull request from a fork receives a read-only token, and the job needs permissions: pull-requests: write.'
    fi
}

# fail_closed reports that no verdict could be reached and exits non-zero. It is the only
# way out of this script that skips revctl, and it never exits 0.
fail_closed() {
    local reason="$1"
    error "$reason"

    {
        printf '## Reversibility Certificate — Grade F\n\n'
        printf '**Analysis did not run.** No verdict was reached, and an unreached verdict is not a pass.\n\n'
        printf '| | |\n| --- | --- |\n'
        printf '| **Grade** | F |\n'
        printf '| **AI merge gate** | FAIL |\n\n'
        printf '### Blockers\n\n- %s\n' "$reason"
    } > "$CERT_MD"

    emit 'grade' 'F'
    emit 'gate' 'FAIL'
    emit 'applicable' 'true'
    emit 'certificate' "$CERT_MD"

    post_certificate "$CERT_MD"
    exit 2
}

# --------------------------------------------------------------------------------------
# Inputs and environment
# --------------------------------------------------------------------------------------

TOKEN="$(input 'github-token')"
MIN_GRADE="$(input 'min-grade')"
MIN_GRADE="${MIN_GRADE:-B}"
INCLUDE="$(input 'include')"
INCLUDE="${INCLUDE:-*.sql *.yaml *.yml}"
EXCLUDE="$(input 'exclude')"
COMMENT="$(input 'comment')"
COMMENT="${COMMENT:-true}"
FAIL_ON_GATE="$(input 'fail-on-gate')"
FAIL_ON_GATE="${FAIL_ON_GATE:-true}"

case "$MIN_GRADE" in
    A|B|C|F) ;;
    *) error "min-grade must be A, B, C, or F; got '$MIN_GRADE'."; exit 2 ;;
esac

mkdir -p "$STAGE" "$BASE_TREE" "$HEAD_TREE"
cd "$WORKSPACE"

# The workspace is bind-mounted and owned by the runner user, so to git inside this
# container it looks like someone else's repository.
git config --global --add safe.directory "$WORKSPACE" 2>/dev/null || true

PR_NUMBER=''
BASE_SHA=''
if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; then
    PR_NUMBER="$(jq -r '.pull_request.number // .number // empty' "$GITHUB_EVENT_PATH")"
    BASE_SHA="$(jq -r '.pull_request.base.sha // empty' "$GITHUB_EVENT_PATH")"
fi

if [ -z "$PR_NUMBER" ] || [ -z "$BASE_SHA" ]; then
    error "This action analyzes pull requests; event '${GITHUB_EVENT_NAME:-unknown}' carries no pull request to compare against."
    exit 2
fi

# The head side is read from the working tree rather than from the pull request's head SHA,
# so the files analyzed are exactly the files checked out. When actions/checkout has left
# the merge commit in place -- its default -- that is the merged result, which is what would
# actually land on the base branch.
HEAD_REF="$(git rev-parse HEAD)"

if ! git cat-file -e "${BASE_SHA}^{commit}" 2>/dev/null; then
    log "Base commit $BASE_SHA is not in the local history; fetching it."
    git fetch --no-tags --quiet origin "$BASE_SHA" 2>/dev/null || true
fi
if ! git cat-file -e "${BASE_SHA}^{commit}" 2>/dev/null; then
    fail_closed "The base commit $BASE_SHA could not be reached, so there is nothing to compare against. Set 'fetch-depth: 0' on actions/checkout."
fi

# --------------------------------------------------------------------------------------
# Stage the two trees
# --------------------------------------------------------------------------------------

# Globbing is disabled while the pathspecs are split: '*.sql' must reach git as a pattern,
# not be expanded by this shell against the workspace first.
set -f
# shellcheck disable=SC2206
SPEC=($INCLUDE)
for pattern in $EXCLUDE; do
    SPEC+=(":!$pattern")
done
set +f

CHANGED=()
while IFS= read -r line; do
    if [ -n "$line" ]; then
        CHANGED+=("$line")
    fi
done < <(git diff --name-only --diff-filter=d "$BASE_SHA" "$HEAD_REF" -- "${SPEC[@]}" 2>/dev/null || true)

if [ "${#CHANGED[@]}" -eq 0 ]; then
    log 'No changed file matches an analyzer. Nothing to certify.'
    emit 'grade' 'A'
    emit 'gate' 'PASS'
    emit 'applicable' 'false'
    emit 'certificate' ''
    exit 0
fi

log "Analyzing ${#CHANGED[@]} changed file(s):"
for f in "${CHANGED[@]}"; do
    log "  $f"
    mkdir -p "$HEAD_TREE/$(dirname "$f")" "$BASE_TREE/$(dirname "$f")"
    cp "$WORKSPACE/$f" "$HEAD_TREE/$f"

    # A file this pull request added has no base side. Leaving it absent is what makes the
    # analyzers see it as ADDED rather than as a modification of an empty file.
    if ! git show "$BASE_SHA:$f" > "$BASE_TREE/$f" 2>/dev/null; then
        rm -f "$BASE_TREE/$f"
    fi
done

# --------------------------------------------------------------------------------------
# Analyze
# --------------------------------------------------------------------------------------

# Markdown carries the gate, because its exit code is the one the job is graded on.
# Exit codes: 0 met, 1 below min-grade, 2 the run did not complete.
set +e
revctl check --before "$BASE_TREE" "$HEAD_TREE" \
    --format markdown --output "$CERT_MD" --min-grade "$MIN_GRADE"
RC=$?
set -e

if [ ! -s "$CERT_MD" ]; then
    fail_closed 'revctl produced no certificate.'
fi

# JSON is rendered separately purely to read the grade back for this action's outputs.
# Re-deriving a grade in shell would be a second definition of the verdict, and the second
# one is always the one that gets it wrong in the permissive direction.
GRADE='F'
GATE='FAIL'
APPLICABLE='true'
if revctl check --before "$BASE_TREE" "$HEAD_TREE" \
        --format json --output "$CERT_JSON" > /dev/null 2>&1 && [ -s "$CERT_JSON" ]; then
    GRADE="$(jq -r '.grade // "F"' "$CERT_JSON")"
    GATE="$(jq -r '.aiGateStatus // "FAIL"' "$CERT_JSON")"
    APPLICABLE="$(jq -r '.applicable // true' "$CERT_JSON")"
fi

emit 'grade' "$GRADE"
emit 'gate' "$GATE"
emit 'applicable' "$APPLICABLE"
emit 'certificate' "$CERT_MD"

post_certificate "$CERT_MD"

# --------------------------------------------------------------------------------------
# Verdict
# --------------------------------------------------------------------------------------

if [ "$RC" -ge 2 ]; then
    error 'The analysis did not complete. This is a broken run, not a passing one.'
    exit "$RC"
fi

if [ "$RC" -eq 1 ]; then
    if [ "$FAIL_ON_GATE" = 'true' ]; then
        error "Grade $GRADE is below the required minimum $MIN_GRADE. See the certificate on the pull request."
        exit 1
    fi
    log "Grade $GRADE is below $MIN_GRADE, but fail-on-gate is false; not failing the job."
    exit 0
fi

log "Grade $GRADE meets the minimum $MIN_GRADE."
exit 0
