# SubAI gateway task entry (§15: lint、test、test-integration、build、migrate、dev).

SHELL := /bin/bash
TEST_DB_URL ?= postgres://subai:subai@localhost:54329/subai?sslmode=disable
TEST_PG_CONTAINER := subai-test-pg

.PHONY: help lint test test-integration build migrate dev dev-web compose-up compose-down clean test-pg-up test-pg-down

help:
	@echo "lint              - go vet + gofmt check"
	@echo "test              - unit tests (no database required)"
	@echo "test-integration  - integration tests (starts a dockerized PostgreSQL)"
	@echo "build             - build server binary and admin UI"
	@echo "migrate           - apply migrations (SUBAI_DATABASE_URL)"
	@echo "dev               - run server locally (needs SUBAI_MASTER_KEY and PostgreSQL)"
	@echo "dev-web           - run admin UI dev server with API proxy"

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
	CGO_ENABLED=0 go build -o bin/subai-server ./cmd/server
	cd web && npm ci --no-audit --no-fund && npm run build

migrate:
	SUBAI_MASTER_KEY=$${SUBAI_MASTER_KEY:?set SUBAI_MASTER_KEY} \
	go run ./cmd/server --migrate-only

dev:
	SUBAI_MASTER_KEY=$${SUBAI_MASTER_KEY:?export SUBAI_MASTER_KEY (openssl rand -hex 32)} \
	SUBAI_DATABASE_URL=$${SUBAI_DATABASE_URL:-postgres://subai:subai@localhost:5432/subai?sslmode=disable} \
	SUBAI_DEV_BOOTSTRAP_ADMIN=$${SUBAI_DEV_BOOTSTRAP_ADMIN:-} \
	go run ./cmd/server

dev-web:
	cd web && npm run dev

compose-up:
	cd deploy && docker compose up -d --build

compose-down:
	cd deploy && docker compose down
