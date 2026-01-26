POSTGRES_LOCAL_DSN ?= postgres://postgres:postgres@localhost:5432/lifeops?sslmode=disable

.PHONY: up down migrate test test-integration worker-once seed-sample ingest-finance openapi-generate wait-for-postgres

up:
	docker-compose up -d --build
	@$(MAKE) wait-for-postgres
	@$(MAKE) migrate

down:
	docker-compose down

migrate:
	@echo "Running DB migrations..."
	POSTGRES_DSN=$(POSTGRES_LOCAL_DSN) go run ./cmd/migrate -cmd up

wait-for-postgres:
	@echo "Waiting for Postgres to accept connections..."
	@until docker-compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do \
		sleep 1; \
	done

test:
	go test ./...

test-integration:
	go test ./... -tags=integration

worker-once:
	set -a; . ./.env; set +a; POSTGRES_DSN=$(POSTGRES_LOCAL_DSN) TELEGRAM_AGENT_CONFIG_DIR=./data/telegram_agents go run ./cmd/lifeops worker-once

seed-sample:
	set -a; . ./.env; set +a; POSTGRES_DSN=$(POSTGRES_LOCAL_DSN) TELEGRAM_AGENT_CONFIG_DIR=./data/telegram_agents go run ./cmd/lifeops seed-sample

ingest-finance:
	go run ./cmd/lifeops ingest-finance

openapi-generate:
	cp openapi.yaml internal/api/openapi.yaml
