# SubAI gateway task entry (§15: lint、test、test-integration、build、migrate、dev).

SHELL := /bin/bash
TEST_DB_URL ?= postgres://subai:subai@localhost:54329/subai?sslmode=disable
TEST_PG_CONTAINER := subai-test-pg
APP_VERSION := $(shell tr -d '\r\n' < VERSION)
VCS_REF := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo local)$(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo -dirty)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_LDFLAGS := -X subai/internal/buildinfo.Version=$(APP_VERSION) -X subai/internal/buildinfo.Revision=$(VCS_REF) -X subai/internal/buildinfo.BuiltAt=$(BUILD_TIME)

.PHONY: help lint test test-integration build migrate dev dev-web compose-up compose-down deploy pull-update clean test-pg-up test-pg-down

# One-shot release: commit/push first, wait for Actions to finish, then run
# `make deploy`. Runs CI remotely and updates Oracle3 when the image is ready.
deploy:
	@test -z "$$(git status --porcelain)" || (echo "working tree not clean; commit first" && exit 1)
	git push origin main
	$(MAKE) ci-watch
	$(MAKE) pull-update

# Pull the newest Docker Hub image on Oracle3 (use after CI has already published).
pull-update:
	@sha=$$(git rev-parse HEAD); \
	ssh oracle3 "cd /opt/1panel/docker/compose/subai && docker compose pull server && docker compose up -d && printf '%s\\n' '$$sha' > DEPLOYED_COMMIT && docker ps --filter name=subai-server --format '{{.Image}} {{.Status}}'"

# Watch the latest CI run for the current commit until it completes.
ci-watch:
	@sha=$$(git rev-parse HEAD); run=""; \
	for attempt in $$(seq 1 30); do \
		run=$$(gh run list --repo allen0039/subai --commit "$$sha" --limit 1 --json databaseId -q '.[0].databaseId'); \
		[ -n "$$run" ] && break; \
		sleep 2; \
	done; \
	[ -n "$$run" ] || (echo "no GitHub Actions run found for $$sha" && exit 1); \
	gh run watch "$$run" --repo allen0039/subai --exit-status

help:
	@echo "lint              - go vet + gofmt check"
	@echo "test              - unit tests (no database required)"
	@echo "test-integration  - integration tests (starts a dockerized PostgreSQL)"
	@echo "build             - build server binary and admin UI"
	@echo "migrate           - apply migrations (SUBAI_DATABASE_URL)"
	@echo "dev               - run server locally (needs SUBAI_MASTER_KEY and PostgreSQL)"
	@echo "compose-up        - local compose dev stack"
	@echo "deploy            - push to main, wait for CI image, update Oracle3"
	@echo "pull-update       - pull newest image on Oracle3 (after CI done)"

lint:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal rules tests)" || (gofmt -l cmd internal rules tests && echo "run gofmt -w" && exit 1)

test:
	go test ./internal/... ./rules/... -count=1

test-pg-up:
	@docker inspect $(TEST_PG_CONTAINER) >/dev/null 2>&1 || \
	docker run -d --name $(TEST_PG_CONTAINER) -e POSTGRES_USER=subai -e POSTGRES_PASSWORD=subai -e POSTGRES_DB=subai -p 54329:5432 postgres:16
	@sleep 4

test-pg-down:
	-docker rm -f $(TEST_PG_CONTAINER)

test-integration: test-pg-up
	TEST_DATABASE_URL="$(TEST_DB_URL)" go test ./tests/integration/ -count=1 -timeout 300s

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(BUILD_LDFLAGS)" -o bin/subai-server ./cmd/server
	cd web && npm ci --no-audit --no-fund && npm run build

migrate:
	SUBAI_MASTER_KEY=$${SUBAI_MASTER_KEY:?set SUBAI_MASTER_KEY} \
	go run ./cmd/server --migrate-only

dev:
	SUBAI_MASTER_KEY=$${SUBAI_MASTER_KEY:?export SUBAI_MASTER_KEY (openssl rand -hex 32)} \
	SUBAI_DATABASE_URL=$${SUBAI_DATABASE_URL:-postgres://subai:subai@localhost:5432/subai?sslmode=disable} \
	SUBAI_DEV_BOOTSTRAP_ADMIN=$${SUBAI_DEV_BOOTSTRAP_ADMIN:-} \
	go run -ldflags "$(BUILD_LDFLAGS)" ./cmd/server

dev-web:
	cd web && npm run dev

compose-up:
	cd deploy && docker compose up -d --build

compose-down:
	cd deploy && docker compose down
