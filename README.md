# Smart Campaign Composer

Marketers describe a campaign in natural language. Claude proposes an
audience segment and SMS/email drafts. A human reviews, edits, and approves,
and approved messages go only to fake users through a local mock provider.
**No real SMS or email is ever sent.**

> **Status: Milestone 2 (user data and segmentation).** The stack starts
> with ~5000 seeded fake users, and a JSON segment rule can be validated and
> sized per channel through `POST /api/v1/segments/preview`. AI generation,
> approval, delivery, and the dashboard are not implemented yet.

## Requirements

- Docker with Docker Compose v2 (required for the stack **and** for
  `make test`, which uses testcontainers)
- Go ≥ 1.21 (the pinned go1.27.3 toolchain is downloaded automatically)
- Node.js ≥ 24 and npm (frontend tests and lint)
- GNU Make

`ANTHROPIC_API_KEY` is **not** required for anything in this milestone.

## Quick start

```bash
cp .env.example .env        # placeholders only; adjust if you like
docker compose up --build   # or: make up (detached)
```

| Service       | URL / address                         | Notes                               |
| ------------- | ------------------------------------- | ----------------------------------- |
| Frontend      | http://localhost:5173                 | nginx; proxies `/api/*` to the API  |
| API           | http://localhost:8080/healthz, `/readyz` |                                  |
| Mock provider | http://localhost:9090/healthz         | send endpoints come in Milestone 5  |
| Worker        | internal only (`:8081/healthz`, `/readyz`) |                                |
| Postgres, Kafka | internal Compose network only       |                                     |

All published ports bind to `127.0.0.1`. Check status with `docker compose ps`.
Every long-running service reports `healthy`, and the one-shot `migrate` and
`seed` services exit with code 0 (the API starts only after both succeed).

## Seed data

The `seed` service (`cmd/seed`) loads deterministic fake data once, right
after migrations: 5000 users across 8 Turkish cities (İstanbul is the
largest), `tr`/`en` locales, mixed SMS/email consent (granted, explicitly
denied, or never asked), and cart events and orders spread over the last
90 days relative to seed time. A dozen fixed scenario users guarantee
coverage of the example segment: Istanbul cart-abandoners with every
consent combination, a user who added to cart and then purchased, and a
few very long names for later SMS-length tests.

- **Clearly fake recipients.** Emails are `…@example.com` (reserved domain)
  and phones are `+9995XXXXXXXXX` (`+999` is not assigned to any country).
  CHECK constraints in the schema reject anything else.
- **Deterministic.** `SEED_USER_COUNT` and `SEED_RANDOM_SEED` fully
  determine the data (timestamps are relative to when the seed ran).
- **Idempotent.** If `users` already has rows, the seed logs
  `database already seeded; nothing to do` and exits. To reseed, remove the
  Postgres volume (`docker compose down -v`, deletes local data) and start
  again.

```bash
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SELECT city, count(*) FROM users GROUP BY city ORDER BY 2 DESC"'
```

## Segment preview

A segment rule is a small JSON document. Conditions are ANDed, and each
field may appear once.

| Field                       | Ops          | Value                                         |
| --------------------------- | ------------ | --------------------------------------------- |
| `city`                      | `eq`, `in`   | string, or array of ≤ 20 strings; 1–64 letters; case- and Turkish-letter-insensitive (`Istanbul` = `İSTANBUL`) |
| `locale`                    | `eq`, `in`   | `tr`, `en`                                    |
| `added_to_cart_within_days` | `eq`         | integer 1–365: ≥ 1 cart event in the window   |
| `no_purchase_within_days`   | `eq`         | integer 1–365: no order in the window         |
| `purchased_within_days`     | `eq`         | integer 1–365: ≥ 1 order in the window        |
| `order_count`               | `gte`, `lte` | integer 0–10000 (all-time orders)             |

"Within N days" means the last `N × 24h` up to the request time.

```bash
curl -s -X POST http://localhost:8080/api/v1/segments/preview \
  -H 'Content-Type: application/json' \
  -d '{"version":1,"conditions":[
        {"field":"city","op":"eq","value":"Istanbul"},
        {"field":"added_to_cart_within_days","op":"eq","value":30},
        {"field":"no_purchase_within_days","op":"eq","value":30}]}'
```

```json
{"rule":{"version":1,"conditions":[ ...normalized... ]},
 "counts":{"total":390,"sms":263,"email":247}}
```

`total` counts every matching user. `sms` and `email` count only users who
granted consent for that channel. The response echoes the **normalized**
rule (canonical condition order, trimmed and de-duplicated values).

Invalid rules return `400` with field-level details and never reach the
database:

```json
{"error":"invalid segment rule",
 "details":[{"path":"conditions[0].value","message":"city may contain only letters, spaces, hyphens, and periods"}]}
```

Rejected with `400`: malformed JSON, unknown or duplicate JSON keys,
unknown fields or operators, wrong value types (e.g. `"30"` or `30.5` for
days), out-of-range numbers, repeated fields, more than 10 conditions, and
bodies over 16 KB. A non-JSON `Content-Type` gets `415`. Server errors
return a generic `500`. The same path works through the frontend origin
(`http://localhost:5173/api/v1/...`).

## Commands

| Command          | What it does                                                            |
| ---------------- | ----------------------------------------------------------------------- |
| `make up`        | Build and start the stack detached (requires `.env`)                    |
| `make down`      | Stop the stack (volumes are kept)                                       |
| `make test`      | All Go tests (unit + integration via testcontainers) and frontend tests |
| `make test-unit` | Tests that need no containers (Go unit with `-race`, frontend)          |
| `make lint`      | golangci-lint, ESLint, and vue-tsc                                      |
| `make fmt`       | gofmt + goimports                                                       |
| `make help`      | List all targets                                                        |

Single tests:

```bash
go test ./internal/health/... -run TestReadinessHandler
go test ./internal/segment/... -run TestParse_Rejections
go test -tags=integration ./internal/platform/postgres/...   # Docker required
cd frontend && npm test
```

## Health endpoints

- `GET /healthz`: liveness. Always `200 {"status":"ok"}` while the process
  serves HTTP.
- `GET /readyz`: readiness. Pings Postgres and Kafka concurrently within
  `READINESS_TIMEOUT`. Returns `200` when all checks pass and `503` otherwise:

  ```json
  {"status":"unavailable","checks":{"kafka":"ok","postgres":"unavailable"}}
  ```

  Error details are logged server-side only.

## Configuration

Compose builds the service configuration from `.env` (see `.env.example`).

| Variable             | Used by            | Default  | Notes                                             |
| -------------------- | ------------------ | -------- | ------------------------------------------------- |
| `ANTHROPIC_API_KEY`  | api                | empty    | Optional; empty ⇒ mock LLM (from Milestone 3)     |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | compose | — | Required; local-only credentials, URL-safe characters |
| `LOG_LEVEL`          | all                | `info`   | `debug`, `info`, `warn`, `error`                  |
| `SHUTDOWN_TIMEOUT`   | all                | `10s`    | Graceful shutdown bound, max `60s`                |
| `SEED_USER_COUNT`    | seed               | `5000`   | 1–100000; applied only to an empty database       |
| `SEED_RANDOM_SEED`   | seed               | `20261022` | Same value ⇒ same data                          |
| `API_PORT`, `MOCK_PROVIDER_PORT`, `FRONTEND_PORT` | compose | `8080`, `9090`, `5173` | Host ports |

Set inside `docker-compose.yml`, so not needed in `.env`: `DATABASE_URL`,
`KAFKA_BROKERS`, `API_HTTP_ADDR`, `WORKER_HEALTH_ADDR`, `MOCK_PROVIDER_ADDR`,
`READINESS_TIMEOUT` (default `2s`), and `HTTP_REQUEST_TIMEOUT` (default `30s`).
Invalid values stop the process at startup with a message naming every bad
variable. Secret values are never echoed.

## Architecture

```
browser ──► frontend (nginx) ──/api──► api ──► postgres
                                        └────► kafka ◄──── worker ──► mock-provider
   migrate (one-shot) ──► seed (one-shot) ──► postgres
```

```
cmd/api            API entry point (chi router, /healthz, /readyz, /api/v1)
cmd/worker         worker entry point (dependency wait, probe server)
cmd/seed           one-shot fake-data loader
mock-provider/     mock SMS/email provider (only /healthz so far)
internal/
  config           env loading + validation, redacting Secret type
  health           liveness/readiness handlers, concurrent checks
  segment          rule DSL: strict parser/validator, SQL compiler, preview service
  seed             deterministic fake-data generator + transactional loader
  httpapi          /api/v1 handlers (segment preview)
  worker           worker lifecycle
  platform/
    httpserver     server timeouts, middleware, graceful shutdown, probe client
    logging        slog JSON logger
    postgres       pgx pool, health checker, audience repository (consent filter)
    kafka          franz-go client + health checker
  testutil/pgtest  testcontainers Postgres + migrations (integration tag)
migrations/        versioned SQL (golang-migrate)
frontend/          Vue 3 + TypeScript (strict) + Vite, Vitest, ESLint
```

Dependencies flow inward. Entry points wire the platform adapters. The
`segment` domain package imports neither HTTP nor database code; the
Postgres repository and HTTP handlers depend on it, not the reverse. See [DECISIONS.md](DECISIONS.md) for the reasoning
behind each choice.

## Troubleshooting

- **`required variable POSTGRES_USER is missing`**: create `.env` from
  `.env.example`.
- **`/readyz` returns 503**: run `docker compose ps` and
  `docker compose logs api`. The log names the failing dependency.
- **`make test` fails with a Docker error**: integration tests need a
  running Docker daemon. On WSL 2, enable *Docker Desktop → Settings →
  Resources → WSL integration* for your distro. `make test-unit` works
  without Docker.
- **Changing Postgres credentials after the first start** has no effect on
  the existing volume. Reset it with `docker compose down -v` (deletes local
  data).

## Known limitations

- Segment rules are AND-only; alternatives use `in`. There is no OR or
  nesting, and a field can appear only once (so no `order_count` range).
- Contradictory rules (e.g. `purchased_within_days: 7` together with
  `no_purchase_within_days: 30`) are valid and count 0.
- The seed only fills an empty database. Changing `SEED_USER_COUNT` later
  requires removing the Postgres volume.
- Preview returns counts only, not recipient lists.

## Not yet implemented

Everything after Milestone 2: LLM integration (brief → rule, drafts),
campaign approval UI, queueing and delivery, metrics, and the live
dashboard.
