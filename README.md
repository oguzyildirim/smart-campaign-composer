# Smart Campaign Composer

Marketers describe a campaign in natural language. Claude proposes an
audience segment and SMS/email drafts. A human reviews, edits, and approves,
and approved messages go only to fake users through a local mock provider.
**No real SMS or email is ever sent.**

> **Status: complete (Milestone 7, hardening).** The stack starts
> with ~5000 seeded fake users. In the browser, a marketer writes a brief
> (Turkish or English), sees the proposed audience with per-channel counts,
> watches SMS and email drafts stream in, edits them, previews them
> personalized for real (fake) recipients, and explicitly approves. Approval
> is persisted, bound to exactly the reviewed content, and creates one
> delivery per consented recipient and channel. Workers send them through
> Kafka to the local mock provider, which fails, delays, and rate-limits on
> purpose, with retries, idempotency keys, and a shared limit of at most 100
> messages per second per channel. After approval a live dashboard shows
> queued, processing, sent, failed and retrying messages per channel as
> they change. The api and worker expose Prometheus metrics. Claude or a
> deterministic mock does the AI work.

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
| Mock provider | http://localhost:9090/healthz, `/v1/stats`, `/v1/messages` | in-memory, fake recipients only |
| Worker        | internal only (`:8081/healthz`, `/readyz`) | scale with `--scale worker=3`  |
| Postgres, Kafka | internal Compose network only       |                                     |

All published ports bind to `127.0.0.1`. Check status with `docker compose ps`.
Every long-running service reports `healthy`, and the one-shot `migrate` and
`seed` services exit with code 0 (the API starts only after both succeed).

## Commands

| Command          | What it does                                                            |
| ---------------- | ----------------------------------------------------------------------- |
| `make up`        | Build and start the stack detached (requires `.env`)                    |
| `make down`      | Stop the stack (volumes are kept)                                       |
| `make test`      | All Go tests (unit + integration via testcontainers) and frontend tests |
| `make test-unit` | Tests that need no containers (Go unit with `-race`, frontend)          |
| `make smoke`     | Run the example campaign end to end against the running stack (below)   |
| `make fuzz`      | Fuzz each target for `FUZZTIME` (default `30s`)                         |
| `make lint`      | golangci-lint, ESLint, and vue-tsc                                      |
| `make vuln`      | govulncheck (pinned, installed into `bin/`; needs network)              |
| `make fmt`       | gofmt + goimports                                                       |
| `make help`      | List all targets                                                        |

### Smoke test

With the stack running (`docker compose up -d --wait`):

```bash
make smoke    # API_PORT=… MOCK_PROVIDER_PORT=… if you changed them in .env
```

`cmd/smoke` waits for readiness, creates the example campaign, streams the
drafts, approves them, and polls progress until the campaign completes. It
then compares the result with the mock provider's statistics:
- every delivery is terminal;
- the provider delivered exactly the messages recorded as sent;
- no recipient got a duplicate;
- no provider-side throttling occurred;
- the peak rate is at most 100 requests per channel per second.

It exits non-zero on any failed check.

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

## Campaign flow

Open http://localhost:5173, choose **New campaign**, and paste the example
brief:

> Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki
> kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun.

1. **Audience.** The AI proposes a segment rule; the backend validates it
   and counts matching users and consented recipients per channel. Criteria
   the rules cannot express are shown as a warning.
2. **Drafts.** *Generate SMS and email drafts* streams the text field by
   field. Partial text is display-only; drafts are stored only after they
   pass validation. Closing the tab cancels the model call.
3. **Edit and preview.** Edits autosave. The preview renders each selected
   channel's content for sample recipients, including the one with the
   longest first name, and shows final lengths after personalization and
   every problem approval would reject.
4. **Approve.** *Approve…* asks for your name and approves exactly the
   version and content hash you reviewed. The campaign is then read-only.
   The same transaction creates the deliveries (see [Delivery](#delivery)).

Statuses: `draft → audience_ready → drafts_ready → approved → sending →
completed`. The backend owns them; the UI only displays them. `sending`
starts when a worker claims the first delivery; `completed` means every
delivery is `sent` or `failed`.

### Endpoints

| Method and path | Purpose |
| --------------- | ------- |
| `POST /api/v1/campaigns` `{"brief"}` | Create: AI segment extraction, validation, counts → `201`, `audience_ready` |
| `GET /api/v1/campaigns` | The 100 most recent campaigns |
| `GET /api/v1/campaigns/{id}` | One campaign, with `validation` once drafts exist |
| `POST /api/v1/campaigns/{id}/drafts` | Generate drafts as Server-Sent Events (see below) |
| `PATCH /api/v1/campaigns/{id}` `{"version","channels","sms_body","email_subject","email_body"}` | Replace content and channels; all fields required |
| `GET /api/v1/campaigns/{id}/preview` | Content rendered for up to 3 recipients per selected channel, final lengths, all approval checks |
| `POST /api/v1/campaigns/{id}/approve` `{"version","content_hash","approved_by"}` | Explicit human approval |

**Draft stream.** The response is `text/event-stream` on a POST, read with
`fetch` (not `EventSource`, which would reconnect and silently start another
paid model call). Events: `delta` `{"attempt","field","text"}` with `field`
one of `sms_body`, `email_subject`, `email_body` (a new `attempt` means a
repair call started; discard earlier text); then `done`
`{"status","drafts","validation","version","content_hash"}` or `error`
`{"code","message"}`. Heartbeat comments arrive every 15 s. Only one
generation runs per campaign (`409 generation_in_progress` otherwise).

**Edits** must be structurally sound (known channels; UTF-8 without NUL;
at most twice each field's limit), or they get `400`. Content that breaks
a content rule (over a limit, unknown variable, HTML, empty) is saved and
reported in `validation`, so work in progress is never lost. Approval
rejects it. An edit that changes nothing does not bump the version.

**Approval** runs in one transaction. It checks the state, `version`, and
`content_hash`, then re-validates the segment rule and content. It then
checks every limit against the longest real `first_name` and `city` among
each selected channel's consented recipients, and requires at least one
recipient. Approving an approved campaign with the same hash returns `200`
unchanged with no second audit event; a different hash returns `409`.
`approved_by` is free text: there is no authentication.

Example (mock mode, seeded data; output as observed, shortened):

```bash
BRIEF='{"brief":"Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'"'"'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."}'
ID=$(curl -s -H 'Content-Type: application/json' -d "$BRIEF" localhost:8080/api/v1/campaigns | jq -r .id)
# status audience_ready, counts {"total":389,"sms":262,"email":246}
curl -sN -X POST localhost:8080/api/v1/campaigns/$ID/drafts
# event: delta
# data: {"attempt":1,"field":"sms_body","text":"Merhaba "}
# ...
# event: done
# data: {"status":"drafts_ready","drafts":{"sms_body":"Merhaba {{first_name}}, sepetindeki ürünler seni bekliyor! %30 indirimle bugün tamamla.",...},"version":2,...}
curl -s localhost:8080/api/v1/campaigns/$ID/preview | jq '.channels[0].samples[0].fields[0]'
# {"field":"sms.body","text":"Merhaba Alexandria Elizabeth Josephine, sepetindeki ürünler seni bekliyor! %30 indirimle bugün tamamla.",
#  "length":103,"limit":160,"over_limit":false}
HASH=$(curl -s localhost:8080/api/v1/campaigns/$ID | jq -r .content_hash)
curl -s -H 'Content-Type: application/json' \
  -d "{\"version\":1,\"content_hash\":\"$HASH\",\"approved_by\":\"Deniz\"}" localhost:8080/api/v1/campaigns/$ID/approve
# {"error":"the campaign was changed by someone else; reload it","code":"version_conflict"}   (stale version → 409)
```

The audit log is in `campaign_events` (action, actor, from → to status,
version). It never holds content or recipient data:

```bash
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT action, actor, from_status, to_status, version FROM campaign_events ORDER BY id"'
```

**Mock vs real mode.** With `ANTHROPIC_API_KEY` empty (or
`LLM_PROVIDER=mock`), a keyword-based mock answers. It recognizes the
example brief's city, cart, and no-purchase phrases, a few others
(Turkish and English), and age/gender/income/birthday as unsupported. It
returns fixed Turkish or English drafts, streamed in chunks paced by
`LLM_MOCK_STREAM_DELAY` so the UI shows them arriving. With a key set, the API calls
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
| 400/415| `invalid_request`      | Malformed or oversized body, unknown or missing fields, wrong type; `details` lists field problems |
| 404    | `not_found`            | No campaign with that id (also for ids that are not UUIDs) |
| 409    | `invalid_state`        | The status does not allow the action (e.g. editing an approved campaign) |
| 409    | `version_conflict`     | The campaign changed since it was read                    |
| 409    | `content_changed`      | The content hash does not match the stored content        |
| 409    | `generation_in_progress` | Drafts are being generated for the campaign             |
| 422    | `invalid_content`      | Approval: content fails validation, including for the real audience; `details` lists fields |
| 422    | `no_recipients`        | Approval: no selected channel has a consented recipient   |
| 422    | `unsupported_brief`    | Only unsupported criteria; `unsupported_criteria` lists them |
| 422    | `refused`              | The model declined the brief                              |
| 502    | `invalid_output`       | Output failed validation on every attempt                 |
| 503    | `upstream_unavailable` | Provider error (429/5xx/network) after SDK retries        |
| 504    | `timeout`              | The model did not answer within the call timeout          |

During draft generation, errors after the stream started arrive as an
`error` event with the same codes.

SMS length is counted in characters (runes), as the specification asks.
Real networks count differently: any character outside the GSM-7 alphabet
(ş, ğ, ı, İ, emoji) switches the message to UCS-2, where one segment holds
only 70 characters. A 160-character Turkish SMS can therefore be billed as
several segments.

## Delivery

```
approve ──(one transaction)──► campaigns.status = approved
                               deliveries: one row per consented (user, channel), status queued
                               outbox: one job message per delivery
worker: outbox relay ──► Kafka deliveries.sms / deliveries.email (acks=all)
worker: consumer ──► claim row (lease) ──► rate-limit token ──► consent + render + validate
                 ──► POST mock-provider (Idempotency-Key = delivery id) ──► record outcome
                 ──► commit offset
worker: maintenance (every 1 s) ──► expired leases → retrying; due retries → queued + outbox;
                                    campaigns with only sent/failed deliveries → completed
```

- **One delivery per person, channel, and campaign.** `deliveries` has
  `UNIQUE (campaign_id, user_id, channel)`. Approving again creates nothing.
- **Duplicates are absorbed.** A Kafka message only names a delivery. A
  worker must claim the row (`queued`/due `retrying` → `processing`, with a
  lease) before sending, so a redelivered message for a row that is
  processing, sent, or failed is skipped. The attempt counter fences the
  outcome: a worker whose lease expired cannot overwrite a newer attempt.
- **Uncertain outcomes are retried with the same key.** After a timeout the
  provider may already have the message; the retry sends the same
  `Idempotency-Key` (the delivery id), and the provider returns the
  original result instead of delivering twice.
- **Consent and content are checked right before sending.** Consent is read
  from the database after the rate-limit wait. Revoked consent fails the
  delivery with `consent_revoked` without calling the provider. The message
  is rendered for the recipient and re-validated (SMS ≤ 160 characters,
  subject ≤ 80, no leftover `{{…}}`); a failure is `invalid_content`.
- **Retries.** Network errors, timeouts, 408, 429, and 5xx are transient;
  other 4xx are permanent. Transient failures wait an exponential backoff
  with full jitter (1 s · 2^(attempt−1), capped at 60 s), or the provider's
  `Retry-After` when longer (also capped). After 5 attempts the delivery
  fails with `retries_exhausted`. Retries go back through the outbox, the
  claim, and the rate limiter, like first attempts.
- **Rate limit.** One token bucket per channel in Postgres, shared by every
  worker and refilled by the database clock: 90 tokens/s with a burst of 5,
  so any one-second window holds at most 95 sends per channel. Rate + burst
  may not exceed 100 (enforced at startup).
- **Crash recovery.** A killed worker leaves its claimed rows in
  `processing`; when their lease (60 s) expires, maintenance moves them to
  `retrying` (the attempt stays counted). Uncommitted Kafka messages are
  redelivered to the other workers. On SIGTERM a worker stops polling,
  gives in-flight sends half of `SHUTDOWN_TIMEOUT`, and commits offsets only
  for batches that finished.

Delivery statuses: `queued`, `processing`, `retrying`, `sent`, `failed`.
Error codes in `last_error_code`: `consent_revoked`, `invalid_content`,
`provider_rejected`, `provider_rate_limited`, `provider_unavailable`,
`provider_timeout`, `provider_invalid_response`, `interrupted`,
`internal_error`, `lease_expired`, `retries_exhausted`.

Run several workers and watch a campaign:

```bash
docker compose up -d --scale worker=3
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c \
  "SELECT channel, status, count(*) FROM deliveries GROUP BY 1, 2 ORDER BY 1, 2"'
curl -s localhost:9090/v1/stats | jq .channels
```

### Mock provider

`POST /v1/sms {"to","body"}` and `POST /v1/email {"to","subject","body"}`
require an `Idempotency-Key` header. The first accepted request for a key is
delivered; later requests with the same key return the original
`message_id` with `"duplicate": true`. The provider only accepts the fake
recipient ranges (`@example.com`, `+9995…`) and rejects everything else
with 422, a second safety net behind the database constraints.
`GET /v1/messages?channel=&limit=` lists accepted messages (newest first);
`GET /v1/stats` counts per channel: `received`, `delivered`,
`duplicates_suppressed`, `duplicate_content` (the same message delivered to
the same person under a different key; should stay 0), injected faults,
`throttled`, and `max_per_second` (most requests in any one-second window).

Faults are set with `MOCK_*` variables (see the table below). Decisions are
a hash of `MOCK_SEED`, the idempotency key, and the attempt number for that
key, so a fixed seed reproduces them however requests interleave. State is
in memory: restarting the mock provider forgets delivered keys and stats.

## Live monitoring

Approving a campaign opens its dashboard (`/campaigns/{id}/dashboard`; the
campaign list and the review page link to it). It shows the campaign status,
per-channel counts for every delivery status, overall and per-channel
progress bars, the recent send rate, failures by reason, and a connection
indicator (live / reconnecting / disconnected / completed).

```
browser ──GET /progress──► api ──► one aggregate query (snapshot)
browser ══EventSource /events══► api ◄── shared poller per campaign (1 query/s,
                                         however many viewers) ──► postgres
```

- `GET /api/v1/campaigns/{id}/progress` returns the snapshot as JSON:

  ```json
  {"campaign_id":"…","campaign_status":"sending",
   "channels":[{"channel":"sms","queued":100,"processing":10,"sent":140,"failed":2,"retrying":10,"total":262,"sent_recently":90,"percent_done":54.2}, …],
   "totals":{"queued":220,"processing":16,"sent":250,"failed":6,"retrying":16,"total":508},
   "percent_done":50.39,"failures":[{"code":"provider_rejected","count":5}],
   "throughput":{"window_seconds":10,"sent":150,"per_second":15},"generated_at":"…"}
  ```

- `GET /api/v1/campaigns/{id}/events` is a Server-Sent Events stream for
  `EventSource`: a `snapshot` event on connect, another one only when the
  state changed, and a final `complete` event (then the stream closes) once
  the campaign is completed. Every event carries the full snapshot, so a
  reconnect or a repeated event can never inflate the numbers. Event ids
  continue from `Last-Event-ID`; a reconnect always starts with a fresh
  snapshot. Heartbeat comments every 15 s. More than
  `PROGRESS_MAX_VIEWERS` streams on one campaign get `429 too_many_viewers`.

```bash
curl -s localhost:8080/api/v1/campaigns/$ID/progress
curl -sN localhost:8080/api/v1/campaigns/$ID/events     # Ctrl-C to stop
```

### Metrics

Prometheus text format on `GET /metrics` of the api (`localhost:8080/metrics`)
and of each worker (port 8081, published on an ephemeral host port per
replica). nginx does not proxy `/api/metrics`. No Prometheus or Grafana runs
in the stack; point your own scraper at these endpoints.

```bash
curl -s localhost:8080/metrics | grep -E '^(http_requests_total|llm_)'
curl -s localhost:$(docker compose port --index 1 worker 8081 | cut -d: -f2)/metrics \
  | grep -E '^(deliveries_total|provider_|retries_scheduled_total|rate_limiter_wait|outbox_pending|leases_recovered_total)'
```

| Metric | Labels | Where |
| --- | --- | --- |
| `http_requests_total`, `http_request_duration_seconds` | `route` (chi pattern), `method`, `status_class` (SSE stream durations are not observed) | api, worker |
| `llm_requests_total`, `llm_request_duration_seconds`, `llm_validation_failures_total` | `operation`, `outcome` | api |
| `deliveries_total` | `channel`, `outcome` (`sent`, `failed`, `retrying`, `released`) | worker |
| `provider_request_duration_seconds`, `provider_responses_total` | `channel`, `code_class` (`2xx`, `4xx`, `429`, `5xx`, `error`) | worker |
| `retries_scheduled_total` | `channel`, `reason` (error code) | worker |
| `rate_limiter_wait_seconds` | `channel` | worker |
| `outbox_pending` (gauge), `leases_recovered_total` | none | worker |

Labels never carry campaign, delivery, or user ids, addresses, or raw paths.
Example PromQL:

```
sum by (channel) (rate(deliveries_total{outcome="sent"}[1m]))          # sends/s per channel (≤ 100)
sum by (channel, code_class) (rate(provider_responses_total[5m]))       # provider error mix
histogram_quantile(0.95, sum by (le, channel) (rate(rate_limiter_wait_seconds_bucket[5m])))
sum by (operation) (rate(llm_validation_failures_total[1h]))
```

## Testing

`make test` runs every Go test with `-race` (unit tests, plus
`integration`-tagged tests against throwaway Postgres and Kafka containers)
and the frontend tests. No `ANTHROPIC_API_KEY` is needed: AI tests use the
deterministic mock LLM or a stub HTTP server.

- **Failure scenarios** (`internal/delivery/scenario_integration_test.go`)
  run the approved example campaign through the real claim, limiter, and
  provider client against an in-process mock provider. A log in front of the
  provider is the ground truth:
  - 5xx bursts, 429 storms with `Retry-After`, client timeouts, "accepted
    but response lost", and permanent 4xx. Every delivery ends `sent` or
    `failed`, never more than 5 attempts. Each retry reaches the provider no
    earlier than its backoff and the previous `Retry-After`.
  - Consent: no row without consent at approval, revoked consent fails
    unsent, and single-channel recipients get nothing on the other channel.
  - Four worker instances, each with its own pool and limiter, under 429s:
    every one-second sliding window holds at most rate + burst requests per
    channel, retries included (20 + 5 in the test; 90 + 5 in the stack).
  - Duplicates: a crash between provider acceptance and the database update,
    a lease that expires while the first worker is still sending, and
    (in `TestE2E_PipelineWithKafka`) every Kafka message and outbox row
    replayed. The provider holds at most one message per idempotency key
    and per recipient and channel.
  - Graceful shutdown of every worker mid-campaign, then a restart: the
    campaign completes with no lost or duplicate message.
  - Concurrent approvals are covered by
    `TestApprove_ConcurrentApprovalsMaterializeOnce` (Postgres).
- **API validation** (`internal/httpapi/validation_test.go`): malformed
  JSON, unknown fields, wrong types, oversized bodies, wrong content types,
  invalid ids, wrong states, and stale versions on every campaign endpoint.
  Error bodies are checked for SQL, driver errors, and stack traces.
- **AI safety**: the prompt-injection corpus and hostile model outputs
  (extra `approve` fields, raw SQL, `{{phone}}`, HTML, overlong SMS) go
  through the HTTP endpoints. Results are a validated proposal or a safe
  error, and nothing gets approved or queued.
- **Fuzzing**: Go native fuzz targets for the segment rule validator and
  compiler, the template parser and per-recipient renderer, and the LLM
  output decoders. `make test` runs their seed corpora; `make fuzz` fuzzes.


## Health endpoints

The api (`:8080`), each worker (`:8081`), and the mock provider (`:9090`)
serve both probes; Compose healthchecks use `/readyz`.

- `GET /healthz`: liveness. Always `200 {"status":"ok"}` while the process
  serves HTTP.
- `GET /readyz`: readiness. Pings Postgres and Kafka concurrently within
  `READINESS_TIMEOUT`. Returns `200` when all checks pass and `503` otherwise:

  ```json
  {"status":"unavailable","checks":{"kafka":"ok","postgres":"unavailable"}}
  ```

  Error details are logged server-side only. The mock provider has no
  dependencies, so its `/readyz` is `200` whenever it serves.

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
| `LLM_MOCK_STREAM_DELAY` | api             | `40ms`   | Pause between the mock's streamed chunks (max `1s`) |
| `PROGRESS_POLL_INTERVAL` | api            | `1s`     | Shared progress poll per campaign (100ms–10s)     |
| `PROGRESS_MAX_VIEWERS` | api              | `20`     | Open dashboard streams per campaign (1–1000)      |
| `PROGRESS_THROUGHPUT_WINDOW` | api        | `10s`    | Period of the dashboard's send rate (max `5m`)    |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | compose | — | Required; local-only credentials, URL-safe characters |
| `LOG_LEVEL`          | all                | `info`   | `debug`, `info`, `warn`, `error`                  |
| `SHUTDOWN_TIMEOUT`   | all                | `10s`    | Graceful shutdown bound, max `60s`                |
| `DELIVERY_MAX_ATTEMPTS` | api, worker     | `5`      | Attempts per delivery incl. the first (1–20), stored at approval |
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

## Architecture

```
browser ──► frontend (nginx) ──/api──► api ──► postgres ◄──────────┐
                                                (approval writes      │ claims, rate limit,
                                                 deliveries + outbox) │ outcomes
                                     kafka ◄── outbox relay ── worker(s) ──► mock-provider
   migrate (one-shot) ──► seed (one-shot) ──► postgres
```

```
cmd/api            API entry point (chi router, /healthz, /readyz, /metrics, /api/v1)
cmd/worker         worker entry point (outbox relay, consumer, maintenance, probes, metrics)
cmd/seed           one-shot fake-data loader
cmd/smoke          end-to-end check against the running stack (make smoke)
mock-provider/     mock SMS/email provider entry point
internal/
  config           env loading + validation, redacting Secret type
  health           liveness/readiness handlers, concurrent checks
  segment          rule DSL: strict parser/validator, SQL compiler, preview service
  campaign         state machine, edits, generation lease, preview, approval (Service);
                   AI generation: brief validation, output decoding, repair loop (Generator)
  campaign/campaigntest  in-memory Store and AudienceInspector fakes for unit tests
  content          drafts, {{variable}} templates, channel limits, plain-text checks,
                   per-recipient rendering with final validation
  delivery         claim → rate limit → consent/render → send → record (Processor);
                   retry policy, shared-bucket limiter, outbox relay, maintenance loops
  monitoring       progress read model: snapshot, shared per-campaign poller (Hub)
  mockprovider     the mock provider's API: idempotency, fake-recipient guard, faults
  llm              LLM port (interface, request types, errors)
  llm/mockllm      deterministic keyword mock with scripted test faults
  strictjson       strict JSON decoding (duplicate keys, unknown fields, trailing data)
  seed             deterministic fake-data generator + transactional loader
  httpapi          /api/v1 handlers (segment preview, campaigns, SSE draft stream,
                   progress JSON + SSE)
  worker           worker lifecycle: dependency wait, setup, long-running tasks
  platform/
    httpserver     server timeouts, middleware, graceful shutdown, probe router + client
    metrics        Prometheus registry, HTTP middleware, delivery and LLM adapters
    logging        slog JSON logger
    postgres       pgx pool, health checker, audience repository (consent filter),
                   campaign store (row lock + version check, audit events, delivery
                   materialization), delivery store (claim/lease, outbox), token bucket,
                   progress repository (one aggregate query)
    kafka          franz-go client, health checker, topic setup, publisher, consumer group
    provider       HTTP client for the mock provider (error classification, Retry-After)
    claude         Claude API adapter (anthropic-sdk-go, structured outputs, streaming)
  testutil/pgtest  testcontainers Postgres + migrations (integration tag)
  testutil/kafkatest  testcontainers Kafka (apache/kafka, KRaft; integration tag)
migrations/        versioned SQL (golang-migrate)
frontend/          Vue 3 + TypeScript (strict) + Vite + vue-router, Vitest, ESLint
  src/api          typed client, runtime response guards, fetch-based SSE reader
  src/views        campaign list, new campaign, review (stream, edit, preview, approve),
                   delivery dashboard (EventSource)
```

Dependencies flow inward. Entry points wire the platform adapters. The
`segment`, `content`, `llm`, `campaign`, and `delivery` packages import
neither HTTP, database, queue, nor LLM SDK code. The Postgres and Kafka
adapters, the provider and Claude clients, the metrics adapters, and the
HTTP handlers depend on them, not the reverse. `delivery` and `campaign`
report metrics through small observer ports (`delivery.Metrics`,
`campaign.LLMMetrics`); only `platform/metrics` imports Prometheus. See [DECISIONS.md](DECISIONS.md) for the reasoning
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
- **Campaign creation or the draft stream returns `502 invalid_output` or
  `503 upstream_unavailable`**:
  `docker compose logs api | grep "llm attempt"` shows each attempt's
  outcome and the validation problems. Briefs and generated text are never
  logged. `LLM_PROVIDER: claude requires ANTHROPIC_API_KEY` at startup
  means the provider was forced without a key.
- **A campaign stays in `sending`**: check `docker compose ps worker` and
  `docker compose logs worker`. Deliveries in `retrying` wait for
  `next_attempt_at`; deliveries stuck in `processing` are recovered when
  their lease (`DELIVERY_LEASE`, 60 s) expires.
- **`make test` reports a Kafka container that never starts**: the
  integration tests pull `apache/kafka:4.3.2`; the first run can take a
  minute.
- **The dashboard says "Reconnecting…"**: the API is unreachable; the
  browser retries every 2 s and shows a fresh snapshot once back.
  "Disconnected" means the server refused the stream (for example
  `429 too_many_viewers`); close other tabs and press *Reconnect*.
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
- Segment preview returns counts only, not recipient lists. The campaign
  preview shows first name and city of up to 3 fake recipients per channel,
  never contact details.
- The mock LLM is keyword based: it understands the example brief and
  some variations, not arbitrary briefs.
- There is no authentication. `approved_by` is a free-text name, and edits
  are attributed to "marketer". Anyone who can reach the API can approve.
- The segment rule cannot be edited by hand. To change the audience, edit
  the brief and create a new campaign.
- Generating drafts again replaces the current drafts, including edits.
  The UI only offers generation before the first drafts exist.
- Content limits are checked twice: against fixed worst-case variable
  lengths (first name 30, city 20) and, at approval, against the longest
  real values in the audience, and per recipient right before sending.
- Audience counts are a snapshot taken at creation. Approval re-checks the
  live audience, but the stored counts are not refreshed.
- If an API process dies mid-generation, the campaign's generation lease
  blocks a new generation until it expires (generation timeout + 15 s).
- SMS limits count characters, not GSM-7/UCS-2 segments (see above).
- Delivery is effectively-once only because the provider honours the
  idempotency key. A provider without idempotency support could deliver
  twice when a response is lost or a lease expires mid-send. The mock
  provider keeps its keys in memory, so restarting it during a campaign
  loses that protection.
- The audience is fixed at approval. Users who gain consent later are not
  added; users who revoke it are skipped at send time.
- The rate limit is enforced when a token is taken, not when the provider
  receives the request; network jitter can move a few requests into the
  next second. The defaults (90 + 5) leave headroom below 100.
- A delivery whose lease expires on its last attempt fails with
  `retries_exhausted` even if the provider accepted that attempt.
- There is no way to cancel or pause a sending campaign.
- Failed deliveries stay as `failed` rows; there is no dead-letter topic
  and no manual retry.
- Published outbox rows are deleted after 24 hours.
- A worker retries a message whose handling fails (for example while
  Postgres is down) until it succeeds, and holds its consumer-group
  rebalance meanwhile; other workers keep their partitions but cannot take
  over the stuck worker's until it recovers or is stopped.
- Dashboard numbers are up to `PROGRESS_POLL_INTERVAL` (1 s) old. A
  stream reconnecting to a different API instance gets a fresh snapshot,
  but event ids are per connection (continued from `Last-Event-ID`), not
  global.
- `outbox_pending` is counted by every worker; with several workers each
  reports the same global value (take `max`, not `sum`).
- The worker's metrics port is published on a random host port, so
  scraping it from the host needs `docker compose port`.
- An attempt interrupted by shutdown is given back, so repeated restarts
  in the middle of sends can make more than `DELIVERY_MAX_ATTEMPTS`
  provider calls for one delivery (all with the same idempotency key).

## Requirements coverage

| Requirement | Where it is enforced | Tests |
| --- | --- | --- |
| Segment from the LLM is validated; no LLM SQL | `segment` allowlist parser, compiler to `$n` placeholders | `TestParse_*`, `TestCompile_ValuesNeverReachSQLText`, `FuzzParse`, `TestAISafety_*` |
| No message without channel consent | consent filter at approval; re-read before each send | `TestApprove_MaterializesConsentedDeliveries`, `TestScenario_Consent`, `TestE2E_ConsentRevokedAfterApprovalIsNotSent` |
| SMS ≤ 160, subject ≤ 80, `{{first_name}}` | `content` templates; worst case at approval; per recipient before send | `content` tests, `TestApprove_LongestRealFirstName`, `FuzzRenderMessage` |
| Async queue, ≤ 100 msg/s per channel | outbox → Kafka; Postgres token bucket shared by workers | `TestTokenBucket_SharedWindowAcrossInstances`, `TestScenario_RateLimitAcrossWorkers`, `make smoke` |
| Same message never twice to a person | unique (campaign, user, channel); leased claims; idempotency key | `TestScenario_CrashAfterProviderAccepted`, `TestScenario_LeaseExpiresWhileSending`, `TestE2E_PipelineWithKafka` |
| Retry strategy | bounded attempts, exponential backoff + full jitter, `Retry-After` | `TestRetryPolicy_*`, `TestScenario_ProviderFailures` |
| Live status, metrics, health | progress snapshot + SSE, `/metrics`, `/healthz`, `/readyz` | `TestProgress_*`, `TestEvents_*`, `TestHub_*`, `TestProbeRouter_*` |
| Structured, validated LLM output | strict JSON decoding, size caps, bounded repair | `TestGenerator_*`, `FuzzDecodeSegmentOutput`, `FuzzDecodeDrafts` |
| Tests without an API key | `mockllm`; stub server for the Claude adapter | the whole suite |
| Human approval only | approval bound to version + content hash; no LLM code path | `TestApprove_*`, `TestAISafety_InjectionCorpusThroughEndpoints` |

## Not done

- Bonus items: scheduled sending, A/B variants, locale-based content (the
  mock answers in the brief's language, but there is no per-recipient
  language), CI pipeline.
- Authentication and roles (see Known limitations).
- Redis, retry or dead-letter topics: retries go through Postgres and the
  outbox; failed deliveries stay as `failed` rows.
- Prometheus/Grafana containers, alerting, tracing, and a per-recipient
  delivery log in the UI.
