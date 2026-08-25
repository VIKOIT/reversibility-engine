#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)
#
# Downloads revctl and refuses to run anything whose checksum does not match the release.
#
# The binary decides whether changes merge. Running one that arrived over a hijacked
# connection, or a truncated download that happens to still execute, would put an unverified
# program in charge of that decision.

set -euo pipefail

fail() { printf '::error::%s\n' "$*" >&2; exit 1; }

if [ -x "$REVCTL" ]; then
    printf 'Cached: %s\n' "$REVCTL"
    "$REVCTL" version || true
    exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$INSTALL_DIR"

# --location follows the redirect to the asset storage host; --fail turns an HTTP error into a
# non-zero exit instead of a file containing an error page.
download() {
    curl --silent --show-error --fail --location --retry 3 --retry-delay 2 \
        --output "$2" "$1"
}

printf 'Downloading %s\n' "$DOWNLOAD_URL"
if ! download "$DOWNLOAD_URL" "$work/$ASSET"; then
    fail "Could not download $DOWNLOAD_URL. If this pins a version, check that the release exists and publishes an asset for this platform."
fi

if ! download "$CHECKSUMS_URL" "$work/checksums.txt"; then
    fail "Could not download $CHECKSUMS_URL. The release must publish checksums.txt; without it the binary cannot be verified, and an unverified binary is not run."
fi

expected="$(awk -v want="$ASSET" '$2 == want || $2 == "*" want { print $1 }' "$work/checksums.txt" | head -n 1)"
if [ -z "$expected" ]; then
    fail "checksums.txt does not list $ASSET, so there is nothing to verify the download against."
fi

# sha256sum on Linux and on the Windows runners' git-bash; shasum on macOS.
if command -v sha256sum > /dev/null 2>&1; then
    actual="$(sha256sum "$work/$ASSET" | awk '{print $1}')"
elif command -v shasum > /dev/null 2>&1; then
    actual="$(shasum -a 256 "$work/$ASSET" | awk '{print $1}')"
else
    fail 'Neither sha256sum nor shasum is available, so the download cannot be verified.'
fi

if [ "$actual" != "$expected" ]; then
    fail "Checksum mismatch for $ASSET. Expected $expected, got $actual. The download was not what the release published; nothing has been installed."
fi

printf 'Checksum verified: %s\n' "$expected"

case "$ASSET" in
    *.zip)    unzip -q -o "$work/$ASSET" -d "$INSTALL_DIR" ;;
    *.tar.gz) tar -xzf "$work/$ASSET" -C "$INSTALL_DIR" ;;
    *) fail "Unrecognised archive format for $ASSET." ;;
esac

if [ ! -f "$REVCTL" ]; then
    fail "$ASSET did not contain $(basename "$REVCTL")."
fi
chmod +x "$REVCTL"

printf 'Installed: %s\n' "$REVCTL"
"$REVCTL" version
