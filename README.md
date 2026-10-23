# Smart Campaign Composer

Marketers describe a campaign in natural language. Claude proposes an
audience segment and SMS/email drafts. A human reviews, edits, and approves,
and approved messages go only to fake users through a local mock provider.
**No real SMS or email is ever sent.**

> **Status: Milestone 3 (AI campaign generation).** The stack starts with
> ~5000 seeded fake users. A JSON segment rule can be validated and sized
> per channel, and a natural-language brief (Turkish or English) becomes a
> validated segment proposal with counts and validated SMS/email drafts,
> using Claude or a deterministic mock. Campaign persistence, approval,
> delivery, and the dashboard are not implemented yet.

## Requirements

- Docker with Docker Compose v2 (required for the stack **and** for
  `make test`, which uses testcontainers)
- Go ≥ 1.21 (the pinned go1.27.3 toolchain is downloaded automatically)
- Node.js ≥ 24 and npm (frontend tests and lint)
- GNU Make

`ANTHROPIC_API_KEY` is **optional**. Without it the API uses a
deterministic mock LLM, and every test runs without it.

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

## AI generation

Two temporary endpoints turn a brief into proposals. The next milestone
replaces them with campaign endpoints and streaming:

- `POST /api/v1/ai/segment` `{"brief": "..."}` → validated segment rule,
  per-channel counts (same consent-aware counting as the preview), criteria
  the rules cannot express, and a short explanation.
- `POST /api/v1/ai/drafts` `{"brief": "..."}` → validated SMS and email
  drafts plus their worst-case lengths.

Example (mock mode, seeded data; output as observed):

```bash
BRIEF='{"brief":"Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'"'"'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."}'
curl -s -H 'Content-Type: application/json' -d "$BRIEF" localhost:8080/api/v1/ai/segment
# {"rule":{"version":1,"conditions":[{"field":"city","op":"eq","value":"İstanbul"},
#   {"field":"added_to_cart_within_days","op":"eq","value":30},
#   {"field":"no_purchase_within_days","op":"eq","value":30}]},
#  "counts":{"total":389,"sms":262,"email":246},"unsupported_criteria":[],
#  "explanation":"Users matching: city İstanbul; added to cart within 30 days; no purchase within 30 days."}
curl -s -H 'Content-Type: application/json' -d "$BRIEF" localhost:8080/api/v1/ai/drafts
# {"drafts":{"sms":{"body":"Merhaba {{first_name}}, sepetindeki ürünler seni bekliyor! %30 indirimle bugün tamamla."},
#   "email":{"subject":"{{first_name}}, sepetin seni bekliyor - %30 indirim","body":"Merhaba {{first_name}},\n\n..."}},
#  "worst_case_lengths":{"sms_body":103,"email_subject":67,"email_body":181}}
```

**Mock vs real mode.** With `ANTHROPIC_API_KEY` empty (or
`LLM_PROVIDER=mock`), a keyword-based mock answers. It recognizes the
example brief's city, cart, and no-purchase phrases, a few others
(Turkish and English), and age/gender/income/birthday as unsupported. It
returns fixed Turkish or English drafts. With a key set, the API calls
Claude (`ANTHROPIC_MODEL`, default `claude-sonnet-5-5`); restart the API
after changing `.env` (`docker compose up -d api`). The startup log line
shows `llm_provider` and `llm_model`, never the key.

**What is validated.** Model output is untrusted. It must match the JSON
contract exactly (no unknown, missing, or duplicate keys; size caps
16 KB / 32 KB), the rule must pass the same validator as hand-written rules
before any count runs, and drafts must be plain text (no HTML or control
characters) and may use only `{{first_name}}` and `{{city}}`. Limits are
checked on the worst case, with `first_name` counted as 30 characters and
`city` as 20: SMS ≤ 160, email subject ≤ 80, email body ≤ 2000 characters.
If output fails validation, the model gets one repair attempt with the
validation errors (`LLM_MAX_ATTEMPTS`). The brief is limited to 1000
characters.

**Errors** carry a stable `code`; no model text or provider error detail is
returned:

| Status | `code`                 | Meaning                                                   |
| ------ | ---------------------- | --------------------------------------------------------- |
| 400    | `invalid_brief`        | Empty, longer than 1000 characters, or control characters |
| 400/415| `invalid_request`      | Malformed or oversized body, unknown fields, wrong type   |
| 422    | `unsupported_brief`    | Only unsupported criteria; `unsupported_criteria` lists them |
| 422    | `refused`              | The model declined the brief                              |
| 502    | `invalid_output`       | Output failed validation on every attempt                 |
| 503    | `upstream_unavailable` | Provider error (429/5xx/network) after SDK retries        |
| 504    | `timeout`              | The model did not answer within the call timeout          |

SMS length is counted in characters (runes), as the specification asks.
Real networks count differently: any character outside the GSM-7 alphabet
(ş, ğ, ı, İ, emoji) switches the message to UCS-2, where one segment holds
only 70 characters. A 160-character Turkish SMS can therefore be billed as
several segments.

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
go test ./internal/campaign/... -run TestGenerator_ProposeSegment_InvalidOutput
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
| `ANTHROPIC_API_KEY`  | api                | empty    | Optional; empty ⇒ mock LLM                        |
| `ANTHROPIC_MODEL`    | api                | `claude-sonnet-5-5` | Model id used with a key               |
| `LLM_PROVIDER`       | api                | empty    | `mock` or `claude`; empty ⇒ `claude` if a key is set, else `mock`. `claude` without a key fails at startup |
| `LLM_SEGMENT_TIMEOUT`, `LLM_DRAFTS_TIMEOUT` | api | `20s`, `60s` | Per model call, including SDK retries (max `3m`) |
| `LLM_MAX_ATTEMPTS`   | api                | `2`      | Calls per request incl. repair attempts (1–3)     |
| `LLM_SDK_MAX_RETRIES`| api                | `2`      | SDK retries for 429/5xx/connection errors (0–5)   |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | compose | — | Required; local-only credentials, URL-safe characters |
| `LOG_LEVEL`          | all                | `info`   | `debug`, `info`, `warn`, `error`                  |
| `SHUTDOWN_TIMEOUT`   | all                | `10s`    | Graceful shutdown bound, max `60s`                |
| `SEED_USER_COUNT`    | seed               | `5000`   | 1–100000; applied only to an empty database       |
| `SEED_RANDOM_SEED`   | seed               | `20261022` | Same value ⇒ same data                          |
| `API_PORT`, `MOCK_PROVIDER_PORT`, `FRONTEND_PORT` | compose | `8080`, `9090`, `5173` | Host ports |

Set inside `docker-compose.yml`, so not needed in `.env`: `DATABASE_URL`,
`KAFKA_BROKERS`, `API_HTTP_ADDR`, `WORKER_HEALTH_ADDR`, `MOCK_PROVIDER_ADDR`,
`READINESS_TIMEOUT` (default `2s`), and `HTTP_REQUEST_TIMEOUT` (default
`30s`; the AI routes instead get `LLM_MAX_ATTEMPTS ×` the longer LLM
timeout plus 5 s).
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
  campaign         AI generation: brief validation, output decoding, repair loop, error codes
  content          drafts, {{variable}} templates, channel limits, plain-text checks
  llm              LLM port (interface, request types, errors)
  llm/mockllm      deterministic keyword mock with scripted test faults
  strictjson       strict JSON decoding (duplicate keys, unknown fields, trailing data)
  seed             deterministic fake-data generator + transactional loader
  httpapi          /api/v1 handlers (segment preview, AI segment/drafts)
  worker           worker lifecycle
  platform/
    httpserver     server timeouts, middleware, graceful shutdown, probe client
    logging        slog JSON logger
    postgres       pgx pool, health checker, audience repository (consent filter)
    kafka          franz-go client + health checker
    claude         Claude API adapter (anthropic-sdk-go, structured outputs, streaming)
  testutil/pgtest  testcontainers Postgres + migrations (integration tag)
migrations/        versioned SQL (golang-migrate)
frontend/          Vue 3 + TypeScript (strict) + Vite, Vitest, ESLint
```

Dependencies flow inward. Entry points wire the platform adapters. The
`segment`, `content`, `llm`, and `campaign` packages import neither HTTP,
database, nor LLM SDK code. The Postgres repository, the Claude adapter,
and the HTTP handlers depend on them, not the reverse. See [DECISIONS.md](DECISIONS.md) for the reasoning
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
- **AI endpoints return `502 invalid_output` or `503 upstream_unavailable`**:
  `docker compose logs api | grep "llm attempt"` shows each attempt's
  outcome and the validation problems. Briefs and generated text are never
  logged. `LLM_PROVIDER: claude requires ANTHROPIC_API_KEY` at startup
  means the provider was forced without a key.
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
- The mock LLM is keyword based: it understands the example brief and
  some variations, not arbitrary briefs.
- The AI endpoints are temporary and return JSON only. Draft streaming
  (SSE), persistence, editing, and approval come in Milestone 4. Nothing
  generated is stored.
- Draft limits are checked against fixed worst-case variable lengths
  (first name 30, city 20), not yet against the real audience.
- SMS limits count characters, not GSM-7/UCS-2 segments (see above).

## Not yet implemented

Everything after Milestone 3: campaign persistence, draft streaming,
the approval UI, queueing and delivery, metrics, and the live dashboard.
