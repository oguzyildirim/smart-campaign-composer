# Smart Campaign Composer

Marketers describe a campaign in natural language. Claude proposes an
audience segment and SMS/email drafts. A human reviews, edits, and approves,
and approved messages go only to fake users through a local mock provider.
**No real SMS or email is ever sent.**

## Requirements

- Docker with Docker Compose v2 (for the stack and for `make test`)
- Go ≥ 1.21 (the pinned toolchain is downloaded automatically)
- Node.js ≥ 24 and npm (frontend tests and lint)
- GNU Make

`ANTHROPIC_API_KEY` is **optional**. Without it the API uses a
deterministic mock LLM, and every test runs without it.

## Quick start

```bash
cp .env.example .env        # placeholders only; add ANTHROPIC_API_KEY if you have one
docker compose up --build   # or: make up (detached)
```

Open http://localhost:5173, choose **New campaign**, and paste the example
brief:

> Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki
> kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun.

| Service       | Address                                    |
| ------------- | ------------------------------------------ |
| Frontend      | http://localhost:5173                      |
| API           | http://localhost:8080 (`/healthz`, `/readyz`, `/metrics`) |
| Mock provider | http://localhost:9090 (`/v1/stats`, `/v1/messages`) |
| Worker        | internal only; scale with `docker compose up -d --scale worker=3` |
| Postgres, Kafka | internal Compose network only            |

On startup, `migrate` builds the database and `seed` loads 5000 fake users
(emails `@example.com`, phones `+9995…`). Both run once and exit. Check the
stack with `docker compose ps`. To start over with an empty database, run
`docker compose down -v`, which deletes local data.

## Commands

| Command          | What it does                                                  |
| ---------------- | ------------------------------------------------------------- |
| `make up` / `make down` | Start the stack detached / stop it (data is kept)      |
| `make test`      | All Go tests (unit + integration, needs Docker) and frontend tests |
| `make test-unit` | Go unit tests and frontend tests, no Docker needed            |
| `make smoke`     | Run the example campaign end to end against the running stack |
| `make lint`      | golangci-lint, ESLint, vue-tsc                                |
| `make vuln`      | govulncheck                                                   |
| `make fuzz`      | Run the Go fuzz targets                                       |
| `make fmt`       | Format Go code                                                |
| `make help`      | List all targets                                              |

## How it works

```
browser ──► frontend ──/api──► api ──► postgres ◄─────────────┐
                                                               │
                         kafka ◄── outbox relay ── worker(s) ──► mock-provider
```

### Campaign

1. **Audience.** The AI turns the brief into a segment rule, for example
   "city is Istanbul, added to cart in the last 30 days, no purchase in the
   last 30 days". The backend accepts only allowlisted fields and operators
   and turns the rule into parameterized SQL. The AI never writes SQL.
2. **Drafts.** SMS and email drafts stream into the page. They must be
   plain text, may use only `{{first_name}}` and `{{city}}`, and must fit
   the limits: SMS ≤ 160 characters, email subject ≤ 80.
3. **Edit and preview.** Edits autosave. The preview shows the messages
   personalized for real (fake) recipients, with their final lengths.
4. **Approve.** A human approves exactly the content they reviewed, to send
   now or on a schedule. The AI can never approve or send.

Statuses: `draft → audience_ready → drafts_ready → approved → sending →
completed`. With a schedule, the path is `drafts_ready → scheduled →
approved → …`, or the campaign ends as `release_failed` or `expired`.

### Scheduled delivery

A scheduled campaign stays locked until its time, and it can be rescheduled
or canceled until then. At the scheduled time, a worker checks the audience
and the content again. If everything passes, sending starts. If the
campaign is more than 15 minutes late, for example because the workers were
down, it expires instead of sending late.

### Delivery

- Approval creates one delivery per consented user and channel. A database
  constraint guarantees that nobody gets the same campaign twice on a channel.
- Jobs are written to an outbox table and carried to the workers through
  Kafka.
- Before each send, the worker waits for a rate-limit token, re-checks
  consent, and validates the personalized message again.
- At most 100 messages per second are sent per channel, across all workers.
  The limit is a token bucket in Postgres that every worker shares.
- Temporary failures are retried up to 5 attempts, with exponential backoff
  and jitter, and the provider's `Retry-After` is respected. Each retry
  reuses the same idempotency key, so the provider never delivers a message
  twice.
- If a worker crashes, its unfinished deliveries are picked up again after
  their lease expires.

The mock provider fails, delays, and rate-limits on purpose so that the
retry logic gets exercised. It accepts only fake recipients.

### Monitoring

After approval, the dashboard shows live progress per channel (queued,
processing, sent, failed, retrying) over Server-Sent Events. The api and
worker expose Prometheus metrics on `/metrics`.

## AI: mock vs Claude

With `ANTHROPIC_API_KEY` empty, a keyword-based mock answers. It
understands the example brief and a few variations. With a key set, the API
calls Claude. Restart the API after changing `.env`
(`docker compose up -d api`). In both cases the model's output is treated as
untrusted and strictly validated. If it fails validation, the model gets one
repair attempt.

## Configuration

All settings come from `.env`. See `.env.example` for every variable and its
default. Invalid values stop the service at startup with a message naming
the bad variable.

## Troubleshooting

- **`required variable POSTGRES_USER is missing`**: create `.env` from
  `.env.example`.
- **`/readyz` returns 503**: run `docker compose logs api`. The log names
  the failing dependency.
- **`make test` fails with a Docker error**: start Docker. On WSL 2, enable
  WSL integration in Docker Desktop. `make test-unit` works without Docker.
- **A campaign stays in `sending`**: check `docker compose logs worker`.
  Retrying deliveries wait for their backoff, and stuck ones are recovered
  after 60 s.

## Known limitations

- There is no authentication. `approved_by` is a free-text name.
- Segment rules support AND only, with no OR or nesting.
- The mock LLM understands only the example brief and similar ones.
- Delivery is effectively-once, not exactly-once: it relies on the
  provider honouring the idempotency key.
- A sending campaign cannot be paused or canceled.
- Failed deliveries stay as `failed` rows. There is no dead-letter queue and
  no manual retry.

## More

- [docs/](docs/index.md): detailed reference (API endpoints and error
  codes, delivery and scheduling internals, configuration, metrics,
  testing, troubleshooting, limitations).
- [DECISIONS.md](DECISIONS.md): the overall approach and main choices.
- [ai-sessions/DECISIONS.md](ai-sessions/DECISIONS.md): detailed technical
  decisions, alternatives, and trade-offs.
