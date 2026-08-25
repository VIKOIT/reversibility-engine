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

# git is what revctl shells out to in order to resolve --base into a changeset. ca-certificates
# is needed by nothing here today and is kept because a TLS-less base image fails obscurely the
# first time anything reaches the network.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/revctl /usr/local/bin/revctl

# No USER directive. A mounted workspace is owned by the invoking user, and dropping privileges
# here would leave the certificate unwritable.
#
# The entrypoint is the CLI itself. Until v2 this image was also the GitHub Action, and its
# entrypoint was a script that reconstructed the changeset and posted the comment; the action
# is now a composite that downloads a released binary, so that script has no caller and is
# gone. What remains is the way to run revctl without installing Go — which is what the README
# documents this image for.
ENTRYPOINT ["/usr/local/bin/revctl"]
