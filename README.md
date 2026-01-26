# LifeOps MVP Backend

Local MVP backend for personal life analytics (Health Bridge ingest + finance CSV + Telegram digest).

## Features
- LAN-only OpenAPI ingest endpoints for workouts, sleep, and metrics.
- CSV finance ingestion from `./data/finance`.
- Daily Telegram digest via LLM provider (OpenAI or mock).
- Telegram bots per agent with separate chat history/context.
- Nutritionist Telegram bot that logs every meal (text + optional photo), stores calories/macрос in Postgres, and exposes the data as a tool for other agents (e.g. the coach).

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
- `POSTGRES_DSN` (leave the host as `postgres` so containers can reach it)
- `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` (optional unless you need Telegram features)
- `TELEGRAM_BOT_TOKENS` (optional for multi-bot setup)
- `TELEGRAM_AGENT_CONFIG_DIR` (optional, defaults to `./data/telegram_agents`)
- `LLM_PROVIDER=mock|openai`
- `OPENAI_API_KEY` (required if `LLM_PROVIDER=openai`)
- `OPENAI_MODEL` (default `gpt-5-mini`)
- `TIMEZONE`

### 2) Start services (and run migrations automatically)

```bash
make up
```

`make up` performs three actions: builds images, brings up the compose stack, waits for Postgres, and runs the SQL migrations via the exposed port on `localhost:5432`. If you ever need to re-run migrations manually you can call `make migrate` (it defaults to the same local DSN but can be overridden via `POSTGRES_LOCAL_DSN`).

## LAN iOS connection
- API listens on `0.0.0.0:8080` by default (change with `BIND_ADDR`).
- Set Health Bridge iOS app to send data to `http://<mac-ip>:8080`.
- There is no authentication for LAN testing, so you can hit the ingest endpoints directly.

### Triggering the worker on-demand
`POST /v1/worker/run` lets you run the daily-review worker for any configured agent immediately. Example:

```bash
curl -X POST http://localhost:8080/v1/worker/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coach"}'
```

The request will use that agent’s Telegram token/chat list and send the generated message to the usual chat(s).

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

Commands (single bot mode):
- `/start`
- `/whoami`
- `/daily`
- `/coach` / `/sleep` / `/finance` / `/nutrition`

Commands (multi-bot mode):
- `/start`
- `/whoami`
- `/daily`

### Getting Telegram credentials
1. Open Telegram and start a chat with [@BotFather](https://t.me/BotFather). Send `/newbot` and follow the prompts to name your bot. BotFather will return the `TELEGRAM_BOT_TOKEN`; drop it into `.env`.
2. Send a message to your new bot from the account that should receive digests. Then query the updates API to discover the chat ID:
   ```bash
   curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
   ```
   Look for `"chat":{"id":123456789,...}` in the response and copy the numeric value into `TELEGRAM_CHAT_ID`.
3. Restart the compose stack (`make up`) so the `bot` and `worker` services pick up the credentials.

### Multi-bot setup (one bot per agent)
Set `TELEGRAM_BOT_TOKENS` as a comma-separated list of `agent=token` pairs, for example:

```bash
TELEGRAM_BOT_TOKENS=coach=123:sometoken,sleep=456:othertoken,finance=789:anothertoken
```

Each agent gets its own bot and its own chat history/context, and daily reviews are generated using that same chat history. To add a new bot, add another `agent=token` pair, then create a YAML config file in `./data/telegram_agents/<agent>.yaml` (or point `TELEGRAM_AGENT_CONFIG_DIR` to your custom folder).

Each YAML file supports:
- `prompt`: the system prompt for chat.
- `timezone`: IANA timezone name for the daily review schedule (defaults to `Europe/Moscow`).
- `daily_review_time`: time of day (`HH:MM`) for the daily review.
- `daily_review_prompt`: prompt text for the daily review. The worker automatically appends instructions describing every available tool, so keep this field focused on tone/style.
- `telegram_token` / `chat_id`: you can point these to environment variables using `${ENV_VAR_NAME}` or `env:ENV_VAR_NAME` so actual secrets stay in your `.env`. Example: `telegram_token: ${COACH_TELEGRAM_TOKEN}`. All env values are resolved automatically when configs are loaded.

Example:

```bash
cat <<'EOF' > ./data/telegram_agents/focus.yaml
name: focus
prompt: |
  Ты ассистент по продуктивности.
timezone: Europe/Moscow
daily_review_time: "09:00"
  daily_review_prompt: |
    Составь краткий обзор продуктивности за день.
    Получи свежие данные через доступные инструменты и сформулируй рекомендации.
EOF
```

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
