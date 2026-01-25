# LifeOps MVP Backend

Local MVP backend for personal life analytics (Health Bridge ingest + finance CSV + Telegram digest).

## Features
- LAN-only OpenAPI ingest endpoints for workouts, sleep, and metrics.
- CSV finance ingestion from `./data/finance`.
- Daily Telegram digest via LLM provider (OpenAI or mock).
- Telegram bot with modes `/coach`, `/sleep`, `/finance` and chat history.

## Requirements
- Docker + Docker Compose
- Go 1.22+ (for local dev and tests)

## Setup

### 1) Configure environment
Copy `.env.example` to `.env` and fill values:

```bash
cp .env.example .env
```

Minimum variables:
- `POSTGRES_DSN`
- `API_KEY` (for `X-Api-Key` header)
- `TELEGRAM_BOT_TOKEN`
- `LLM_PROVIDER=mock|openai`
- `OPENAI_API_KEY` (required if `LLM_PROVIDER=openai`)
- `OPENAI_MODEL` (default `gpt-5-mini`)
- `TIMEZONE`

### 2) Start services

```bash
make up
```

### 3) Run migrations

```bash
make migrate
```

## LAN iOS connection
- API listens on `0.0.0.0:8080` by default (change with `BIND_ADDR`).
- Set Health Bridge iOS app to send data to `http://<mac-ip>:8080`.
- Provide header `X-Api-Key: <API_KEY>`.

## Finance CSV ingestion
- Place CSV files in `./data/finance` (mounted into the container).
- Run:

```bash
make ingest-finance
```

Headers are matched by best-effort (date, amount, currency, description, category, merchant, type).

## Daily worker
Run once:

```bash
make worker-once
```

Run continuously via Docker Compose (service `worker`).

## Telegram bot
Run via Docker Compose (`bot` service) or locally:

```bash
go run ./cmd/lifeops bot
```

Commands:
- `/start`
- `/whoami`
- `/daily`
- `/coach` / `/sleep` / `/finance`

## OpenAI provider
Set:

```bash
LLM_PROVIDER=openai
OPENAI_API_KEY=...
OPENAI_MODEL=gpt-5-mini
```

Mock provider:

```bash
LLM_PROVIDER=mock
```

## Tests
Unit tests:

```bash
make test
```

Integration tests (starts Postgres via testcontainers):

```bash
make test-integration
```

## OpenAPI contract
The OpenAPI file is `openapi.yaml`. Sync it into the embedded copy with:

```bash
make openapi-generate
```
