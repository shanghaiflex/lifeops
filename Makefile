.PHONY: up down migrate test test-integration worker-once ingest-finance openapi-generate

up:
	docker compose up -d --build

down:
	docker compose down

migrate:
	go run ./cmd/migrate -cmd up

test:
	go test ./...

test-integration:
	go test ./... -tags=integration

worker-once:
	go run ./cmd/lifeops worker-once

ingest-finance:
	go run ./cmd/lifeops ingest-finance

openapi-generate:
	cp openapi.yaml internal/api/openapi.yaml
