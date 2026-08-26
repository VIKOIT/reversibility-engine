#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Decides which revctl build this run needs and where it will live.
#
# Nothing is downloaded here. Separating the decision from the fetch is what lets the cache
# step sit between them, keyed on the answer.

set -euo pipefail

emit() { printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"; }
fail() { printf '::error::%s\n' "$*" >&2; exit 1; }

# ------------------------------------------------------------------------------------------
# Platform
# ------------------------------------------------------------------------------------------

case "${RUNNER_OS_NAME:-}" in
    Linux)   os='linux'   ;;
    macOS)   os='darwin'  ;;
    Windows) os='windows' ;;
    *) fail "Unsupported runner OS '${RUNNER_OS_NAME:-}'. This action runs on Linux, macOS, and Windows." ;;
esac

case "${RUNNER_ARCH_NAME:-}" in
    X64)   arch='amd64' ;;
    ARM64) arch='arm64' ;;
    *) fail "Unsupported runner architecture '${RUNNER_ARCH_NAME:-}'. Released builds are amd64 and arm64." ;;
esac

# Windows builds are amd64 only. Saying so here beats a 404 from the download step, which
# reads as a broken release rather than an unsupported combination.
if [ "$os" = 'windows' ] && [ "$arch" != 'amd64' ]; then
    fail 'Windows runners are supported on amd64 only.'
fi

if [ "$os" = 'windows' ]; then
    archive_ext='zip'
    binary_name='revctl.exe'
else
    archive_ext='tar.gz'
    binary_name='revctl'
fi

asset="revctl_${os}_${arch}.${archive_ext}"

# ------------------------------------------------------------------------------------------
# An externally supplied binary short-circuits everything
# ------------------------------------------------------------------------------------------

if [ -n "${INPUT_BINARY:-}" ]; then
    if [ ! -x "$INPUT_BINARY" ]; then
        fail "The binary input points at '$INPUT_BINARY', which is not an executable file."
    fi
    printf 'Using the supplied binary %s; no download.\n' "$INPUT_BINARY"
    emit 'needs-download' 'false'
    emit 'revctl' "$INPUT_BINARY"
    emit 'asset' "$asset"
    emit 'cache-key' ''
    emit 'install-dir' ''
    emit 'download-url' ''
    emit 'checksums-url' ''
    exit 0
fi

# ------------------------------------------------------------------------------------------
# Version and source
# ------------------------------------------------------------------------------------------

# The action's own ref is the default version, so `uses: ...@v1.2.0` runs the v1.2.0 binary
# and the two cannot drift. A branch or SHA ref names no release, so those fall back to the
# latest one rather than guessing a tag that may not exist.
version="${INPUT_VERSION:-}"
if [ -z "$version" ]; then
    case "${ACTION_REF:-}" in
        v[0-9]*) version="$ACTION_REF" ;;
        *)       version='latest' ;;
    esac
fi

# A moving major tag has no release of its own.
#
# `uses: ...@v1` is the documented way to consume this action, and it resolves ACTION_REF to
# "v1" — but releases are cut as v1.1.0 and the v1 tag is only repointed at them afterwards.
# Downloading from releases/download/v1/ therefore 404s on the tag everybody actually uses,
# while a full pin like @v1.2.0 works. Major-only refs resolve to the newest release instead.
#
# The caveat, stated rather than hidden: should a second major line ever be maintained
# alongside this one, @v1 would resolve to a release from the newer line. Only v1 exists, and
# a build that must be reproducible across such a change should pin a full version, which is
# what `version` is for.
case "$version" in
    v[0-9]) version='latest' ;;
    v[0-9][0-9]) version='latest' ;;
    v[0-9][0-9][0-9]) version='latest' ;;
esac

case "$version" in
    latest)  ;;
    v[0-9]*) ;;
    [0-9]*)  version="v$version" ;;
    *) fail "The version input must be a release tag such as v1.2.0, or 'latest'; got '$version'." ;;
esac

# action_repository is empty when the action is referenced by path (`uses: ./`), which is how
# this repository tests it against itself.
repository="${ACTION_REPOSITORY:-}"
if [ -z "$repository" ]; then
    repository="${FALLBACK_REPOSITORY:-}"
fi
if [ -z "$repository" ]; then
    fail 'Could not determine which repository to download the release from.'
fi

server="${GITHUB_SERVER_URL:-https://github.com}"
if [ "$version" = 'latest' ]; then
    base_url="$server/$repository/releases/latest/download"
else
    base_url="$server/$repository/releases/download/$version"
fi

install_dir="${RUNNER_TOOL_CACHE:-${RUNNER_TEMP:-/tmp}}/reversibility-engine/${version}/${os}-${arch}"

emit 'needs-download' 'true'
emit 'asset' "$asset"

# The version is part of the key as well as the asset: an asset name carries no version, so a
# key without one would let a cache entry from an older release satisfy a newer request.
#
# 'latest' is deliberately not cached. A cache key is immutable once written, so a key
# containing the word "latest" would pin the first binary it ever saw and keep serving it long
# after a newer release existed — the opposite of what the word promises. Pin a version to get
# the cache.
if [ "$version" = 'latest' ]; then
    emit 'cache-key' ''
else
    emit 'cache-key' "reversibility-engine-${version}-${asset}"
fi
emit 'install-dir' "$install_dir"
emit 'revctl' "$install_dir/$binary_name"
emit 'download-url' "$base_url/$asset"
emit 'checksums-url' "$base_url/checksums.txt"

printf 'revctl %s for %s/%s\n' "$version" "$os" "$arch"
