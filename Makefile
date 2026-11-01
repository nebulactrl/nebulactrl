SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c
.DEFAULT_GOAL := help

COMPOSE      := docker compose $(if $(wildcard .env),--env-file .env) -f deploy/dev/docker-compose.yml
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -s -w -X main.version=$(VERSION)
OAPI_CODEGEN_VERSION ?= v2.8.0
GOLANGCI_LINT_VERSION ?= v2.15.0
AGENT_ARCHES := amd64 arm64

# Source .env (if present) so tests see NEBULACTRL_TEST_DATABASE_URL etc.
LOAD_ENV := set -a; if [ -f .env ]; then . ./.env; fi; set +a;

.PHONY: help setup keygen dev-deps dev dev-agent dev-down dev-reset generate test test-db \
        test-smoke test-full lint lint-go lint-dead lint-workflows test-release-rules build build-web build-agents \
        release-binaries e2e site-dev site-build

help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  \033[1m%-18s\033[0m %s\n", $$1, $$2}'

setup: ## Check tools, create .env with a fresh secret key, install web deps
	@missing=0; \
	for t in go bun docker sqlc openssl; do \
	  if command -v $$t >/dev/null 2>&1; then echo "ok       $$t"; else echo "MISSING  $$t"; missing=1; fi; \
	done; \
	[ $$missing -eq 0 ] || { echo "install the missing tools (see CONTRIBUTING.md)"; exit 1; }
	@go version | grep -qE 'go1\.(2[7-9]|[3-9][0-9])' || { echo "Go 1.27+ required, found: $$(go version)"; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "Docker daemon is not reachable (start Docker Desktop / dockerd)"; exit 1; }
	@if [ -f .env ]; then echo ".env already exists, leaving it alone"; else \
	  key=$$(openssl rand -base64 32); \
	  sed "s|^NEBULACTRL_SECRET_KEY=.*|NEBULACTRL_SECRET_KEY=$$key|" .env.example > .env; \
	  echo "created .env with a new NEBULACTRL_SECRET_KEY"; fi
	bun install

keygen: ## Print a new NEBULACTRL_SECRET_KEY value
	@go run ./cmd/server keygen

dev-deps: ## Start Postgres for development (docker compose)
	$(COMPOSE) up -d --wait postgres

dev: dev-deps ## Run control plane (:8080) and web dev server (:5173) together
	@deploy/dev/dev.sh

dev-agent: ## Run a dev agent in Docker: make dev-agent TOKEN=<enrollment token>
	@[ -n "$(TOKEN)" ] || echo "note: TOKEN not set; fine only if agent1-state already holds an enrolled identity"
	@docker network inspect nebulactrl-edge >/dev/null 2>&1 || docker network create nebulactrl-edge
	NEBULACTRL_ENROLL_TOKEN="$(TOKEN)" $(COMPOSE) --profile agent up --build agent1

dev-down: ## Stop all dev containers (keeps volumes)
	$(COMPOSE) --profile agent --profile multi down

dev-reset: ## Stop dev containers AND delete their volumes (database, agent identity)
	$(COMPOSE) --profile agent --profile multi down -v

generate: ## sqlc, OpenAPI spec, web API client, Go API client, CLI reference docs
	sqlc generate
	go generate ./deploy/pages
	go run ./cmd/server openapi
	cd apps/web && bun run gen:api
	@if [ -f internal/apiclient/oapi-codegen.yaml ]; then \
	  echo "oapi-codegen $(OAPI_CODEGEN_VERSION)"; \
	  go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
	    -config internal/apiclient/oapi-codegen.yaml openapi/openapi.json; \
	else echo "skip oapi-codegen: internal/apiclient/oapi-codegen.yaml not present yet"; fi
	go run ./cmd/cli docs --out docs/reference/cli.md

test: ## Tier 1: Go unit + DB tests and web unit tests (every change, 1-2 min)
	@$(LOAD_ENV) go test ./...
	cd apps/web && bun run test
	cd packages/ascii && bun run test
	cd packages/dither && bun run test
	cd packages/docs && bun run test
	cd packages/tokens && bun run test
	cd apps/site && bun run test

test-db: dev-deps
	@$(COMPOSE) exec -T postgres psql -U nebulactrl -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='nebulactrl_test'" | grep -q 1 \
	  || $(COMPOSE) exec -T postgres createdb -U nebulactrl nebulactrl_test

test-smoke: test-db ## Tier 2: ~8 core Docker scenarios, shared control plane + agent (<= 4 min; pauses dev agent1, see tests/README.md)
	@./tests/run.sh smoke

test-full: test-db ## Tier 3: every Docker scenario incl. templates sample, Railpack, backups, reconnect (pauses dev agent1; NEBULACTRL_TEST_MULTI=1 adds multi-server)
	@./tests/run.sh full

lint: lint-go lint-dead ## golangci-lint (includes go vet), deadcode, knip and biome
	cd apps/web && bun run lint
	cd packages/ascii && bun run lint
	cd packages/dither && bun run lint
	cd packages/docs && bun run lint
	cd packages/tokens && bun run lint
	cd apps/site && bun run lint && bun run typecheck

build-web:
	bun install --frozen-lockfile && cd apps/web && VITE_APP_VERSION=$(VERSION) VITE_SITE_URL=$(SITE_URL) VITE_GITHUB_URL=$(GITHUB_URL) bun run build

build-agents: ## Static Linux agents (amd64, arm64) into bin/agents for the install script
	@mkdir -p bin/agents
	@for a in $(AGENT_ARCHES); do \
	  echo "bin/agents/nebulactrl-agent-linux-$$a"; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agents/nebulactrl-agent-linux-$$a ./cmd/agent || exit 1; \
	done

release-binaries: ## Release archives and SHA256SUMS into dist/ (CLI and agent, linux/darwin): make release-binaries VERSION=0.2.0
	@case "$(VERSION)" in [0-9]*.[0-9]*.[0-9]*|v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "VERSION=x.y.z is required, e.g. make release-binaries VERSION=0.2.0"; exit 1 ;; esac
	scripts/release/build-binaries.sh "$(VERSION)" dist

lint-go: ## golangci-lint v2 (go vet, errcheck, staticcheck, gosec, ...) over all Go code, including the integration tests
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

lint-dead: ## Dead code: unreachable Go functions (deadcode), unused files/exports/dependencies of the Bun workspace (knip)
	scripts/ci/deadcode.sh
	scripts/ci/knip.sh

lint-workflows: ## Workflow rules (allowed triggers, GitHub-hosted runners, SHA-pinned actions, timeouts) and actionlint
	scripts/release/check-workflows.sh

test-release-rules: ## Version bumps of release.config.mjs (0.x rules), semantic-release --dry-run in a scratch repo
	scripts/release/test-release-rules.sh

build: build-web ## Web, then server (web embedded), agent and CLI into bin/
	@mkdir -p internal/webui/dist
	@find internal/webui/dist -mindepth 1 ! -name .keep -delete
	cp -R apps/web/dist/. internal/webui/dist/
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nebulactrl-server ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nebulactrl-agent ./cmd/agent
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nebulactrl ./cmd/cli

site-dev: ## Marketing site dev server (apps/site; SITE_URL defaults to http://localhost:5174)
	cd apps/site && bun run dev

site-build: ## Marketing site into apps/site/dist (needs SITE_URL=https://... and Go for the installer; optional GITHUB_URL)
	@[ -n "$(SITE_URL)" ] || { echo "SITE_URL is required, e.g. make site-build SITE_URL=https://nebulactrl.example"; exit 1; }
	cd apps/site && SITE_URL="$(SITE_URL)" bun run build

e2e: ## Playwright smoke test (needs `make dev` + agent1 running; see .env.example)
	@$(LOAD_ENV) cd apps/web && bunx playwright test
