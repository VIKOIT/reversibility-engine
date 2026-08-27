#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Runs the gate and reports it: outputs, inline annotations, and a job summary.
#
# Every path out of this script that is not a completed analysis ends in a non-zero exit and a
# grade F certificate on disk. A pull request with no certificate is indistinguishable from one
# that was never analyzed, so silence is the one unacceptable outcome.

set -euo pipefail

readonly STAGE="${RUNNER_TEMP:-/tmp}/reversibility"
readonly CERT_JSON="$STAGE/certificate.json"

log()   { printf '%s\n' "$*" >&2; }
warn()  { printf '::warning::%s\n' "$*" >&2; }
error() { printf '::error::%s\n' "$*" >&2; }
emit()  { printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"; }

mkdir -p "$STAGE"

# jq ships on every GitHub-hosted runner. A self-hosted one may not have it, and the failure
# without this check is an empty grade read from nowhere — which is the one shape of failure
# that could look like a pass.
if ! command -v jq > /dev/null 2>&1; then
    error 'jq is required and was not found. It is preinstalled on GitHub-hosted runners; a self-hosted runner needs it installed.'
    exit 2
fi

# ------------------------------------------------------------------------------------------
# Inputs
# ------------------------------------------------------------------------------------------

FORMAT="${INPUT_FORMAT:-markdown}"
case "$FORMAT" in
    markdown|json|sarif) ;;
    *) error "format must be markdown, json, or sarif; got '$FORMAT'."; exit 2 ;;
esac

# The certificate the caller asked for keeps the extension of what it is, so an uploaded
# artifact is openable by whatever consumes it.
case "$FORMAT" in
    markdown) CERT="${GITHUB_WORKSPACE:-$PWD}/reversibility-certificate.md" ;;
    json)     CERT="${GITHUB_WORKSPACE:-$PWD}/reversibility-certificate.json" ;;
    sarif)    CERT="${GITHUB_WORKSPACE:-$PWD}/reversibility-certificate.sarif" ;;
esac

# A policy file that was read by nothing would leave a user believing their waivers applied.
# That is a failure in the permissive direction, so it is an error rather than a warning.
if [ -n "${INPUT_CONFIG:-}" ]; then
    error "The 'config' input is not implemented yet and would be ignored. Remove it rather than letting a policy file appear to apply."
    exit 2
fi

# Deprecated aliases. Setting both names for one setting is ambiguous, and resolving it by
# precedence would silently pick one during exactly the upgrade that introduced the mistake.
GATE="${INPUT_GATE:-A}"
if [ -n "${INPUT_MIN_GRADE:-}" ]; then
    if [ "$GATE" != 'A' ]; then
        error "Both 'gate' ($GATE) and the deprecated 'min-grade' (${INPUT_MIN_GRADE}) are set. Remove 'min-grade'."
        exit 2
    fi
    warn "'min-grade' is deprecated; rename it to 'gate'."
    GATE="$INPUT_MIN_GRADE"
fi

case "$GATE" in
    A|B|C|F) ;;
    *) error "gate must be A, B, C, or F; got '$GATE'."; exit 2 ;;
esac

PATHS="${INPUT_PATH:-.}"
if [ -n "${INPUT_INCLUDE:-}" ]; then
    if [ "$PATHS" != '.' ]; then
        error "Both 'path' ($PATHS) and the deprecated 'include' (${INPUT_INCLUDE}) are set. Remove 'include'."
        exit 2
    fi
    warn "'include' is deprecated; rename it to 'path'. Note that it is a git pathspec, so a bare extension glob such as '*.sql' now means 'any .sql at any depth'."
    PATHS="$INPUT_INCLUDE"
fi

FAIL_ON_GATE="${INPUT_FAIL_ON_GATE:-true}"

# Off by default. gate-status already refuses to pass a partially analyzed change, so this only
# decides whether the *job* fails too — which is a choice about the pipeline, not about safety.
COVERAGE_ARGS=''
if [ "${INPUT_REQUIRE_FULL_COVERAGE:-false}" = 'true' ]; then
    COVERAGE_ARGS='--require-full-coverage'
fi

# ------------------------------------------------------------------------------------------
# What to compare against
# ------------------------------------------------------------------------------------------

cd "${GITHUB_WORKSPACE:-$PWD}"

# The workspace belongs to the runner user; git refuses to read a repository it thinks belongs
# to somebody else.
git config --global --add safe.directory "${GITHUB_WORKSPACE:-$PWD}" 2> /dev/null || true

BASE="${INPUT_BASE:-}"
if [ -z "$BASE" ] && [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; then
    # A pull request names its own base commit. A push names the commit it moved from, which
    # is all zeros when the branch is new and there is nothing to compare against.
    BASE="$(jq -r '.pull_request.base.sha // empty' "$GITHUB_EVENT_PATH")"
    if [ -z "$BASE" ]; then
        BEFORE="$(jq -r '.before // empty' "$GITHUB_EVENT_PATH")"
        case "$BEFORE" in
            ''|0000000000000000000000000000000000000000) ;;
            *) BASE="$BEFORE" ;;
        esac
    fi
fi

if [ -z "$BASE" ]; then
    error "No base to compare against. Event '${GITHUB_EVENT_NAME:-unknown}' carries none, so set the 'base' input to the ref this change should be measured against."
    exit 2
fi

# A shallow checkout is the usual reason the base commit is absent. Fetching it costs one round
# trip and turns the most common failure in CI into a successful run.
if ! git cat-file -e "${BASE}^{commit}" 2> /dev/null; then
    log "Base $BASE is not in the local history; fetching it."
    git fetch --no-tags --quiet origin "$BASE" 2> /dev/null || true
fi

# ------------------------------------------------------------------------------------------
# Pathspecs
# ------------------------------------------------------------------------------------------

# Globbing stays off while these are split: '*.sql' must reach git as a pattern rather than be
# expanded by this shell against the workspace first.
set -f
# shellcheck disable=SC2206
SPEC=($PATHS)
for pattern in ${INPUT_EXCLUDE:-}; do
    SPEC+=(":!$pattern")
done
set +f

# ------------------------------------------------------------------------------------------
# Analyze
# ------------------------------------------------------------------------------------------

# The requested format carries the gate, because its exit code is the one the job is graded on.
# Exit codes: 0 met, 1 below the gate, 2 the run did not complete.
set +e
"$REVCTL" check --base "$BASE" --format "$FORMAT" --output "$CERT" --min-grade "$GATE" \
    ${COVERAGE_ARGS} "${SPEC[@]}"
RC=$?
set -e

# A gate must prove it ran. No certificate produced means exit 2, never exit 0 — absence of
# output is never success. This is the post-condition on the whole step, and it holds whatever
# revctl's own exit code said: a zero from a run that wrote nothing is a zero about nothing.
if [ ! -s "$CERT" ]; then
    error 'revctl produced no certificate. This is a broken run, not a passing one.'
    emit 'grade' 'F'
    emit 'gate-status' 'FAIL'
    emit 'findings-count' '0'
    emit 'applicable' 'true'
    emit 'certificate-path' ''
    emit 'markdown-path' ''
    emit 'sarif-path' ''
    exit 2
fi

# JSON is rendered separately to read the verdict back. Re-deriving a grade in shell would be a
# second definition of it, and the second definition is the one that gets it wrong in the
# permissive direction.
GRADE='F'
GATE_STATUS='FAIL'
APPLICABLE='true'
FINDINGS='0'

if [ "$FORMAT" = 'json' ]; then
    cp "$CERT" "$CERT_JSON"
else
    "$REVCTL" check --base "$BASE" --format json --output "$CERT_JSON" "${SPEC[@]}" > /dev/null 2>&1 || true
fi

# The same post-condition applied to the verdict itself. Without this JSON there is a
# certificate on disk but no readable grade, so the outputs would report F/FAIL while the step
# still exited on revctl's code — possibly zero. A step that says FAIL and passes the job has
# not proved it ran; it has proved only that something was written.
if [ ! -s "$CERT_JSON" ]; then
    error 'The certificate could not be read back as JSON, so the verdict cannot be reported. This is a broken run, not a passing one.'
    emit 'grade' 'F'
    emit 'gate-status' 'FAIL'
    emit 'findings-count' '0'
    emit 'applicable' 'true'
    emit 'certificate-path' "$CERT"
    emit 'markdown-path' ''
    emit 'sarif-path' ''
    exit 2
fi

GRADE="$(jq -r '.grade // "F"' "$CERT_JSON")"
GATE_STATUS="$(jq -r '.aiGateStatus // "FAIL"' "$CERT_JSON")"
APPLICABLE="$(jq -r '.applicable // true' "$CERT_JSON")"
FINDINGS="$(jq -r '.findings | length' "$CERT_JSON")"

# Schema 1.5.0 added .outcome. A certificate without one predates it, and the honest default is
# the one that assumes nothing: ANALYZED would claim the run read something, which is exactly the
# claim this field exists to stop being made silently.
OUTCOME="$(jq -r '.outcome // ""' "$CERT_JSON")"

# Coverage defaults to empty rather than FULL for the same reason: claiming a pre-1.5.0
# certificate covered everything is a claim its producer never made.
COVERAGE="$(jq -r '.coverage // ""' "$CERT_JSON")"
UNANALYZED="$(jq -r '.unanalyzedFiles | length' "$CERT_JSON" 2> /dev/null || echo 0)"

# An unreadable grade is UNKNOWN by another name, and UNKNOWN fails. jq emits "null" for a
# missing key rather than failing, so the // defaults above do not catch a malformed document.
#
# N/A is readable and is not a pass: it says the engine did not analyze this changeset. It is
# accepted here so that a docs-only pull request does not read as a broken run — whether it
# *fails* is decided below, from the exit code revctl already returned.
case "$GRADE" in
    A|B|C|F|N/A) ;;
    *)
        error "The certificate carries no readable grade (got '$GRADE'). Unknown means unsafe."
        exit 2
        ;;
esac

# Markdown is what a person reads, so the comment always gets it whatever format was asked for.
MARKDOWN="$CERT"
if [ "$FORMAT" != 'markdown' ]; then
    MARKDOWN="$STAGE/certificate.md"
    "$REVCTL" check --base "$BASE" --format markdown --output "$MARKDOWN" "${SPEC[@]}" > /dev/null 2>&1 || true
fi

SARIF=''
if [ "${INPUT_SARIF_UPLOAD:-false}" = 'true' ]; then
    if [ "$FORMAT" = 'sarif' ]; then
        SARIF="$CERT"
    else
        SARIF="$STAGE/certificate.sarif"
        "$REVCTL" check --base "$BASE" --format sarif --output "$SARIF" "${SPEC[@]}" > /dev/null 2>&1 || true
    fi
    [ -s "$SARIF" ] || SARIF=''
fi

# ------------------------------------------------------------------------------------------
# Report
# ------------------------------------------------------------------------------------------

# Annotations put each finding on the line that caused it, where the reviewer already is.
# Line 0 means the finding is about the file as a whole; GitHub anchors those at the top.
if [ -s "$CERT_JSON" ]; then
    jq -r '
        .findings[]
        | "::error file=\(.file),line=\(if .line > 0 then .line else 1 end),title=\(.ruleId) \(.reversibility)::\(.rationale)"
    ' "$CERT_JSON" 2> /dev/null || true
fi

emit 'grade' "$GRADE"
emit 'gate-status' "$GATE_STATUS"
emit 'findings-count' "$FINDINGS"
emit 'applicable' "$APPLICABLE"
emit 'outcome' "$OUTCOME"
emit 'coverage' "$COVERAGE"
emit 'unanalyzed-count' "$UNANALYZED"
emit 'certificate-path' "$CERT"
emit 'markdown-path' "$MARKDOWN"
emit 'sarif-path' "$SARIF"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -s "$MARKDOWN" ]; then
    cat "$MARKDOWN" >> "$GITHUB_STEP_SUMMARY"
fi

# ------------------------------------------------------------------------------------------
# Verdict
# ------------------------------------------------------------------------------------------

if [ "$RC" -ge 2 ]; then
    error 'The analysis did not complete. This is a broken run, not a passing one.'
    exit "$RC"
fi

if [ "$RC" -eq 1 ]; then
    if [ "$FAIL_ON_GATE" = 'true' ]; then
        error "Grade $GRADE is below the required minimum $GATE. See the certificate on the pull request."
        exit 1
    fi
    log "Grade $GRADE is below $GATE, but fail-on-gate is false; not failing the job."
    exit 0
fi

# RC is 0 from here. That still does not mean the change was measured: NO_CANDIDATES exits 0
# because there was genuinely nothing to read. Saying so in the log is the difference between a
# green check somebody trusts and a green check somebody has to go and interpret.
#
# 'fail-on-gate: false' is deliberately not consulted — nothing is being failed. This branch only
# changes what the log says about a job that is passing either way.
if [ "$OUTCOME" = 'NO_CANDIDATES' ]; then
    log "Nothing in this change is analyzed by this engine, so no reversibility grade was produced. This is not a pass."
    exit 0
fi

# The job passed and the agent gate did not. Saying so is the whole point of the coverage axis:
# without this line the only trace is a field in a JSON file nobody opens on a green build.
if [ "$COVERAGE" = 'PARTIAL' ]; then
    warn "Grade $GRADE meets the minimum $GATE, but $UNANALYZED file(s) were not analyzed. gate-status is $GATE_STATUS: an autonomous agent must not merge this. See the certificate for the list, or set 'require-full-coverage: true' to fail the job here."
    exit 0
fi

log "Grade $GRADE meets the minimum $GATE."
exit 0
