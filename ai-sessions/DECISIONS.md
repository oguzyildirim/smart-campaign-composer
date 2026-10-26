# Smart Campaign Composer — Decisions

## Milestone 1 — Project foundation

### D1. Go 1.26+ with a pinned toolchain (go1.27.3)

`../go.mod` declares `go 1.26.0` (the minimum required by pgx, testcontainers,
and golang-migrate at current versions) and `toolchain go1.27.3`. Older local
Go installations download the pinned toolchain automatically
(`GOTOOLCHAIN=auto`, the default). Docker images use `golang:1.27.3-alpine`
with `GOTOOLCHAIN=local` so builds never download anything at build time.

### D2. Single Go module, three binaries

`../cmd/api`, `cmd/worker`, and `mock-provider/` share one module and the
`internal/platform` packages (HTTP server, logging, Postgres, Kafka).
*Alternative:* a separate module for the mock provider. Rejected for now: it
would duplicate the server/lifecycle code. The mock provider only imports
generic platform packages, never domain logic, so it can be split later.

### D3. Router: `go-chi/chi/v5`

100% `net/http`-compatible (handlers and middleware are plain
`http.Handler`), small, no reflection. Provides route groups, URL params,
and request-ID/timeout middleware. *Alternatives:* stdlib `ServeMux` (Go
1.22 patterns) lacks middleware composition and route groups; Gin/Echo use
their own context types and pull business code toward the framework.

### D4. Logging: `log/slog` JSON

Standard library, structured, no dependency. Secrets use a `config.Secret`
type that redacts itself in `fmt`, `slog`, and JSON. Access logs record the
path without the query string. Probe endpoints (`/healthz`, `/readyz`) log
at debug level because Docker polls them every 10 s; failing readiness
checks are logged as warnings by the health package.

### D5. Postgres access: `jackc/pgx/v5` + `pgxpool`, no ORM

pgx is the most complete and fastest Postgres driver for Go. Plain SQL keeps
the segment-rule → parameterized-SQL translation (Milestone 2) explicit and
auditable. The pool connects lazily, so the API starts while Postgres is
down and reports it through `/readyz`. Pool parse errors are replaced with
a generic error because pgx may quote the connection string (password).

### D6. Migrations: `golang-migrate/migrate`, run by a one-shot service

Plain versioned `.up.sql`/`.down.sql` files. In Compose, the official
`migrate/migrate` image runs once before the API and worker start
(`service_completed_successfully`). The same library applies migrations in
integration tests, so tests and Compose exercise identical files.
*Alternative:* running migrations inside the API at startup. Rejected: with
several replicas, startup migrations race and couple deploys to schema
changes. Trade-off: the migrate service's command line includes the DB
password (visible in `docker inspect`), which is acceptable for local-only
credentials.

### D7. Kafka: `apache/kafka` in KRaft mode + `twmb/franz-go`

KRaft needs no ZooKeeper. franz-go is pure Go (no cgo/librdkafka), supports
transactions and consumer groups, and is actively maintained.
Auto-topic-creation is disabled: the worker creates the delivery topics
explicitly with known partition counts (D40).

### D8. Readiness semantics

- `GET /healthz`: liveness only; 200 while the process serves HTTP.
- `GET /readyz`: pings Postgres and Kafka **concurrently** under one
  timeout (`READINESS_TIMEOUT`, default 2 s). It returns 503 if any check
  fails. The response names each check as `ok`/`unavailable` and never
  includes error text, hosts, or ports; details go to the server log.
- A checker that ignores context cancellation still cannot stall the
  probe (the probe returns on timeout).
- Compose healthchecks for `api` and `worker` use `/readyz`, so "healthy"
  in `docker compose ps` means the dependencies are reachable.

### D9. Worker lifecycle

The worker waits for its dependencies with capped exponential backoff
(0.5 s → 5 s), then runs its delivery tasks until shutdown (D47). It serves only
`/healthz` and `/readyz` on an internal port (8081, not published). An
errgroup ties the worker loop and the probe server together: if either
fails, the process exits instead of running half-alive.

### D10. Graceful shutdown

`signal.NotifyContext` (SIGINT/SIGTERM) → `http.Server.Shutdown` bounded by
`SHUTDOWN_TIMEOUT` (default 10 s, max 60 s). If the bound is hit, remaining
connections are force-closed. Compose `stop_grace_period` (20 s) is longer
than the app timeout, so Docker never SIGKILLs a clean shutdown. There is
no `http.Server.WriteTimeout` because SSE streams (Milestone 4) are
long-lived. Slow-client protection comes from `ReadHeaderTimeout` and
`ReadTimeout`, and handlers are bounded by the chi `Timeout` middleware.

### D11. Container images

- Go services: multi-stage build into `gcr.io/distroless/static-debian13:nonroot`.
  No shell, no package manager, non-root, and `read_only: true` in Compose.
  Because there is no curl/wget, each binary has a `healthcheck URL`
  subcommand that Compose uses.
- Frontend: built with Node 24, served by `nginx-unprivileged`, which also
  proxies `/api/` to the API. The browser therefore talks only to its own
  origin (no CORS), and security headers (CSP, nosniff, frame-deny) are set
  in one place.
- No secrets are baked into images. `../.dockerignore` excludes `.env*`.
- Host ports bind to `127.0.0.1` only. Postgres and Kafka are not published.

### D12. Configuration

Environment variables only, validated at startup with all errors reported
together. `ANTHROPIC_API_KEY` is optional: empty means the mock LLM (from
Milestone 3 on; see D30 for `LLM_PROVIDER`). The API logs only
`llm_provider` and `llm_model`, never the key.
Compose requires `.env` (via `${VAR:?}`) so the documented setup is
`cp .env.example .env`.

### D13. Testing strategy

- **Unit tests** (`make test-unit`): no containers, no network beyond
  loopback, no API key. Run with `-race`. Frontend tests run under Vitest +
  jsdom with `fetch` stubbed.
- **Integration tests** (`//go:build integration`): `testcontainers-go`
  starts a throwaway Postgres (same image as Compose). Migrations are
  applied, then readiness is checked against it. `make test` runs unit +
  integration, so **Docker is a required dev dependency**.
  *Alternative:* test against the Compose stack. Rejected: tests would
  depend on shared, mutable state and on the stack being up.
- Shared helpers for later milestones live in `../internal/testutil/pgtest`.

### D14. Linting

- Go: `golangci-lint` v2.15.0 with `standard` linters plus gosec, errorlint,
  contextcheck, noctx, bodyclose, sloglint, revive, and others
  (`../.golangci.yml`). The Makefile installs the pinned version into `bin/`
  with the project toolchain, because a linter built with an older Go
  refuses to analyse newer code.
  *Alternative:* the official container image. Rejected so that `make lint`
  works without Docker.
- Go commands use explicit package roots (`./cmd/... ./internal/...
  ./mock-provider/...`) instead of `./...`. An npm package under
  `../frontend/node_modules` ships a `.go` file, and `./...` would pick it up.
- Frontend: ESLint 10 flat config (`typescript-eslint` strict +
  `eslint-plugin-vue` recommended, `vue/no-v-html` as error) and `vue-tsc`
  in strict mode with `noUncheckedIndexedAccess`.
  `@vue/eslint-config-typescript` was dropped: it pulled in a dependency
  chain with high-severity `npm audit` advisories.

## Milestone 2 — User data and segmentation

### D15. Schema

Four tables (`migrations/000002`): `users`, `channel_consents`,
`cart_events`, `orders`, with foreign keys (`ON DELETE CASCADE`) and CHECK
constraints for enum-like columns (`locale IN ('tr','en')`,
`channel IN ('sms','email')`). Indexes follow the only query shape so
far, a per-user `EXISTS` bounded by time: `cart_events (user_id, added_at)`,
`orders (user_id, created_at)`, plus an expression index on
`city_key(city)`. Money is `numeric(12,2)` (seeded from integer cents, never
floats). The schema was written for this milestone's access patterns and
small data volume; it should be revisited with real volumes, e.g. a
pre-aggregated `order_count`, if counts become slow.

### D16. Fake recipients enforced by the database

`users.email` must match `^[a-z0-9._+-]+@example\.com$` (a reserved domain,
RFC 2606) and `users.phone` must match `^\+9995[0-9]{9}$`. `+999` is an
ITU-T E.164 country code that is not assigned to any country, so these
numbers cannot route to a real subscriber. Because these are CHECK
constraints, a real address cannot be stored even by a buggy seed or a
future import path. *Trade-off:* adding any other test domain needs a
migration, which is intended.

### D17. Consent as a separate table

`channel_consents (user_id, channel, granted, updated_at)`, primary key
`(user_id, channel)`, instead of boolean columns on `users`. A new channel
needs no `users` migration, each consent has its own `updated_at` for
auditing, and "never asked" (no row) is distinct from "declined"
(`granted = false`) in the data while both mean **no message**. The seed
produces both cases. Consent is applied inside
`postgres.AudienceRepository`, not by callers: the `segment` package
cannot produce a per-channel count without it.

### D18. Segment rule DSL: an allowlist plus a compiler, never LLM SQL

```json
{"version": 1, "conditions": [{"field": "city", "op": "eq", "value": "Istanbul"}]}
```

Rules will come from an LLM in Milestone 3, so they are untrusted input:

- **Strict parsing** (`segment.Parse`). The document must be exactly one
  JSON object. Unknown keys, duplicate keys (which `encoding/json` would
  silently resolve to the last occurrence), trailing data, `null`s, and
  wrong JSON types are rejected. Integers must be integer literals: `"30"`,
  `30.0`, and `3e1` are refused. All errors are collected with JSON paths
  (`conditions[1].value`). Messages never echo submitted values, so they are
  safe to return to clients and to feed back to the LLM for repair.
- **Allowlist** (`fieldSpecs`): 6 fields, each with its operators, value
  type, and range. 1–10 conditions, each field at most once. City values
  are 1–64 characters of letters, combining marks, spaces, hyphens, and
  periods; quotes, semicolons, and digits are rejected even though values
  are parameterized (defence in depth).
- **Compiler** (`segment.Compile`): each field selects a constant SQL
  fragment; the only text substituted into fragments is generated `$n`
  placeholders and constant comparison operators. Values go into `Args`.
  Tests compile hostile values that bypass `Parse` and assert that they
  appear only in `Args`, and an integration test executes them.
- **Normalization**: the accepted rule is returned in canonical form
  (fixed condition order, trimmed and de-duplicated values), so later
  milestones can store and hash it.

*Alternative rejected:* letting the LLM write SQL (even read-only, even
validated by a parser). That cannot be made safe: one missed construct
leaks data or bypasses consent, and the database role would need broad
read access.

*Known limitation:* conditions are AND-only, with `in` for alternatives.
No OR or nesting yet, which keeps the attack and validation surface small.
A field cannot repeat, so `order_count` cannot express a range.

### D19. Reference time is a parameter

"Within N days" is the closed interval `[now − N×24h, now]`, and both
bounds are bound parameters computed in Go from an injected clock
(`segment.NewService(counter, time.Now)`). SQL never calls `now()`, so
tests are deterministic and future-dated rows never match. Days are 24-hour
windows, not calendar days in the user's time zone.

### D20. City matching: `city_key()`

`lower()` is collation-dependent for Turkish İ/ı (`lower('İ')` is not `i`
under most collations), so `Istanbul` would not match `İstanbul`. The
migration defines one immutable, side-effect-free SQL function,
`city_key(text) = lower(translate(btrim(text), 'İIıŞş…', 'iiiss…'))`. The
compiled predicate applies it to both the column and the parameter values,
so there is a single folding implementation. An expression index on
`city_key(city)` keeps the filter indexable. *Alternative:* folding in Go
and storing a normalized column. Rejected because it needs the same
function in two languages and a write-path invariant. This is the only SQL
function in the schema: no triggers or procedures.

### D21. Seed: deterministic, idempotent, one-shot service

- `../cmd/seed` runs as a Compose one-shot after `migrate`, and `api` waits for
  it (`service_completed_successfully`), so previews never see a half
  seed.
- `math/rand/v2` PCG with a fixed seed (`SEED_RANDOM_SEED`); timestamps are
  relative to the run time and truncated to seconds. Same options ⇒ same
  data (unit test compares checksums; integration test compares database
  checksums across two fresh databases).
- **Idempotency:** one transaction holds `pg_advisory_xact_lock` and checks
  whether `users` is empty. If not, nothing is written. Data is bulk-loaded
  with `COPY`. A crash leaves nothing behind, and concurrent runs serialize.
  *Alternative:* `ON CONFLICT DO NOTHING` per row. Rejected because
  cart events and orders have no natural key, so a re-run would duplicate
  them. *Trade-off:* changing `SEED_USER_COUNT` later has no effect until
  the volume is reset.
- Twelve fixed scenario users cover the example segment from every side
  (each consent combination, cart-then-purchased, old purchase, cart
  outside the window, another city, long names). The remaining users are
  random: Istanbul ≈ 34%, consent ≈ 45% both / 20% SMS / 20% email / 15%
  none.

### D22. Preview endpoint

`POST /api/v1/segments/preview`, with the rule as the body. The body is
capped at 16 KB (`http.MaxBytesReader`; a maximal valid rule is far
smaller), and `Content-Type: application/json` is required (`415`
otherwise). Invalid rules return `400 {"error","details":[{path,message}]}`.
Oversized bodies also return `400`, as the milestone acceptance criteria
specify (`413` would be the more specific code). Unexpected errors are
logged with the request ID and returned as a generic `500`.

The Go API serves the versioned API under `/api/v1` itself, so the path
is the same whether called directly (`:8080`) or through the frontend
origin. nginx forwards `/api/v1/` unchanged, while the older `/api/` rule
(which strips the prefix) still serves `/api/healthz` for the M1 status
widget.

## Milestone 3 — AI campaign generation

### D23. LLM port, adapters, and where they live

`../internal/llm` holds only the port: a two-method `Client` interface
(`ExtractSegment`, `GenerateDrafts` with a delta callback), its request
types, and three sentinel errors (`ErrRefused`, `ErrIncomplete`,
`ErrUnavailable`). Adapters return **raw text**. Decoding and validation
belong to the consumer (`internal/campaign`), so no adapter can hand back
something that skips validation. The Claude adapter is infrastructure and
lives in `internal/platform/claude`. The deterministic mock lives in
`internal/llm/mockllm`. *Alternative:* declaring the interface inside
`campaign` (strict "consumer owns the interface"). Rejected because the
milestone asks for an `llm` package, and the port has no dependencies
either way. Streaming is in the interface now so M4's SSE needs no port
change. Fragments are display-only, and each carries its attempt number so
a client can discard fragments from a rejected attempt.

### D24. Structured outputs, with the backend as the authority

Every Claude call uses `output_config.format` with a JSON schema
(closed objects, enums for segment fields and operators, nullable rule).
`tool_choice` is not forced (it returns 400 on current models). The schema
is only a convenience: structured outputs cannot express length or range
limits, and a response can still be truncated or refused. The backend
therefore decodes strictly and validates everything. Strict decoding means
no unknown, missing, or duplicate keys and no trailing data. The
duplicate-key/trailing-data check moved from `segment` to a shared
`../internal/strictjson` package with unchanged behavior; the M2 tests still
pass. Size caps (16 KB segment, 32 KB drafts) apply before parsing. A
proposed rule goes through `segment.Service.Preview`, the same strict
parser, compiler, and consent-aware counter as hand-written rules, so model
output cannot bypass the M2 validator.

### D25. Model, effort, and no refusal fallback

The model comes from `ANTHROPIC_MODEL`, default `claude-sonnet-5-5` as
the milestone specifies. Extraction runs at effort `low` (a small
extraction task) and drafts at `medium` (short creative writing).
Thinking stays at the model default (adaptive). `max_tokens` is 8000 for
segments and 16000 for drafts, which leaves room for thinking on top of a
small answer. The adapter checks `stop_reason` before reading content:
`refusal` → `refused`, and `max_tokens` (or anything other than
`end_turn`/`stop_sequence`) → incomplete. **No refusal fallback** to
another model: the API offers a server-side fallback, but a refusal here
surfaces as the `refused` error so the marketer rephrases the brief. This
keeps behavior predictable and avoids sending a declined brief to a
second model.

The adapter is built with `option.WithoutEnvironmentDefaults()`. The key
comes only from config (the redacting `Secret` type), never from other
env vars or on-disk SDK profiles. Logs carry model, latency, token usage,
stop reason, and outcome. The SDK error string is not propagated because
it embeds the raw response body.

### D26. Bounded repair retry

When output fails validation, or the model stopped at `max_tokens`, the
service makes one more call (total `LLM_MAX_ATTEMPTS`, default 2, max 3).
That call carries the previous output and the validator messages, both as
delimited data. Validator messages never echo submitted values, so they are
safe to log and to send back. Refusals, timeouts, provider errors, and
cancellation are **not** repaired. A `max_tokens` stop is an error at the
adapter (as specified) but counts as a failed attempt in the repair loop:
truncated JSON is just invalid output, and asking for a shorter object
usually fixes it. If every attempt is truncated, the result is
`invalid_output`. An "only unsupported criteria" answer
is a valid result (`unsupported_brief`), not a repairable error. Transport
retries (429/5xx/connection) are the SDK's own (`LLM_SDK_MAX_RETRIES`,
honoring `retry-after`) and happen inside the per-call timeout
(`LLM_SEGMENT_TIMEOUT` 20 s, `LLM_DRAFTS_TIMEOUT` 60 s). Worst-case
latency is therefore attempts × call timeout. The AI routes get exactly
that plus 5 s as their HTTP timeout, so the service's own timeout fires
first and returns a clean `timeout`; other routes keep
`HTTP_REQUEST_TIMEOUT`. `httpserver.NewRouter(logger, 0)` now means
"timeouts per route group".

### D27. Prompt-injection defense

- Fixed system prompts. Nothing from a request is added to them.
- The brief is wrapped in `<brief>…</brief>`, with `<` and `>` replaced by
  their fullwidth forms (＜ ＞), so it cannot close its own section. HTML
  entities are deliberately not used: the model may echo brief text
  (e.g. "H&M") into drafts, and an `&amp;` would then fail the plain-text
  check. The prompt states that the content is data, not instructions.
- Briefs are capped at 1000 characters and must be valid UTF-8 without
  control characters.
- The model's only output channel is a JSON object with a fixed set of
  keys. Fields such as `approved` or `send_now` are unknown keys and are
  rejected. There is no code path from generation to approval or sending
  (those arrive in M4/M5 and require a human request).
- SQL is never accepted: rule values reach the database only as bind
  parameters, after the allowlist validator.

Tests cover injection briefs ("ignore previous instructions", "approve and
send", fake `</brief>` tags, SQL fragments) and hostile model outputs. The
result is always a validated proposal or a safe error code.

### D28. Personalization variables and content limits

Only `{{first_name}}` and `{{city}}` are allowed, written exactly so (no
spaces, no expressions, no other `{{`/`}}`). A strict syntax makes
rendering in M4/M5 trivial and leaves nothing for the model to smuggle
through a template engine. Limits are checked on a worst-case render:
`first_name` counts as 30 characters (the longest seeded name) and `city`
as 20 (longest seeded: 9). SMS ≤ 160, subject ≤ 80, email body ≤ 2000
(a product cap that keeps emails short and responses bounded). All counts
are runes. Content must be plain text: HTML tags and character references
are rejected, along with control characters (newlines only in bodies) and
bidirectional-override characters, which can make text display differently
from how it is stored. M4 re-checks against the longest real values in the
audience, and M5 re-checks each rendered message.

**GSM-7 vs UCS-2.** The specification says "160 characters", so the limit
counts characters. On real networks one non-GSM-7 character (ş, ğ, ı, İ,
emoji) makes the message UCS-2 with 70-character segments, so Turkish SMS
may span several segments. This is documented, not enforced.

### D29. Error taxonomy

`campaign.Error` carries a stable code, a client-safe message, and the
internal cause for logs. Codes and statuses: `invalid_brief` 400,
`unsupported_brief` 422 (with `unsupported_criteria`), `refused` 422,
`invalid_output` 502, `upstream_unavailable` 503, `timeout` 504.
Malformed requests are `invalid_request` (400/415). A caller cancellation
is returned as `context.Canceled`, not a generation error, and is
logged/recorded as 499. A caller deadline maps to `timeout`. Responses
never include model text, SDK errors, or validator output beyond the
codes above.

### D30. Provider selection and the mock

`LLM_PROVIDER` is empty by default: `claude` when a key is set, otherwise
`mock`. `LLM_PROVIDER=mock` keeps the mock even with a key, and
`LLM_PROVIDER=claude` without a key fails at startup instead of silently
using the mock. The mock is deterministic (same brief ⇒ byte-identical
output, streamed in 24-character chunks) and produces output that passes
the real validators. Failure modes (malformed JSON, unknown fields,
unsupported field, overlong SMS, unknown variable, refusal, provider error,
truncation, hang) can only be injected through the Go constructor's script,
so no brief can trigger them.

### D31. New dependency: `anthropic-sdk-go`

The official Go SDK (v1.84.0) provides typed requests, structured-output
parameters, SSE stream accumulation, retries with `retry-after`, and typed
API errors. Hand-written HTTP would duplicate all of that. Adapter tests
point the SDK at an `httptest` server via `option.WithBaseURL`. They cover
success, 429, 5xx/529 with retries, 400 without retry, malformed bodies,
the `refusal` and `max_tokens` stop reasons, and streamed deltas. No test
calls the real API.

## Milestone 4 — Campaign approval and content

### D32. Campaign state machine in the domain

`../internal/campaign` owns the statuses `draft → audience_ready →
drafts_ready → approved` and a transition table (`CanTransition`).
`drafts_ready → drafts_ready` is a regeneration. Content and channel edits
are allowed only in `drafts_ready`. Approved campaigns are immutable;
Milestone 5 added `approved → sending → completed` (and `approved →
completed` when nothing is left to send), made by the workers (D47).
Illegal actions return typed errors (`invalid_state`, 409). The database
repeats the status list in a CHECK constraint and requires `approved_by`
and `approved_at` exactly when the status is `approved`, `sending`, or
`completed`, so a half-approved row cannot exist even through a bug.

Creation runs segment extraction first and inserts the campaign directly
in `audience_ready`, with the `created` and `audience_proposed` events in
the same transaction. A failed or unsupported proposal stores nothing, so
the list never fills with dead campaigns. `draft` exists in the state
machine and the audit trail, not as a resting state.

### D33. Approval bound to version and content hash

`content_hash` is the SHA-256 of a canonical encoding: a fixed-order JSON
struct with a format version, the normalized segment rule, the channels in
canonical order, and the three message fields. The brief, counts, version,
and status are not part of it. Only the backend computes it.

Every write goes through `Store.Update(id, fn)`. It takes a row lock
(`SELECT … FOR UPDATE`), runs the domain decision, and writes with `WHERE
version = $locked`. Edits and approval must name the version the client
read (`version_conflict` otherwise). Approval must also name the content
hash the reviewer saw (`content_changed`), and the stored hash is
recomputed to detect tampering. Inside the same transaction approval:

1. re-parses the stored rule through the segment validator;
2. re-runs the content rules for the selected channels;
3. checks every limit with the longest real `first_name` and `city` among
   each selected channel's consented recipients, read in the transaction
   with the consent filter;
4. requires at least one selected channel with a consented recipient;
5. writes status, `approved_by`, `approved_at`, and the audit event.

Re-approving with the same hash is a no-op that returns 200, so retries
and double clicks are safe and record exactly one `approved` event.
Concurrent approvals serialize on the row lock (tested with 8 concurrent
callers against Postgres). Generation and the LLM have no path to approval.

Alternative considered: optimistic locking alone (`UPDATE … WHERE version`
without `FOR UPDATE`). The audience checks must see the campaign that will
be approved, and the lock makes that straightforward. The version
predicate stays as a second guard.

### D34. Edits store invalid content; approval is the gate

PATCH rejects only structurally bad input: unknown channels, invalid UTF-8
or NUL, or text over twice the field's limit. Content that breaks a content
rule (over a limit, unknown variable, HTML, empty) is stored and reported
in `validation`. Rejecting it would discard a marketer's work in progress,
and the preview, which renders stored content, could never show what is
wrong with it. Approval re-validates everything. An edit that changes
nothing does not bump the version, so autosave cannot invalidate a
reviewer's approval for no reason.

### D35. SSE over POST for generation

Draft generation is `POST /campaigns/{id}/drafts` with a
`text/event-stream` response, read with `fetch` and a `ReadableStream`.
`EventSource` only does GET and reconnects automatically. A dropped
connection would then silently start another paid model call.

- Deltas are extracted from the model's partial JSON by a lenient
  incremental scanner (`campaign/stream.go`). It emits only the three
  message fields, keeps escapes and UTF-8 intact across chunk boundaries,
  and never fails. A new `attempt` tells the client to discard the text of
  a rejected attempt.
- Drafts are stored only after full validation (`done` event). Errors
  after the stream starts are `error` events with the usual codes. Errors
  before it starts (404, 409) are ordinary JSON responses.
- One generation per campaign: a lease (`generation_token`,
  `generation_expires_at`) is taken in the database, so it holds across API
  instances and expires on its own if a process dies (generation timeout +
  15 s). Edits and approval are refused while it is held.
- Closing the connection cancels the request context and the model call.
  The lease is then released with a detached context, and the campaign
  keeps its previous content and status.
- The route has no request-timeout middleware; the service bounds the
  generation (`LLM_MAX_ATTEMPTS ×` the longer LLM timeout + 5 s). A
  heartbeat comment every 15 s keeps proxies from closing an idle stream.
  nginx already disables buffering for `/api/v1/`.

`EventSource` remains the plan for the read-only delivery dashboard
(Milestone 6), where automatic reconnection is wanted.

### D36. Audit log

`campaign_events` records action, actor, from → to status, and version for
creation, the audience proposal, generation, edits, and approval. It never
stores message content or recipient data. A trigger rejects updates, so
the log is append-only. Rows are removed only with their campaign, and
nothing in the application deletes campaigns.

### D37. No authentication (limitation)

The case does not require users, so approval takes a free-text
`approved_by` (1–100 characters, plain text). It is stored on the campaign
and as the actor of the `approved` event. Edits are attributed to
`marketer`. Anyone who can reach the API can therefore approve. A real
deployment needs authentication, with the authenticated identity replacing
`approved_by`.

### D38. Frontend structure and new dependency `vue-router`

`vue-router` (5.4.0, the official router) provides the list, new-campaign,
and review pages with real URLs, so reloading a review page shows the
persisted state. All API calls go through `src/api`: a shared client with
stable error codes, runtime guards that reject unexpected response shapes,
and a small fetch-based SSE reader. The backend owns status, validation,
rendering, and approval; the UI shows rune counts of the raw text as hints
and the backend's final counts from the preview. Edits autosave with a
600 ms debounce, one request at a time, followed by a preview refresh.
Approve is enabled only when everything is saved and the preview of the
current version passes. It sends one request even on repeated clicks.
No `v-html` (lint rule); all text renders as text.

The mock LLM paces its chunks (`LLM_MOCK_STREAM_DELAY`, default 40 ms), so
streaming is visible in mock mode. Tests set no delay, or a long one to
prove cancellation.

## Milestone 5 — Queue and reliable delivery

### D39. Deliveries materialized at approval, in the approval transaction

Approval's `Write` carries a `DeliveryPlan` (the segment predicate the
approval checks used, the selected channels, `max_attempts`). The campaign
store applies it in the same transaction as the status change: an
`INSERT … SELECT` of every user matching the predicate with granted consent
for the channel, `ON CONFLICT (campaign_id, user_id, channel) DO NOTHING`,
and, in the same statement, one outbox row per delivery created. Approval,
deliveries, and queue messages therefore commit or roll back together, and
a repeated approval creates nothing (tested with 8 concurrent approvals).

The audience is fixed at approval: a user who grants consent later is not
added. Revocation is still honoured at send time (D42).

*Amended by D57:* a campaign approved for a later time materializes its
deliveries at release, in the release transaction, not at approval.

Alternative considered: materializing in the worker after approval. It
would need its own idempotency and leave approved campaigns without
deliveries after a crash; the single transaction avoids both.

### D40. Transactional outbox, Kafka as transport only

The API never talks to Kafka. The worker's relay locks unpublished outbox
rows (`FOR UPDATE SKIP LOCKED`, so several relays split the work), produces
them with acks from all in-sync replicas (franz-go's idempotent producer),
and marks them published in the same transaction. A crash between produce
and commit republishes them: publishing is at least once.

A queue message carries only `{"delivery_id"}`. All state (status,
attempts, lease, next attempt, error code) lives in `deliveries`, so a
duplicate, stale, or replayed message cannot cause a send by itself.
Topics `deliveries.sms` and `deliveries.email` (6 partitions by default)
are created by the worker at startup with `kadm` (new dependency,
`github.com/twmb/franz-go/pkg/kadm`, the admin package of the Kafka client
already in use). Auto-creation stays disabled. Published outbox rows are
purged after 24 hours.

### D41. Exclusive claim with a lease; attempts as the fencing token

Processing starts with one statement:
`UPDATE deliveries SET status = 'processing', attempts = attempts + 1,
locked_until = clock_timestamp() + lease WHERE id = $1 AND status IN
('queued', 'retrying') AND next_attempt_at <= clock_timestamp() AND attempts
< max_attempts RETURNING …`. Concurrent claimers serialize on the row; one
wins (tested with 20 concurrent claims). No row means a duplicate message:
the consumer acknowledges it without doing anything. The first claim of a
campaign also moves it from `approved` to `sending` with an audit event,
in the same statement.

The outcome is written with `WHERE status = 'processing' AND attempts =
$claimed_attempt`. If the lease expired and another worker claimed the row,
the stale worker's outcome is discarded. The resend is safe because it
reuses the idempotency key (D44).

The lease (60 s) must exceed the rate-limit wait plus the provider timeout
(5 s); configuration requires at least twice the provider timeout. A
maintenance loop moves `processing` rows with an expired lease back to
`retrying` (`lease_expired`, attempt still counted), or to `failed`
(`retries_exhausted`) if that was the last attempt. All times use the
database clock (`clock_timestamp()`), never worker clocks.

When a worker is shut down mid-attempt before the provider answers, it
records `retrying` with the attempt given back (`interrupted`), so
shutdowns do not exhaust attempts. A message already sent then is
protected by the idempotency key.

### D42. Consent and content re-checked right before sending

After the rate-limit token is granted, the worker reads, in one query, the
recipient's current consent for the delivery's channel, the address, the
personalization values, and the campaign's approved content. No consent
means `failed (consent_revoked)` without a provider call. The content is
rendered for the recipient and re-validated by `content.RenderMessage`:
length after personalization (SMS 160, subject 80 runes), plain text, not
empty, and no `{{` or `}}` left (a value could contain them). A failure is
`failed (invalid_content)`. Reading consent after the rate-limit wait
rather than before puts the check as close to the send as possible; a
token spent on a revoked recipient is harmless.

### D43. Retry policy

| Outcome | Classification |
| ------- | -------------- |
| 2xx with a `message_id` | sent |
| network error, timeout, 408 | transient (`provider_timeout`) |
| 429 | transient (`provider_rate_limited`) |
| 5xx | transient (`provider_unavailable`) |
| 2xx without a valid body, 1xx/3xx | transient (`provider_invalid_response`) |
| other 4xx | permanent (`provider_rejected`) |

Transient failures are retried after `min(max(full_jitter_backoff,
Retry-After), RETRY_MAX_DELAY)`, with full-jitter backoff uniform in
`[0, min(cap, base·factor^(attempt−1))]` (1 s, ×2, 60 s cap). Full jitter
spreads deliveries that failed together, for example during a 429 storm.
`Retry-After` (delay-seconds or HTTP-date) is a lower bound, parsed values
are capped at 1 hour, and the policy cap bounds them further, so a broken
header cannot park a delivery. After `max_attempts` (5) the delivery fails
with `retries_exhausted`.

Retries are scheduled in the database (`next_attempt_at`). The maintenance
loop moves due `retrying` rows to `queued` and writes their outbox messages
in one statement, so retries take the same path (relay → claim → rate
limiter) as first attempts and stay inspectable in SQL.

Alternatives considered: Kafka retry topics with delays and a dead-letter
topic. They duplicate state already in Postgres, need one topic per delay
tier, and bypass nothing that the database approach cannot do. Possible
future improvements: a dead-letter topic for alerting, and manual re-drive
of `failed` rows.

### D44. Delivery semantics: effectively once, not exactly once

Processing is at least once: an outbox row can be republished, a Kafka
message can be redelivered after a crash or rebalance, and a lease can
expire mid-send. Duplicates are absorbed in three layers:

1. `UNIQUE (campaign_id, user_id, channel)`: at most one delivery per
   person, channel, and campaign.
2. The claim: only `queued` or due `retrying` rows are processed; `sent`
   and `failed` are final.
3. The provider idempotency key (the delivery id): a resend after an
   uncertain outcome (lost response, timeout, expired lease) returns the
   original result instead of delivering again.

Together this gives effectively-once successful delivery. It is not
exactly once: it relies on the provider honouring the key (the mock keeps
keys in memory, so a provider restart loses them), and a real provider
without idempotency support could deliver twice after a lost response.
The opposite failure exists too: a delivery whose last attempt's lease
expires is marked `failed` although the provider may have accepted it.

Kafka offsets are committed only after every message of a polled batch is
handled, and rebalances wait for that (`BlockRebalanceOnPoll`). Handling
means the delivery row reflects the outcome. A failing handler (database
down) is retried with backoff until it succeeds or the worker stops; on
SIGTERM the worker stops polling, gives in-flight work half of
`SHUTDOWN_TIMEOUT`, and does not commit unfinished batches.

### D45. Global rate limit: a Postgres token bucket per channel

`rate_limit_buckets` has one row per channel (`tokens`, `updated_at`). One
atomic `UPDATE` refills the bucket for the time elapsed since `updated_at`,
using `clock_timestamp()` evaluated once after the row lock, and takes a
token if one is available. PostgreSQL 18's `RETURNING OLD/NEW` reports the
pre-take level, so the caller learns either "granted" or how long until the
next token. Rate and burst are passed by each worker, not stored.

A token bucket admits at most `rate + burst` operations in any window of
one second, so configuration requires `rate + burst ≤ 100`. The defaults
are 90/s + 5. The headroom below 100 absorbs jitter between taking a token
and the provider receiving the request, which the database cannot control.
The integration test runs 4 limiter instances with 4 goroutines each and
checks every one-second window on the database's grant timestamps; in a
local run with 3 workers the mock provider observed at most 95 requests per
second per channel. Retries take tokens like first attempts.

Within one process a channel's waiters take turns (a semaphore), so N
goroutines waiting cost one bucket query per token, not N. Batches of
tokens per worker were not needed: at 100/s the single-row update is far
below what Postgres handles, and batching would let idle workers hold
tokens.

Alternatives considered: Redis (`INCR`/Lua or a GCRA) would add a new
service for something Postgres does at this scale; it is the natural next
step if the rate grows by orders of magnitude. A per-worker local limiter
(100/s divided by the worker count) breaks when workers scale or die.
Relying on the provider's 429s alone violates the requirement and wastes
attempts.

### D46. Mock provider design

The mock provider (`../internal/mockprovider`, served by `mock-provider/`)
accepts `POST /v1/sms` and `/v1/email` with a required `Idempotency-Key`
and stores messages in memory. It only accepts the fake recipient ranges
the `users` table enforces (D16), so even a bug upstream cannot "send" to
a real-looking address. Faults are configurable: uniform latency, 429 with
`Retry-After`, 503, permanent 422 for a fixed share of recipients, "accepted
but response lost" (stored, then the response stalls longer than the
client waits and ends in 504), and its own 100/s per-channel limit.

Each fault decision is a hash of the seed, the idempotency key, and the
attempt number for that key, not a shared random stream. With a fixed seed
the same request sequence per key gives the same outcomes however requests
from many workers interleave, and every retry gets a fresh decision, so
transient faults eventually clear. Concurrent requests with the same key
wait for the first one and return its result. `GET /v1/stats` reports
deliveries, suppressed duplicates, `duplicate_content` (same recipient and
content under a different key, the check for a double send the key could
not catch), and the maximum requests received in any one-second window.

### D47. Worker composition

The worker waits for Postgres and Kafka, creates the topics, then runs
three tasks under one errgroup: the outbox relay, the consumer group
(`delivery-workers`), and maintenance (lease recovery, retry scheduling,
campaign completion every second). If one task fails, the others stop and
the process exits non-zero, and Compose restarts it. Any number of
workers can run (`--scale worker=3`): they split partitions in the
consumer group, split outbox rows with `SKIP LOCKED`, and share the rate
limit in Postgres.

A campaign completes when its last delivery becomes `sent` or `failed`:
the worker that records a terminal outcome runs the completion check, and
the maintenance loop repeats it for all campaigns as a safety net. The
check only matches campaigns in `approved`/`sending` with no unfinished
deliveries, so it is idempotent and records one `completed` event.

## Milestone 6 — Live monitoring

### D48. Periodic aggregate snapshots, not per-row events

The dashboard shows a snapshot: per-channel counts for all five delivery
statuses, failures by error code, recent sends, and the campaign status,
read in one statement (`campaigns LEFT JOIN LATERAL (… GROUP BY channel,
status, failed code)`), so status and counts are mutually consistent. It
uses the existing `deliveries (campaign_id, status)` index from M5, so no
migration was needed.

Alternatives considered:

- **Streaming every delivery change** (per-row events from the worker or
  Kafka): cost grows with send volume (up to 200 events/s per campaign),
  clients must apply increments in order, and a missed or repeated event
  silently corrupts the numbers. Reconnection needs replay.
- **Postgres `LISTEN/NOTIFY` → SSE**: lower latency, but a trigger or a
  notify on every delivery update adds write-path cost to the hot loop,
  payloads are capped at 8 KB, and notifications are lost while the API is
  disconnected, so a periodic re-read is still needed for correctness.
- **Snapshots on a timer (chosen)**: bounded cost (one indexed query per
  campaign per interval), full state in every event, so reconnects and
  duplicates are harmless. Cost: up to one interval (default 1 s) of lag.

### D49. One shared poller per campaign

`monitoring.Hub` starts one poll loop for a campaign when its first viewer
subscribes and stops it when the last one leaves, so N viewers cost one
query per interval, not N. Each subscription holds only the latest
snapshot (a slow client skips intermediate ones; every snapshot is full
state). The SSE handler sends a snapshot only when it differs from the
last one it sent (ignoring `generated_at`). A per-campaign viewer limit
(`PROGRESS_MAX_VIEWERS`, 429 beyond it) bounds memory and goroutines.

A new viewer reads one direct snapshot on connect, which also turns an
unknown campaign into a 404 before the stream starts. Subscribing happens
first, so no change between the two is missed. Pollers are per API
process; with several API instances each polls once per campaign it has
viewers for.

`http.Server.Shutdown` does not cancel request contexts, so open streams
would hold shutdown until its timeout. `Hub.Close` is registered with
`RegisterOnShutdown` and closes every subscription's `Done` channel; the
streams end and the browser reconnects later.

### D50. EventSource for the read-only dashboard

The dashboard uses `EventSource` (unlike draft generation, D35): the
stream is a read-only GET, reconnecting is exactly what we want, and
reconnecting never triggers paid work. Event ids continue from
`Last-Event-ID` so they increase across reconnects, but the server keeps no
history: a reconnect simply gets a fresh full snapshot. A `retry: 2000`
field sets the reconnection delay. The UI first loads `GET /progress` (one
request, a clean 404/error state, no stream for completed campaigns), then
opens the stream; each event replaces the displayed state. On `complete`,
or on unmount, the client closes the `EventSource`, otherwise the browser
would reconnect to a stream that just ended.

### D51. Prometheus metrics: naming, cardinality, and ports

New dependency: `prometheus/client_golang` (the reference Go client; the
milestone specifies it). Each binary uses its own registry (Go runtime and
process collectors plus ours), not the global default.

- Names follow Prometheus conventions (`_total` counters, `_seconds`
  histograms, base units) and the milestone's list: HTTP RED metrics,
  delivery outcomes, provider latency and code classes, retries by reason,
  rate-limiter wait, outbox backlog, recovered leases, and LLM calls,
  latency, and validation failures.
- Labels take only bounded values: chi route patterns (`unmatched` when no
  route matched, so random paths cannot create series), known HTTP methods
  (`other` otherwise), status classes, channels, outcome and error-code
  constants, and LLM operations and outcomes. No campaign, delivery, or
  user ids, addresses, or message text.
- Domain packages stay free of Prometheus: `delivery.Metrics` and
  `campaign.LLMMetrics` are small observer ports with no-op defaults;
  `../internal/platform/metrics` implements them. The provider client
  reports the HTTP code class itself (`provider.Observer`), since only it
  sees the status.
- `http_request_duration_seconds` skips Server-Sent Event responses:
  their duration is a viewer's session length and would swamp the latency
  distribution. They are still counted in `http_requests_total`.
- `outbox_pending` is a global value counted by every worker each
  maintenance tick; aggregate it with `max`.
- Metrics tests were not written in this milestone (the test seams were
  agreed without them); the labels are fixed in code to the sets above.

### D52. Health for every service, no monitoring containers

The worker's and the mock provider's probes now share
`httpserver.ProbeRouter` (`/healthz`, `/readyz`, optional `/metrics`).
The mock provider has no dependencies, so its readiness is "serving";
Compose's healthcheck uses `/readyz` for all three services. The worker's
port 8081 is published on an ephemeral host port per replica so
`--scale worker=3` keeps working.

No Prometheus or Grafana containers: the case asks for metrics endpoints,
and scraping and visualization are the operator's choice. Adding them
would mean more images, config, and startup time for a local demo.

## Milestone 7 — Hardening and submission

### D53. Failure scenarios as integration tests with test-side faults

The scenario suite (`../internal/delivery/scenario_integration_test.go`)
drives the real claim, limiter, Postgres store, and provider client
against an in-process mock provider, with Kafka only where the queue
itself is under test (replay, shutdown and restart). Faults that the mock
provider cannot produce are injected from the test through the existing
ports:
- a `delivery.Store` wrapper whose `Record` "crashes" after the provider
  accepted the message;
- a `delivery.Provider` wrapper that holds a response until the worker's
  lease has expired;
- an HTTP middleware in front of the provider that logs every request's
  key, arrival time, status, and `Retry-After`.

Retry spacing becomes checkable by fixing the processor's jitter source
(`ProcessorConfig.Rand`). Each retry must then arrive no earlier than
`policy.Delay(attempt, Retry-After, 0.5)` after the previous request.

*Alternative:* fault-injection hooks in production code (e.g. a "crash
after send" flag in the worker). Rejected: the ports already allow it, and
production code stays free of test switches.
*Alternative:* scenario tests against the Compose stack. Rejected for the
reasons in D13; the stack is exercised by `make smoke` instead.

The rate-limit scenario lowers the limit to 20 + 5 per second so a few
hundred deliveries saturate it within seconds. The 95/s production value
is checked by the smoke run against the provider's own sliding-window
counter.

### D54. Smoke test as a Go program

`make smoke` runs `../cmd/smoke` against the running stack. It is not built
into any image.
*Alternative:* a shell script with `curl` and `jq`. Rejected: it would add
`jq` as a host requirement, SSE parsing in shell is fragile, and the Go
version decodes the API's own response types (`httpapi`, `mockprovider`),
so it breaks at compile time if they drift.

The provider's statistics are cumulative since it started, so the smoke
test compares differences before and after its campaign. It tolerates one
documented case: a send accepted on the last attempt whose response was
lost is delivered but recorded as `retries_exhausted`. Any other mismatch
fails the run.

### D55. Fuzzing and vulnerability checks

Go native fuzz targets cover the parsers that take untrusted input:
- the segment rule validator and compiler;
- the template parser and per-recipient renderer;
- the LLM output decoders.

Their seed corpora run in every `go test`, and `make fuzz` fuzzes each
target for a while. The properties are invariants rather than exact
outputs: a parsed rule compiles to placeholder-only SQL and round-trips,
and a rendered message is within limits, plain text, and free of
placeholder syntax.

The template length property assumes valid UTF-8. Invalid bytes on both
sides of a placeholder can join into one rune, but stored content and
values are always valid UTF-8 (JSON decoding, Postgres `text`).

`make vuln` runs a pinned `govulncheck` (installed into `../bin` like
golangci-lint). It is separate from `make lint` because it needs the
vulnerability database from the network.

## CI pipeline (bonus)

### D56. GitHub Actions: checks, tests, and image builds, no deploy

`../.github/workflows/ci.yml` runs its jobs in parallel. Each job calls a
Makefile target, so CI and local development use the same entry points:
`make fmt-check` (gofmt + goimports, which prints the diff), `make lint`
(golangci-lint with vet, staticcheck, gosec and the rest, plus ESLint and
vue-tsc), `make vuln` (govulncheck), and `make test`. The test job runs the
integration tests against real Postgres and Kafka through the runner's
Docker. The image job builds every Compose image with `push: false`. A broken
Dockerfile or build context fails the pipeline, and nothing is published.

- Go comes from the `../go.mod` toolchain directive and Node is pinned to the
  frontend image's version, so CI does not drift from local builds or
  images.
- Third-party actions are pinned to commit SHAs (tag in a comment),
  `permissions` is `contents: read`, and checkout does not persist
  credentials. The workflow needs no secrets because the tests use the mock
  LLM.
- The image matrix lists build arguments that are also in
  `../docker-compose.yml`. *Alternative:* `docker compose build`. Rejected
  because it needs a `.env`, builds serially, and caches less well than
  per-image Buildx GHA cache scopes. When a service is added, the matrix
  needs to be updated too.
- Format, lint, and vulnerability checks are separate jobs, so a failure
  points at its own category. golangci-lint also reports formatting
  issues, but `fmt-check` prints the exact diff. govulncheck needs the
  network and can fail when a new advisory is published without any code
  change. That is acceptable because it is the intended signal.
- One `../bin` cache, keyed on the Makefile and `go.mod`, holds the pinned
  golangci-lint and govulncheck builds.
- Frontend formatting is not checked, because no formatter is configured.
  Adding Prettier would be a new dependency and a repo-wide reformat.
- The smoke test is not in CI. It needs the full Compose stack running.

## Milestone 8 — Scheduled campaign delivery (bonus)

### D57. Approve for later; materialize the audience at release

Approval takes an optional `scheduled_at`. With it, approval runs every
check (version, content hash, rule and content re-validation, lengths for
the real audience, at least one recipient) while a human is present,
records the approver, and moves the campaign to `scheduled` without a
delivery plan. At the scheduled time a worker calls `Service.Release`,
which locks the campaign and, in one transaction, repeats the same checks
(the shared `checkApproval`, not a copy) and applies the delivery plan the
checks produced, moving the campaign to `approved`. From there, sending is
exactly as before. D39's single transaction still holds; it is just the
release transaction instead of the approval one.

*Alternative considered:* create the deliveries at approval and hold them
(a future `next_attempt_at`, or a `held` status). Rejected. The outbox
publishes at once, the claim refuses a delivery that is not due, and the
processor would drop the job as a duplicate, so held deliveries need a new
status, a second release path for them, and the outbox written later
anyway. The audience would also be frozen at approval: consent changes
before sending would only be caught per message, and users who newly match
would be missed. Materializing at release reuses the existing write path
unchanged.

### D58. Reference time is the scheduled time

At release the segment is compiled with the scheduled time as reference
(D19), not with approval time and not with the moment a worker gets to
it. "Added to cart in the last 30 days" therefore means the 30 days before
the intended send, and a release a few seconds late selects the same
audience as an on-time one. Approval compiles with its own time, so its
counts are an estimate. The UI labels them as such.

### D59. Expire instead of sending late; `release_failed` instead of a partial send

A due campaign that is not released within `SCHEDULE_RELEASE_GRACE`
(default 15 minutes; for example all workers were down) moves to
`expired` and sends nothing. Scheduled offers are usually time-sensitive,
and a reminder hours late is worse than none. The marketer can cancel and
approve again for a new time. Config validation requires the grace to
exceed `DELIVERY_SCAN_INTERVAL`, so a campaign cannot expire just because
the loop had not run yet.

If a release-time check fails (nobody left after consent changes, content
too long for a recipient who newly matches, rule no longer valid), the
campaign moves to `release_failed` with the error code, message, and field
errors stored on the row (`release_failure`, no content or PII). Nothing is
sent, and there is no automatic retry: the cause is in the data or the
content, so a retry would fail the same way. Cancel returns it to drafts.

Infrastructure errors during a release (database unavailable) are not
`release_failed`: the transaction rolls back and the next tick retries,
until the grace period runs out.

### D60. Release concurrency: database selects, the service decides

The due query (`status = 'scheduled' AND scheduled_at <= clock_timestamp()`,
oldest first, `FOR UPDATE SKIP LOCKED`, partial index on `scheduled_at`) uses
the database clock, so worker clock skew does not move a release, and skips
campaigns another worker is releasing or a human is canceling. The lock ends
with the statement. `Release` then takes the campaign row lock (`Store.Update`)
and decides with the service clock: not `scheduled`, or not yet due, is a
no-op. Concurrent releases therefore produce one release event and one set
of deliveries (tested with 8 concurrent callers), and a release racing a
cancel or reschedule has exactly one winner; the loser gets `409
invalid_state` (cancel) or does nothing (release). Clock skew between a
worker and the database can delay a release by the skew; it cannot cause an
early one beyond it.

Release is wired into the existing maintenance loop (`delivery.Releaser`,
implemented by `campaign.Service`) instead of a separate loop: one interval,
one batch size, no new process. The worker builds a campaign service with no
LLM; `Release` never generates content.

### D61. Reschedule and cancel

Both take the version and content hash the actor saw and a free-text actor
name (validated like `approved_by`; there is no authentication, D37), and
run under the campaign lock. Reschedule is allowed only from `scheduled` and
keeps the approval of the unchanged content. Cancel is allowed from
`scheduled`, `release_failed`, and `expired`. It clears the approval and the
schedule and bumps the version, so a later approval needs a fresh review.
Re-sending the same approval to a scheduled campaign stays idempotent and
does not move the time; moving it is reschedule's job. Generation is limited
to `audience_ready` and `drafts_ready` *before* approval. The new `scheduled →
drafts_ready` edge (cancel) must not become a way to regenerate approved
content.

Scheduled times are RFC 3339 with an explicit offset; the backend rejects a
time without one rather than guessing a zone. An explicit `null` for
`scheduled_at` is rejected rather than read as "send now": only leaving the
field out sends immediately, so a client that loses its time value fails
loudly instead of sending early. The UI converts the
marketer's local `datetime-local` value with the browser's offset for that
date (so daylight-saving changes are handled, and a time skipped by one is
rejected) and shows times with their exact offset.

### D62. Monitoring and metrics for scheduling

The progress snapshot carries `scheduled_at`; a scheduled campaign streams
zero deliveries until its release and then its progress, so the dashboard
switches without a reload. `expired` is final for the stream like
`completed`; `release_failed` is not, because a cancel may follow. Two
worker metrics: `campaign_releases_total{outcome}` (`released`,
`release_failed`, `expired`, all initialized to zero so alerts work from the
start) and `campaign_release_lateness_seconds` (scheduled time to release
decision, every outcome). Neither has campaign-level labels.
