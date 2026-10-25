# Smart Campaign Composer — developer commands. Run `make help` for a list.
# None of these targets need ANTHROPIC_API_KEY.

SHELL := /bin/bash
.DEFAULT_GOAL := help

GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_LINT := bin/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOVULNCHECK_VERSION := v1.9.0
GOVULNCHECK := bin/govulncheck-$(GOVULNCHECK_VERSION)
# Build the linter with the toolchain pinned in go.mod so it understands
# the project's Go version.
GO_TOOLCHAIN := $(shell sed -n 's/^toolchain //p' go.mod)

FRONTEND_DEPS := frontend/node_modules/.package-lock.json

# Explicit package roots: `./...` would also pick up stray .go files that
# npm packages ship inside frontend/node_modules.
GO_PKGS := ./cmd/... ./internal/... ./mock-provider/...

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## --- Stack -------------------------------------------------------------

.PHONY: up
up: ## Build and start the full stack (requires .env)
	@test -f .env || { echo "Missing .env: run 'cp .env.example .env' first"; exit 1; }
	docker compose up --build -d
	docker compose ps

.PHONY: down
down: ## Stop the stack (keeps volumes)
	docker compose down

## --- Tests -------------------------------------------------------------

# Host ports of the running stack; match API_PORT and MOCK_PROVIDER_PORT
# in .env if you changed them (make smoke API_PORT=...).
API_PORT ?= 8080
MOCK_PROVIDER_PORT ?= 9090

.PHONY: smoke
smoke: ## Run the example campaign end to end against the running stack
	go run ./cmd/smoke -api http://127.0.0.1:$(API_PORT) -provider http://127.0.0.1:$(MOCK_PROVIDER_PORT)

.PHONY: test
test: test-go test-frontend ## Run all tests (Docker required for integration tests)

.PHONY: test-unit
test-unit: test-go-unit test-frontend ## Run tests that need no containers

.PHONY: test-go-unit
test-go-unit: ## Go unit tests with the race detector
	go test -race -count=1 $(GO_PKGS)

.PHONY: test-go
test-go: ## All Go tests, unit + integration (testcontainers; Docker required)
	go test -race -count=1 -tags=integration $(GO_PKGS)

.PHONY: test-frontend
test-frontend: $(FRONTEND_DEPS) ## Frontend unit tests (Vitest)
	cd frontend && npm test

# Fuzz targets as package:Name; `make test` runs their seed corpora.
FUZZ_TARGETS := ./internal/segment:FuzzParse \
	./internal/content:FuzzParseTemplate ./internal/content:FuzzRenderMessage \
	./internal/campaign:FuzzDecodeSegmentOutput ./internal/campaign:FuzzDecodeDrafts
FUZZTIME ?= 30s

.PHONY: fuzz
fuzz: ## Fuzz each target for FUZZTIME (default 30s)
	@set -e; for t in $(FUZZ_TARGETS); do \
		echo "--- $${t#*:}"; go test -run '^$$' -fuzz "^$${t#*:}$$" -fuzztime $(FUZZTIME) "$${t%%:*}"; \
	done

## --- Quality -----------------------------------------------------------

.PHONY: lint
lint: lint-go lint-frontend ## Run all linters and type checks

.PHONY: lint-go
lint-go: $(GOLANGCI_LINT) ## golangci-lint (includes go vet)
	$(GOLANGCI_LINT) run $(GO_PKGS)

.PHONY: lint-frontend
lint-frontend: $(FRONTEND_DEPS) ## ESLint and vue-tsc
	cd frontend && npm run lint && npm run typecheck

.PHONY: vuln
vuln: $(GOVULNCHECK) ## Report known vulnerabilities in reachable Go code (network)
	$(GOVULNCHECK) $(GO_PKGS)

.PHONY: fmt
fmt: $(GOLANGCI_LINT) ## Format Go code (gofmt + goimports)
	$(GOLANGCI_LINT) fmt $(GO_PKGS)

.PHONY: fmt-check
fmt-check: $(GOLANGCI_LINT) ## Fail if Go code is not formatted (prints the diff)
	$(GOLANGCI_LINT) fmt --diff $(GO_PKGS)

## --- Tooling -----------------------------------------------------------

$(GOLANGCI_LINT):
	@mkdir -p bin
	GOBIN=$(CURDIR)/bin/.tmp GOTOOLCHAIN=$(GO_TOOLCHAIN) \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv bin/.tmp/golangci-lint $@ && rmdir bin/.tmp

$(GOVULNCHECK):
	@mkdir -p bin
	GOBIN=$(CURDIR)/bin/.tmp GOTOOLCHAIN=$(GO_TOOLCHAIN) \
		go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	mv bin/.tmp/govulncheck $@ && rmdir bin/.tmp

$(FRONTEND_DEPS): frontend/package-lock.json
	cd frontend && npm ci
