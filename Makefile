GO ?= go
COMPOSE ?= docker compose

.PHONY: deps deps-down api worker dev test test-integration vet eval check build compose-check

deps:
	$(COMPOSE) up -d --wait postgres

deps-down:
	$(COMPOSE) down

api:
	$(GO) run ./cmd/api

worker:
	$(GO) run ./cmd/worker

dev: deps
	@trap 'kill 0' INT TERM EXIT; $(GO) run ./cmd/worker & $(GO) run ./cmd/api

test:
	$(GO) test ./...

test-integration: deps
	TEST_DATABASE_URL=postgres://agent_chat:agent_chat_password@127.0.0.1:5433/agent_chat?sslmode=disable $(GO) test -count=1 ./...

vet:
	$(GO) vet ./...

eval:
	python -m pytest evals/runner

check: test vet eval compose-check

build:
	mkdir -p bin
	$(GO) build -o bin/api ./cmd/api
	$(GO) build -o bin/worker ./cmd/worker

compose-check:
	$(COMPOSE) config --quiet
