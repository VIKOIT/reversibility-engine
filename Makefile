# Reversibility Engine — developer entrypoints.
#
# CGO_ENABLED=1 is mandatory and non-negotiable: the Postgres analyzer links the real
# PostgreSQL parser via pganalyze/pg_query_go. A CGO_ENABLED=0 build would silently lose
# the parser, and this project must never trade analysis depth for a green build.
# See CLAUDE.md §9 and ADR/0001-parser-choice.md.

SHELL := /bin/sh
.DEFAULT_GOAL := help

export CGO_ENABLED := 1

GO             ?= go
GOLANGCI_LINT  ?= golangci-lint
BIN_DIR        := bin
COVER_PROFILE  := coverage.out
COVER_MIN      ?= 85
COVER_PACKAGES ?= ./internal/analyzer/... ./internal/engine/...
FUZZ_TIME      ?= 30s
FUZZ_TARGETS   ?= ./internal/analyzer/postgres ./internal/analyzer/postgres/parser ./internal/analyzer/kubernetes ./internal/engine ./internal/delivery/github

# Binaries get .exe on Windows so `make build` is usable on the primary dev machine.
ifeq ($(OS),Windows_NT)
EXT := .exe
else
EXT :=
endif

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build revctl and revsrv into ./bin
	$(GO) build -o $(BIN_DIR)/revctl$(EXT) ./cmd/revctl
	$(GO) build -o $(BIN_DIR)/revsrv$(EXT) ./cmd/revsrv

.PHONY: test
test: ## Run all tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run

.PHONY: fmt
fmt: ## Format all Go source
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Tidy and verify the module graph
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: fuzz
fuzz: ## Fuzz every target in turn (FUZZ_TIME=30s each by default)
	@sh scripts/fuzz.sh $(FUZZ_TIME) $(FUZZ_TARGETS)

.PHONY: cover
cover: ## Produce a coverage profile and enforce the COVER_MIN gate
	$(GO) test -race -count=1 -covermode=atomic -coverprofile=$(COVER_PROFILE) $(COVER_PACKAGES)
	@sh scripts/coverage.sh $(COVER_PROFILE) $(COVER_MIN)

.PHONY: cover-html
cover-html: cover ## Open the coverage profile in a browser
	$(GO) tool cover -html=$(COVER_PROFILE)

.PHONY: verify
verify: build vet lint test cover ## Everything CI runs, locally

.PHONY: run-cli
run-cli: ## Run the CLI (make run-cli ARGS="check ./migrations")
	$(GO) run ./cmd/revctl $(ARGS)

.PHONY: run-server
run-server: ## Run the GitHub App webhook server
	$(GO) run ./cmd/revsrv $(ARGS)

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) $(COVER_PROFILE)
