# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Abdul Ghani (VIKOIT)

# ---------------------------------------------------------------------------------------
# Build stage
#
# CGO_ENABLED=1 is the whole reason this action ships as a container. The Postgres analyzer
# links the real PostgreSQL parser through pg_query_go, so a CGO_ENABLED=0 build would drop
# the analyzer and still exit 0 — a silent downgrade to "no SQL was analyzed". See ADR/0001.
# ---------------------------------------------------------------------------------------
FROM golang:1.22-bookworm AS build

ENV CGO_ENABLED=1

WORKDIR /src

# Dependencies are copied first so the module cache layer survives edits to the source.
# pg_query_go compiles a large amount of C, which makes that layer worth keeping.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath keeps build paths out of the binary so two builds of the same commit agree.
# Symbols are stripped because nothing here is debugged from inside the container.
RUN go build -trimpath -ldflags='-s -w' -o /out/revctl ./cmd/revctl

# A binary that lost its analyzer is the failure this file exists to prevent, so the build
# refuses to produce an image that cannot parse SQL.
RUN printf 'DROP TABLE t;\n' > /tmp/probe.sql \
    && /out/revctl check /tmp/probe.sql --format json | grep -q '"PG001"' \
    || (echo 'FATAL: revctl did not classify DROP TABLE as PG001 — cgo parser missing?' >&2; exit 1)

# ---------------------------------------------------------------------------------------
# Runtime stage
#
# debian:bookworm-slim, not alpine and not scratch. The binary is dynamically linked against
# the glibc of golang:1.22-bookworm; alpine's musl would fail to load it, and scratch has no
# libc at all. The two stages must stay on the same Debian release for that reason — bumping
# one without the other produces a GLIBC_2.xx "not found" at runtime, not at build time.
# ---------------------------------------------------------------------------------------
FROM debian:bookworm-slim

# git      reconstructs the base tree; curl and jq talk to the REST API.
# The GitHub CLI is deliberately absent: it is not present in an action container by default,
# and installing it means trusting an apt repository or an unpinned release tarball to do what
# twenty lines of curl and jq already do.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/revctl /usr/local/bin/revctl
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# No USER directive. GitHub mounts the workspace into the container as root, and dropping
# privileges here would leave the certificate unwritable.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
