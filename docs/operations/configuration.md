---
type: Reference
title: Configuration
description: Environment variables read from .env, with defaults, limits, and the services that use them.
resource: ../../.env.example
tags: [operations, configuration]
---

Compose builds the service configuration from `.env` (see `.env.example`).

| Variable             | Used by            | Default  | Notes                                             |
| -------------------- | ------------------ | -------- | ------------------------------------------------- |
| `ANTHROPIC_API_KEY`  | api                | empty    | Optional; empty ⇒ mock LLM                        |
| `ANTHROPIC_MODEL`    | api                | `claude-sonnet-5-5` | Model id used with a key               |
| `LLM_PROVIDER`       | api                | empty    | `mock` or `claude`; empty ⇒ `claude` if a key is set, else `mock`. `claude` without a key fails at startup |
| `LLM_SEGMENT_TIMEOUT`, `LLM_DRAFTS_TIMEOUT` | api | `20s`, `60s` | Per model call, including SDK retries (max `3m`) |
| `LLM_MAX_ATTEMPTS`   | api                | `2`      | Calls per request incl. repair attempts (1–3)     |
| `LLM_SDK_MAX_RETRIES`| api                | `2`      | SDK retries for 429/5xx/connection errors (0–5)   |
| `LLM_MOCK_STREAM_DELAY` | api             | `40ms`   | Pause between the mock's streamed chunks (max `1s`) |
| `PROGRESS_POLL_INTERVAL` | api            | `1s`     | Shared progress poll per campaign (100ms–10s)     |
| `PROGRESS_MAX_VIEWERS` | api              | `20`     | Open dashboard streams per campaign (1–1000)      |
| `PROGRESS_THROUGHPUT_WINDOW` | api        | `10s`    | Period of the dashboard's send rate (max `5m`)    |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | compose | — | Required; local-only credentials, URL-safe characters |
| `LOG_LEVEL`          | all                | `info`   | `debug`, `info`, `warn`, `error`                  |
| `SHUTDOWN_TIMEOUT`   | all                | `10s`    | Graceful shutdown bound, max `60s`                |
| `DELIVERY_MAX_ATTEMPTS` | api, worker     | `5`      | Attempts per delivery incl. the first (1–20), stored at approval or release |
| `SCHEDULE_MIN_LEAD`, `SCHEDULE_MAX_HORIZON` | api, worker | `1m`, `720h` | Allowed scheduled times relative to now (max `24h`, `8784h`); lead < horizon |
| `SCHEDULE_RELEASE_GRACE` | api, worker    | `15m`    | A due campaign not released within it expires (max `24h`); must exceed `DELIVERY_SCAN_INTERVAL` |
| `PROVIDER_TIMEOUT`   | worker             | `5s`     | Per provider request                              |
| `RETRY_BASE_DELAY`, `RETRY_MAX_DELAY`, `RETRY_BACKOFF_FACTOR` | worker | `1s`, `60s`, `2` | Backoff with full jitter; the cap also bounds `Retry-After` |
| `DELIVERY_LEASE`     | worker             | `60s`    | Claim lease; at least 2 × `PROVIDER_TIMEOUT`      |
| `WORKER_CONCURRENCY` | worker             | `16`     | Messages processed concurrently per worker        |
| `RATE_LIMIT_PER_SECOND`, `RATE_LIMIT_BURST` | worker | `90`, `5` | Shared token bucket per channel; sum ≤ 100 |
| `KAFKA_TOPIC_PARTITIONS` | worker         | `6`      | Partitions of the delivery topics (created at startup) |
| `MOCK_SEED`          | mock-provider      | `0`      | Fault seed; `0` picks a random one at start       |
| `MOCK_LATENCY_MIN`, `MOCK_LATENCY_MAX` | mock-provider | `10ms`, `80ms` | Uniform latency per accepted message |
| `MOCK_RATE_LIMIT_RATE`, `MOCK_RETRY_AFTER` | mock-provider | `0.06`, `1s` | Injected 429s and their `Retry-After` |
| `MOCK_SERVER_ERROR_RATE` | mock-provider  | `0.08`   | Injected 503s                                     |
| `MOCK_PERMANENT_FAILURE_RATE` | mock-provider | `0.01` | Share of recipients always rejected with 422  |
| `MOCK_LOST_RESPONSE_RATE`, `MOCK_LOST_RESPONSE_DELAY` | mock-provider | `0.03`, `8s` | Stored, then the response stalls (longer than `PROVIDER_TIMEOUT`) and ends in 504 |
| `MOCK_MAX_PER_SECOND` | mock-provider     | `100`    | The provider's own per-channel limit (429); `0` disables |
| `SEED_USER_COUNT`    | seed               | `5000`   | 1–100000; applied only to an empty database       |
| `SEED_RANDOM_SEED`   | seed               | `20261022` | Same value ⇒ same data                          |
| `API_PORT`, `MOCK_PROVIDER_PORT`, `FRONTEND_PORT` | compose | `8080`, `9090`, `5173` | Host ports |

# Set by Compose

Set inside `docker-compose.yml`, so not needed in `.env`: `DATABASE_URL`,
`KAFKA_BROKERS`, `API_HTTP_ADDR`, `WORKER_HEALTH_ADDR`, `MOCK_PROVIDER_ADDR`,
`PROVIDER_URL` (default `http://mock-provider:9090`), `OUTBOX_POLL_INTERVAL`
(`200ms`), `OUTBOX_BATCH_SIZE` (`500`), `DELIVERY_SCAN_INTERVAL` (`1s`),
`READINESS_TIMEOUT` (default `2s`), and `HTTP_REQUEST_TIMEOUT` (default
`30s`; campaign creation instead gets `LLM_MAX_ATTEMPTS ×` the longer LLM
timeout plus 5 s, and the draft stream has no request timeout but the same
bound on the generation itself).

Invalid values stop the process at startup with a message naming every bad
variable. Secret values are never echoed.
