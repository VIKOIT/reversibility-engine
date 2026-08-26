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
# The entrypoint is the CLI itself. Through v1.0.x this image was also the GitHub Action, and
# its entrypoint was a script that reconstructed the changeset and posted the comment; from
# v1.1.0 the action is a composite that downloads a released binary, so that script has no
# caller and is gone. What remains is the way to run revctl without installing Go — which is
# what the README documents this image for.
#
# Changing this line silently changed what every caller that did not name an entrypoint was
# running, and one of those callers was the frozen v1.0.x Docker action. Two things now stand
# between that and a repeat, and neither is a convention:
#
#   1. revctl with no arguments exits 2. A caller that loses its arguments fails closed rather
#      than reporting a pass over no analysis. See internal/delivery/cli/cli.go.
#   2. publish-image.yml refuses to publish an image whose no-argument invocation exits 0, and
#      names --entrypoint explicitly everywhere it runs this image.
#
# Do not add a CMD that supplies a default subcommand. It would give this image a plausible
# no-argument success again and undo both of the above.
ENTRYPOINT ["/usr/local/bin/revctl"]
