# Smart Campaign Composer — Milestone Prompts

Working file (gitignored). One prompt per milestone, drafted and reviewed together,
then pasted into Claude Code to run that milestone. Status flow per prompt:
`outline → draft → final`.

---

## 1. Analysis

### 1.1 Things worth flagging

- **M2 vs M3 ordering.** M2's deliverable says "a brief produces a validated segment",
  but Claude arrives in M3. M2 therefore covers *segment-rule JSON → validation →
  parameterized SQL → recipient count*, using a hand-written rule in place of the LLM.
  M3 adds the brief → rule step in front of it.
- **Global rate limit (100 msg/s per channel, across all workers)** needs shared state.
  Kafka and Postgres are already in the stack; Redis would be a new dependency.
  Recommendation: Postgres-backed token bucket per channel (tokens acquired in batches),
  and retries go through the same limiter. Confirmed when drafting M5.
- **SMS 160 characters and Turkish letters.** ş/ğ/ı force UCS-2 encoding (70-char
  segments) on real networks. The spec says 160 characters → count runes and document
  the GSM-7 caveat.
- **Personalization can break limits.** `{{first_name}}` length varies, so validate the
  worst case at approval (longest value in the audience) *and* re-validate per recipient
  right before sending.
- `CLAUDE.md` imports `@project.md`, which is gitignored, so anyone cloning the repo
  won't have it.

### 1.2 Per milestone: features · technologies · techniques

| # | Features introduced | Technologies | Key techniques |
|---|---|---|---|
| M1 Foundation | Repo layout, config loading, `/healthz` `/readyz`, Compose stack skeleton, Makefile, lint config, `.env.example`, README/DECISIONS stubs | Go (`go-chi/chi` router, `log/slog`), Docker multi-stage builds, Compose (Postgres, Kafka KRaft, migrate one-shot, api, worker, mock-provider, frontend), golangci-lint, Vite + Vue 3 + TS, Vitest | Healthchecks + `depends_on: service_healthy`, graceful shutdown via `signal.NotifyContext`, env config with validation, dependencies flowing inward |
| M2 Users & segmentation | Schema: users, channel consents, cart events, orders; deterministic seed of ~5k fake users; segment rule DSL; audience count endpoint | PostgreSQL, `jackc/pgx/v5` + pgxpool, `golang-migrate/migrate` SQL migrations, gofakeit or a hand-rolled seed | Allowlisted field → operator → value-type registry, rule compiler to **parameterized SQL**, consent filter built into every audience query, strict JSON decoding (`DisallowUnknownFields`), indexes for city and cart/order recency |
| M3 AI generation | `LLMClient` interface, Claude adapter, deterministic mock, brief → segment rule, brief → SMS/email drafts | `anthropic-sdk-go`, model from env (`ANTHROPIC_MODEL`, default `claude-sonnet-5-5`), structured outputs (`output_config.format` JSON schema) | Fixed system prompt, brief passed as delimited *data* (prompt-injection defense), schema + business validation (limits, allowed `{{vars}}`, size caps), bounded repair retry with validation errors fed back, timeouts/cancellation via `context`, error taxonomy (malformed / timeout / API / refusal), golden tests with the mock |
| M4 Approval & content UI | Campaign create page, segment + count preview, **streamed** draft generation, editing, personalization preview with live character counts, explicit approval | Vue 3 Composition API, TS strict, vue-router, `EventSource`/SSE, Go SSE handler (`http.Flusher`) | Backend-owned campaign state machine, optimistic locking (`version`), approval bound to a content hash (edit after approval ⇒ re-approve), idempotent approve endpoint, SSE cleanup/reconnect, no `v-html` |
| M5 Queue & delivery | Approval → delivery records → Kafka jobs; worker; mock provider with failures/latency/429; retries; global rate limit; idempotency | Kafka (`franz-go`), Postgres, standalone mock-provider Go service | **Transactional outbox**, unique `(campaign_id, user_id, channel)`, delivery id as provider idempotency key, lease-based claiming (`processing` + `locked_until`) for crash recovery, exponential backoff + full jitter, `Retry-After`, transient vs permanent errors, bounded attempts, consent + render re-check right before send |
| M6 Live monitoring | Per-campaign dashboard (queued/processing/sent/failed/retrying), `/metrics`, real-time updates | Prometheus `client_golang`, SSE (optionally Postgres LISTEN/NOTIFY → SSE), Vue dashboard | Aggregated status counts (not per-row events), RED metrics for provider + LLM, rate-limiter wait histogram, low label cardinality, readiness vs liveness |
| M7 Hardening | Failure-scenario tests, consent, rate limit, duplicate prevention, API validation, clean start from scratch, final docs | testcontainers-go (or Compose-backed) integration tests, `go test -race`, Vitest | Fault injection through mock-provider config, worker kill/redelivery tests, rate measured over a window, README/DECISIONS completion, honest limitations list, session export check |

### 1.3 Cross-cutting decisions

Each is recorded in DECISIONS.md by the milestone that makes it.

- `go-chi/chi` router (net/http-compatible, middleware); no ORM (`jackc/pgx` + SQL);
  `golang-migrate/migrate` for migrations; `franz-go` for Kafka; no Redis.
- Docker is a required dev dependency. `make test` runs Go unit + integration tests
  (Postgres/Kafka via testcontainers-go) and frontend tests; `make lint` may run
  golangci-lint from its container image. Neither needs `ANTHROPIC_API_KEY`.
  `make test-unit` is the fast loop without containers.
- Delivery semantics: at-least-once transport; effectively-once *successful* delivery
  through DB uniqueness + provider idempotency key; exactly-once limits documented.

---

## 2. Prompt template

```
# Milestone N — <name>
STATUS: outline | draft | final
## Goal                 (1–2 sentences, user-visible outcome)
## Context              (what exists from earlier milestones; relevant CLAUDE.md sections)
## Scope                (numbered features)
## Out of scope         (explicitly the later milestones' work)
## Technical decisions  (chosen tech + why; what goes into DECISIONS.md)
## Acceptance criteria  (checkable statements = scope of work)
## Required tests       (positive, negative, edge cases)
## Verification         (exact commands and what to observe)
## When done            (report format, no auto-commit)
```

---

## 3. Milestone prompts

### Milestone 1 — Project foundation

STATUS: final

```
# Milestone 1 — Project foundation

## Goal
One command (`docker compose up`) starts the whole skeleton stack, health checks are
green, and `make test` and `make lint` pass. No business features yet.

## Context
Greenfield repository. Follow CLAUDE.md and .claude/rules/*. Inspect the repo first,
then state your plan briefly before writing code.

## Scope
1. Go module and layout: cmd/api, cmd/worker, internal/{config,platform/httpserver,health},
   mock-provider/ (own main package), migrations/ (one baseline migration),
   frontend/ (Vite + Vue 3 + TypeScript strict).
2. Configuration from environment variables with validation. ANTHROPIC_API_KEY is
   optional (empty ⇒ mock LLM in later milestones) and never logged. Add .env.example
   with placeholders only.
3. API on go-chi: GET /healthz (liveness), GET /readyz (Postgres and Kafka reachable),
   request ID / recoverer / timeout middleware, structured JSON logging with slog,
   server timeouts, graceful shutdown.
4. Worker: starts, checks its dependencies, logs readiness, shuts down gracefully.
   No consuming yet.
5. Mock provider: GET /healthz only for now.
6. docker-compose.yml: postgres, kafka (KRaft, single node), migrate (one-shot),
   api, worker, mock-provider, frontend. Healthchecks and depends_on conditions.
   No secrets baked into images.
7. Makefile targets: up, down, test, test-unit, lint, fmt. `make test` runs Go unit +
   integration tests (testcontainers-go; Docker required) and frontend tests.
   `make test-unit` runs only tests that need no containers.
8. golangci-lint config (may run from its official container image);
   frontend eslint + vue-tsc + vitest.
9. README (setup, layout, commands), DECISIONS.md (stack, layout, test strategy).
   Update the "Commands" and "Repository Layout" sections of CLAUDE.md to match reality.

## Out of scope
Schema beyond the baseline migration, seed data, LLM, campaigns, Kafka topics/consumers,
dashboard.

## Technical decisions
go-chi/chi router, log/slog, jackc/pgx/v5 (pgxpool), golang-migrate/migrate (run by the
one-shot migrate service), franz-go Kafka client, testcontainers-go, Vitest. Docker is a
required dev dependency. Justify each in DECISIONS.md. If you disagree with one, propose
the alternative before implementing.

## Acceptance criteria
- `docker compose up` from a clean clone (with only .env copied from .env.example)
  brings every service to healthy.
- GET /healthz → 200. GET /readyz → 200 when dependencies are up, 503 without internal
  details when they are not.
- SIGTERM stops api and worker within a bounded timeout.
- `make test` and `make lint` pass without ANTHROPIC_API_KEY (Docker available).
- `make test-unit` passes without Docker.

## Required tests
- Config validation: missing and invalid values.
- Health/readiness handlers with fake checkers: ok, failing, timeout.
- One integration test: migrations apply to a testcontainers Postgres and readiness
  reports healthy against it.
- Frontend: one smoke component test.

## Verification
make lint; make test; docker compose up -d; docker compose ps;
curl both health endpoints; docker compose stop postgres → /readyz returns 503.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

---

### Milestone 2 — User data and segmentation

STATUS: final

```
# Milestone 2 — User data and segmentation

## Goal
The database holds a few thousand realistic fake users with purchase history and channel
consent, and a segment rule (JSON) is validated, compiled to parameterized SQL, and returns
an accurate per-channel recipient count. Example target: "users in Istanbul who added to
cart in the last 30 days but did not purchase".

## Context
M1 is done: chi API, worker, Compose stack, golang-migrate migrate service, pgx pool,
testcontainers-based `make test`. Read CLAUDE.md "AI Safety" and "Campaign Safety" and
.claude/rules/security.md: the rule format built here is what Claude will produce in M3,
so treat every rule as untrusted input. In this milestone rules are hand-written.
Inspect the M1 code first and state your plan briefly.

## Scope
1. Migrations (golang-migrate, up + down):
   - users: id, first_name, last_name, email (unique), phone (unique), city, locale
     (tr|en), created_at.
   - channel_consents: user_id, channel (sms|email), granted, updated_at;
     primary key (user_id, channel).
   - cart_events: id, user_id, product_sku, added_at.
   - orders: id, user_id, total_amount, created_at.
   - Indexes supporting city filters and cart/order recency lookups; foreign keys;
     CHECK constraints for enum-like columns.
2. Seed: `cmd/seed`, run by a one-shot Compose service after migrate.
   - Deterministic (fixed random seed, configurable count, default 5000).
   - Idempotent: running it twice does not duplicate users.
   - Clearly fake data: emails on a reserved domain (example.com), phone numbers in one
     documented fake range. Turkish and English names, including names with Turkish
     characters and a few long names (used later for SMS-limit tests).
   - Realistic distributions: Istanbul is the largest city among 6–10 cities;
     mixed consent (some users with none, some with one channel); cart events and orders
     spread over the last ~90 days, timestamps relative to seed time.
   - Guaranteed coverage of the example segment: Istanbul cart-abandoners with and
     without consent, plus users who added to cart and then purchased.
3. Segment rule DSL in `internal/segment` (no HTTP or SQL driver imports in the domain):
   {"version": 1, "conditions": [{"field": "...", "op": "...", "value": ...}]}
   - Conditions are ANDed. 1–10 conditions. A field may appear only once.
   - Allowlisted fields, operators and value types:
     - city: eq | in — strings, 1–64 chars, at most 20 values, case-insensitive match.
     - locale: eq | in — tr | en.
     - added_to_cart_within_days: eq — integer 1–365 (user has ≥1 cart event in window).
     - no_purchase_within_days: eq — integer 1–365 (user has no order in window).
     - purchased_within_days: eq — integer 1–365.
     - order_count: gte | lte — integer 0–10000.
   - Anything else (unknown field/operator, wrong type, out of range, extra JSON keys,
     unsupported version) is rejected with field-level errors.
4. Compiler: validated rule → SQL with $n placeholders and an args slice. Field → SQL
   fragments come from a constant registry; user values are only ever passed as args.
   The reference time ("now") is a parameter, not SQL now(), so tests are deterministic.
5. Audience counting: per channel (sms, email) count only users with granted consent for
   that channel, plus the total matching users regardless of consent. Consent filtering
   lives in the repository query builder, not in callers.
6. Endpoint: POST /api/v1/segments/preview with a rule → normalized rule + counts
   {"total": n, "sms": n, "email": n}. Request body size limit (e.g. 16 KB), strict JSON
   decoding (unknown fields rejected), 400 with field-level errors, 500 without
   internal details.
7. Docs: README (seed, segment preview example with curl), DECISIONS.md (schema, DSL
   design, why an allowlist + compiler instead of LLM SQL, consent modeling).

## Out of scope
LLM / brief parsing (M3), campaigns, message content, personalization, frontend pages,
delivery. Returning recipient lists (counts only).

## Technical decisions
- Consent as a separate table (extensible to new channels, has updated_at) rather than
  boolean columns — record in DECISIONS.md.
- AND-only conditions with `in` for alternatives; no OR/nesting yet (smaller attack and
  validation surface) — record as a known limitation.
- Hand-written query builder; no SQL builder library unless you justify it first.

## Acceptance criteria
- Fresh `docker compose up` migrates, seeds ~5000 users, and the API serves previews.
- The rule {city eq Istanbul, added_to_cart_within_days 30, no_purchase_within_days 30}
  returns counts that match an independently written SQL query over the same data.
- Users without consent for a channel are never counted for that channel.
- Malicious or invalid rules (SQL in values, unknown fields/operators, wrong types,
  out-of-range numbers, duplicate fields, oversized bodies) return 400 and never reach
  the database as SQL text.
- Migrations apply up and down cleanly.

## Required tests
- Validator table tests: every allowed field/op; every rejection class listed above;
  injection strings such as "Istanbul'; DROP TABLE users;--" stay plain values.
- Compiler tests: generated SQL contains only placeholders for values; args match.
- Integration (testcontainers Postgres): fixed small fixture with known expected counts
  per channel, including consent edge cases and the cart-then-purchased case; reference
  time injected.
- Seed: same seed ⇒ same data (e.g. checksum of users); second run adds nothing.
- Handler tests: 200 shape, 400 field errors, unknown JSON field, body too large.

## Verification
make lint; make test; docker compose up -d; check seeded row counts with psql;
curl the preview endpoint with the example rule and with an invalid rule.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

### Milestone 3 — AI campaign generation

STATUS: final

```
# Milestone 3 — AI campaign generation

## Goal
A marketer's natural-language brief (Turkish or English) becomes (1) a validated segment
rule with recipient counts and (2) SMS and email drafts that pass strict backend
validation. Invalid, unsafe or malformed model output is rejected safely. Everything
works and is tested without an API key through a deterministic mock LLM.

## Context
M1 (stack, chi API, testcontainers tests) and M2 (schema, seed, segment DSL validator,
compiler, POST /api/v1/segments/preview) are done. Read CLAUDE.md "AI Safety" and
.claude/rules/security.md "LLM output". Use the `claude-api` skill for current
anthropic-sdk-go usage — do not guess SDK names. Inspect the M2 segment package first
and state your plan briefly.

## Scope
1. Package `internal/llm`: a small interface owned by the consumer side, e.g.
   - ExtractSegment(ctx, brief) → raw segment proposal
   - GenerateDrafts(ctx, brief, audienceSummary, onDelta func(string)) → raw drafts
   Streaming is part of the interface now (deltas via callback or channel) because M4
   streams drafts to the UI; the final value is the only thing that is validated/used.
2. Claude adapter (anthropic-sdk-go):
   - Model from ANTHROPIC_MODEL (default claude-sonnet-5-5); effort set explicitly
     (low or medium; record the choice); max_tokens sized per call.
   - Structured outputs via output_config.format with a JSON schema per call
     (no forced tool_choice — it returns 400 on current models).
   - Drafts call uses streaming; segment call may be non-streaming.
   - Check stop_reason before reading content: refusal and max_tokens are errors.
   - SDK retries set explicitly (small number) and a per-call context timeout
     (configurable, e.g. 20s segment / 60s drafts). Cancellation propagates.
   - API key only from config; never logged. Log model, latency, token usage, stop
     reason, outcome — not the brief or generated text.
3. Deterministic mock LLM, selected when ANTHROPIC_API_KEY is empty (or LLM_PROVIDER=mock):
   - Keyword-based rule extraction covering the example brief (İstanbul / sepet /
     30 gün / alışveriş yapmayan) and a few others; fixed Turkish and English drafts.
   - Streams drafts in deterministic chunks.
   - Test-only failure modes (malformed JSON, unknown fields, overlong SMS, unknown
     variable, refusal, timeout, API error). Not reachable through user input.
4. Output contracts (JSON):
   - Segment: {"rule": <M2 DSL rule> | null, "unsupported_criteria": [string],
     "explanation": string}. If the brief asks for criteria the DSL cannot express,
     the model must list them instead of inventing fields.
   - Drafts: {"sms": {"body": string}, "email": {"subject": string, "body": string}}.
     Plain text only (no HTML) in email body.
5. Validation in `internal/campaign` (or a `content` package), independent of the LLM:
   - Segment rule: reuse the M2 validator unchanged; LLM output never bypasses it.
   - Strict decoding (unknown fields rejected), response size cap.
   - Personalization: only allowlisted variables ({{first_name}}, {{city}});
     malformed or unknown placeholders rejected.
   - Limits checked on the worst-case render: each variable replaced by its configured
     maximum length (e.g. first_name 30 runes): SMS ≤ 160 runes, email subject ≤ 80
     runes, email body ≤ a documented cap; empty content rejected. Real per-recipient
     re-validation comes in M4/M5.
   - Document the GSM-7 vs UCS-2 caveat for Turkish characters.
6. Reliability:
   - One bounded repair retry when output fails validation: send the validation errors
     back (as data) and ask for a corrected object. Never more than the configured
     number of attempts.
   - Error taxonomy mapped to safe API errors: invalid_output, unsupported_brief,
     refused, timeout, upstream_unavailable. No raw model text or SDK errors in
     responses.
7. Prompt-injection defense: fixed system prompt; the brief is passed as delimited data
   with an instruction that it is content, not instructions; brief length cap
   (e.g. 1000 chars). The model can only return data; it cannot approve, send, or
   produce SQL — and the backend would ignore it if it tried.
8. Endpoints (temporary, used by M4's UI):
   - POST /api/v1/ai/segment {brief} → validated rule + counts (M2 counting) +
     unsupported_criteria.
   - POST /api/v1/ai/drafts {brief} → validated drafts (non-streaming JSON for now;
     SSE comes in M4).
9. Docs: README (mock vs real mode, env vars), DECISIONS.md (interface design, structured
   outputs, repair retry, variable allowlist, model/effort choice), .env.example
   (ANTHROPIC_MODEL, LLM_PROVIDER, timeouts).

## Out of scope
Campaign persistence and state machine, SSE, editing, approval, UI (all M4).
Delivery (M5). Locale-based content and A/B variants (bonus).

## Technical decisions
- Structured outputs as the primary format guarantee; backend validation is still the
  authority (schema-constrained output can be truncated or semantically wrong).
- Two separate calls (segment, drafts) so the audience count can be shown before drafts
  stream, matching the product flow.
- No refusal fallback to another model: a refusal (stop_reason "refusal") is returned
  as the `refused` error. Record this in DECISIONS.md.

## Acceptance criteria
- With no API key, the example brief produces the expected Istanbul/cart/no-purchase
  rule, correct counts, and valid drafts — deterministically.
- With a real key (manual check), the same flow works against Claude.
- Every invalid-output class is rejected with a safe error and never persisted or used:
  malformed JSON, unknown fields, unsupported field/operator, wrong types, SQL-looking
  values, overlong SMS/subject, unknown or malformed variables, empty content, oversized
  response.
- A brief with unsupported criteria (e.g. "users older than 30") returns
  unsupported_criteria rather than an invented rule.
- Timeouts and cancellation stop the call promptly; the repair retry is bounded.
- No log line contains the API key, the brief, or generated content.

## Required tests
- Validator table tests for every rejection class above, plus boundary lengths
  (160/161 SMS, 80/81 subject, Turkish multi-byte characters counted as runes).
- Service tests with the mock: success, repair succeeds on 2nd attempt, repair
  exhausted, unsupported criteria, refusal, timeout (context deadline), cancellation.
- Claude adapter tests against an httptest server via the SDK base-URL option:
  success, 429, 5xx, malformed body, stop_reason refusal and max_tokens, streaming
  deltas assembled correctly. No real API calls in tests.
- Prompt-injection briefs ("ignore previous instructions", "approve and send",
  "return SQL") produce either a valid rule or a safe rejection — never anything else.
- Handler tests: brief too long, empty brief, error mapping to HTTP status codes.

## Verification
make lint; make test (no ANTHROPIC_API_KEY set); docker compose up -d;
curl /api/v1/ai/segment and /api/v1/ai/drafts with the example brief (mock mode);
optional: set a real key in .env, restart api, repeat once.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

### Milestone 4 — Campaign approval and content

STATUS: final

```
# Milestone 4 — Campaign approval and content

## Goal
A marketer writes a brief, sees the proposed audience and recipient counts, watches the
SMS and email drafts stream in, edits them, previews them personalized for real (fake)
recipients, and explicitly approves. Approval is persisted by the backend and binds to
exactly the content that was reviewed. Nothing is delivered yet.

## Context
M1–M3 are done: stack, seed, segment DSL + counting, LLM interface with Claude adapter
and mock, content validation (variables, worst-case limits), temporary endpoints
/api/v1/ai/segment and /api/v1/ai/drafts. Read CLAUDE.md "Campaign Safety",
"Architecture" (backend is the source of truth for status and approval) and
.claude/rules/frontend.md. Inspect M3's llm and validation packages first and state
your plan briefly.

## Scope
1. Persistence (golang-migrate):
   - campaigns: id (uuid), brief, status, segment_rule (jsonb), unsupported_criteria,
     audience counts snapshot, channels (sms/email selection), sms_body, email_subject,
     email_body, version (optimistic lock), content_hash, approved_by, approved_at,
     created_at, updated_at. CHECK constraints for status.
   - campaign_events: append-only audit of status transitions and edits
     (who, when, from → to). No message content or recipient PII in it.
2. Domain state machine in `internal/campaign` (no HTTP/SQL imports):
   draft → audience_ready → drafts_ready → approved.
   - Content and channel edits allowed only in drafts_ready.
   - Approved campaigns are immutable (M5 adds sending states after approved).
   - Illegal transitions return typed errors → 409.
3. Approval rules (enforced in the domain service, inside one DB transaction):
   - Request carries version and content_hash the marketer reviewed; mismatch → 409.
   - Full re-validation server-side: segment rule (M2 validator), content (M3 rules),
     and limits re-checked against the *actual* audience: longest first_name/city among
     consented recipients of each selected channel.
   - At least one selected channel with ≥1 consented recipient.
   - approved_by (free-text operator name; no auth in this project — record as a
     limitation) and approved_at persisted together with the status change.
   - Idempotent: approving an already approved campaign with the same content_hash
     returns 200 with the same result; a different hash returns 409.
   - There is no code path where the LLM or the generation flow approves.
4. API (replace the temporary M3 endpoints):
   - POST /api/v1/campaigns {brief} → creates campaign, runs segment extraction,
     returns rule (human-readable summary too), counts per channel, unsupported_criteria.
   - GET /api/v1/campaigns, GET /api/v1/campaigns/{id}.
   - POST /api/v1/campaigns/{id}/drafts → streams generation as SSE (see 5).
   - PATCH /api/v1/campaigns/{id} {version, sms_body, email_subject, email_body,
     channels} → validated, version incremented, content_hash recomputed.
   - GET /api/v1/campaigns/{id}/preview → rendered content for a few sample recipients,
     final character counts after personalization, worst-case lengths, validation
     errors. Rendering lives only in the backend.
   - POST /api/v1/campaigns/{id}/approve {version, content_hash, approved_by}.
   - Strict JSON decoding, body size limits, safe error bodies with stable error codes.
5. Draft streaming (SSE over POST):
   - Response is text/event-stream consumed with fetch + ReadableStream (not
     EventSource), so a dropped connection never silently re-triggers a paid LLM call.
   - Events: `delta` {field: sms_body|email_subject|email_body, text} extracted
     incrementally from the streamed JSON; `done` {drafts, validation, version,
     content_hash}; `error` {code, message}. Periodic heartbeat comments.
   - Partial text is display-only; drafts are persisted only after full validation.
   - Client disconnect cancels the context and the LLM call. Only one generation per
     campaign at a time (409 otherwise).
   - The SSE route is exempt from the global request-timeout middleware and has its
     own generation timeout instead.
6. Frontend (Vue 3, TS strict, vue-router, shared API client module):
   - Campaign list; New campaign (brief textarea with length counter);
     Review page: audience summary + per-channel counts, unsupported criteria warning,
     channel toggles, streaming drafts, editors, preview panel (debounced call to the
     preview endpoint, shows final counts after personalization and violations),
     Approve with a confirmation dialog showing operator name.
   - Approve button disabled while the request is in flight and when validation fails;
     double clicks send one request. After approval the page is read-only.
   - Loading, empty, error and success states everywhere; no v-html (lint rule);
     aborts the stream on unmount.
   - Client-side character counters are usability hints only; the backend decides.
7. Docs: README (campaign flow, endpoints), DECISIONS.md (state machine, approval
   binding with version + content hash, SSE over POST vs EventSource, immutability after
   approval, no-auth limitation).

## Out of scope
Delivery records, Kafka jobs, worker sending, mock provider behavior (M5). Dashboard and
metrics (M6). Editing the segment rule by hand (marketer edits the brief and creates
again). Authentication. Scheduling, A/B, locale-based content.

## Technical decisions
- content_hash = SHA-256 over a canonical encoding of rule + channels + content;
  computed only in the backend.
- Optimistic locking with `version` for edits and approval.
- Fetch-based SSE for generation; EventSource is kept for the read-only dashboard in M6.

## Acceptance criteria
- In the browser: example brief → audience with counts → drafts stream visibly →
  edit → preview shows personalized text and final counts → approve → campaign is
  approved and read-only; reloading the page shows the persisted state.
- Approval is rejected (409/422) for: stale version, hash mismatch, invalid content,
  content over limits for the longest real recipient value, unknown variables, no
  channel with consented recipients, wrong state.
- Re-approving the same content is idempotent; there is exactly one approval event.
- Closing the tab during generation cancels the LLM call (visible in logs/metrics of
  the mock) and leaves the campaign in a consistent state.
- No delivery-related rows or messages are created by approval in this milestone.

## Required tests
- Domain: state machine table test (all legal/illegal transitions); approval rules
  with every rejection reason; content_hash stability and sensitivity.
- Repository integration (testcontainers): optimistic lock conflict, approval
  transaction writes status + approved_by/at + audit event atomically, concurrent
  approve calls produce one approval.
- Worst-case length check against actual audience data (seed fixture with a long name).
- SSE handler with the mock LLM: event order (delta…, done), error event on invalid
  output, cancellation on client disconnect, 409 on concurrent generation.
- Frontend (Vitest + Vue Test Utils): brief form states; streaming rendering from a
  mocked ReadableStream; approve disabled while pending and single request on double
  click; error states rendered from API error codes; read-only after approval.

## Verification
make lint; make test; docker compose up -d; run the full flow in the browser with the
mock LLM; curl approve with a stale version → 409; check campaigns and campaign_events
in psql.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

### Milestone 5 — Queue and reliable delivery

STATUS: final

```
# Milestone 5 — Queue and reliable delivery

## Goal
Approving a campaign durably creates one delivery per (recipient, channel) and queues it
through Kafka. Workers send through a local mock provider that randomly fails, delays and
rate-limits, retry transient failures with backoff, and never deliver the same message
to the same person twice successfully, even under redelivery, crashes and retries.
Each channel stays at or under 100 messages/second across all workers.

## Context
M1–M4 are done: approval is persisted in one transaction with version + content_hash,
approved campaigns are immutable, rendering + validation live in the backend. Read
CLAUDE.md "Campaign Safety" and "Delivery Reliability" and .claude/rules/security.md
"Messaging". Inspect the approval service, the renderer and the worker skeleton first
and state your plan briefly.

## Scope
1. Persistence (golang-migrate):
   - deliveries: id (uuid, also the idempotency key), campaign_id, user_id, channel,
     status (queued | processing | sent | failed | retrying), attempts, max_attempts,
     next_attempt_at, locked_until, last_error_code, provider_message_id, sent_at,
     created_at, updated_at. UNIQUE (campaign_id, user_id, channel). Indexes for
     due-retry and expired-lease scans.
   - outbox: id, topic, key, payload, created_at, published_at.
   - rate_limit_buckets: channel (PK), tokens, updated_at.
   - Campaign statuses extended: approved → sending → completed.
2. Approval (extend the M4 transaction): materialize the audience at approval time —
   INSERT … SELECT deliveries for consented recipients per selected channel
   (ON CONFLICT DO NOTHING) and insert outbox rows, all in the same transaction as the
   approval. Approval retried with the same hash creates nothing new.
3. Outbox relay (in the worker): polls unpublished rows with FOR UPDATE SKIP LOCKED,
   produces to Kafka (franz-go, acks=all, idempotent producer), marks them published.
   At-least-once publishing; duplicates are handled by the consumer.
4. Kafka: topics deliveries.sms and deliveries.email (several partitions each, created at
   startup or by a Compose init step), one consumer group; offsets committed only after
   the delivery row reflects the outcome. Running `docker compose up --scale worker=3`
   must be safe.
5. Delivery processing (per message, in `internal/delivery`, transport-agnostic):
   a. Claim: UPDATE … SET status='processing', locked_until=now()+lease,
      attempts=attempts+1 WHERE id=$1 AND status IN ('queued','retrying')
      AND next_attempt_at <= now() RETURNING … — no row ⇒ duplicate, ack and skip.
   b. Re-check consent from the DB right now; revoked ⇒ failed (consent_revoked).
   c. Render personalization and re-validate final content (SMS ≤ 160 runes, subject
      ≤ 80 runes, no leftover placeholders); invalid ⇒ failed (invalid_content).
   d. Acquire a rate-limit token for the channel (blocks with context).
   e. Call the provider with header Idempotency-Key = delivery id and a request timeout.
   f. Outcome: 2xx ⇒ sent (+ provider_message_id); transient ⇒ retrying with
      next_attempt_at; permanent or attempts exhausted ⇒ failed with error code.
6. Retries:
   - Transient: network errors, timeouts, 408, 429, 5xx. Permanent: other 4xx.
   - Exponential backoff with full jitter (base 1s, factor 2, cap 60s, max 5 attempts —
     all configurable). Retry-After (seconds or HTTP date) is a lower bound, capped.
   - A retry scheduler in the worker re-enqueues due `retrying` deliveries through the
     outbox (FOR UPDATE SKIP LOCKED). Retries pass through the same claim and the same
     rate limiter as first attempts.
7. Global rate limiter (Postgres token bucket, one row per channel):
   - Single atomic UPDATE refills by elapsed time using the DB clock
     (clock_timestamp(), not worker clocks) and takes tokens if available; otherwise
     returns the wait time.
   - Guarantee: rate r + burst b ≤ 100, so any 1-second sliding window has at most 100
     sends per channel (e.g. r = 95/s, b = 5). Configurable; documented.
   - Workers may take small batches of tokens to reduce row contention; unused tokens
     are not hoarded beyond a short window.
8. Crash recovery: a reaper moves `processing` deliveries with expired locked_until back
   to `retrying` (attempt already counted). Graceful shutdown: stop fetching, finish or
   abandon in-flight work within a grace period, never commit offsets for unfinished
   work.
9. Campaign completion: when every delivery of a campaign is sent or failed, the
   campaign becomes completed (idempotent check after each terminal update).
10. Mock provider (`mock-provider/`, separate service, never calls out):
    - POST /v1/sms {to, body}, POST /v1/email {to, subject, body}; Idempotency-Key
      required. Same key ⇒ returns the original result and counts a suppressed
      duplicate; it never "delivers" twice.
    - Rejects recipients outside the fake ranges (example.com emails, the documented
      fake phone range) — a second safety net against real sends.
    - Configurable via env: latency distribution, 5xx rate, 429 rate with Retry-After,
      permanent 4xx for a deterministic subset of recipients, "accepted but response
      lost" (timeout after storing), its own 100/s per-channel limit returning 429.
      Fixed random seed option for tests.
    - In-memory store (documented limitation: restart loses dedupe state);
      GET /v1/messages and GET /v1/stats (received, delivered, duplicates suppressed,
      429s returned, max observed per-second rate per channel).
11. Docs: README (delivery flow, scaling workers, mock provider knobs), DECISIONS.md
    (outbox, claim/lease, retry policy, Postgres rate limiter vs alternatives, delivery
    semantics: at-least-once processing + idempotency key = effectively-once successful
    delivery; limits of exactly-once, e.g. a real provider without idempotency support).

## Out of scope
Dashboard, live status streaming and Prometheus metrics (M6). Load/soak testing and the
full failure-scenario suite (M7). Scheduling, A/B, locale-based content. Real providers.

## Technical decisions
- Transactional outbox instead of producing to Kafka inside the HTTP request.
- Postgres as the single source of truth for delivery state and rate-limit state; Kafka
  only transports work. No Redis.
- DB-scheduled retries (next_attempt_at + scheduler) instead of Kafka delay/retry
  topics: durable, inspectable, and uses the same limiter.
- Keep it simple: no Redis, no Kafka retry topics, no dead-letter topic. Permanently
  failed deliveries stay queryable as `failed` rows in Postgres. List Redis-based
  limiting and retry/DLQ topics in DECISIONS.md as possible future improvements.

## Acceptance criteria
- Approving the example campaign creates exactly one delivery per consented
  (recipient, channel); approving again creates none.
- With the mock provider failing ~20% (mixed 5xx/429/timeouts), all deliveries reach
  sent or failed; failures are only permanent errors or exhausted retries.
- The mock provider never records two deliveries for the same idempotency key, and no
  user receives the same campaign message twice on a channel.
- With 3 workers, observed max per-second rate per channel (mock /v1/stats) is ≤ 100.
- Revoking consent after approval prevents sending to that user on that channel.
- Killing a worker mid-campaign (docker compose kill worker) and restarting it
  completes the campaign without duplicate deliveries.
- Campaign becomes completed when all deliveries are terminal.

## Required tests
- Unit: backoff bounds and jitter range (seeded RNG), Retry-After parsing (seconds,
  HTTP date, invalid, huge values capped), error classification table, delivery state
  transitions, rendering + final validation (long names, Turkish characters).
- Integration (testcontainers Postgres): claim is exclusive under concurrent claimers;
  duplicate message after `sent` is skipped; lease expiry recovery; outbox relay
  publishes each row (Kafka testcontainer) and tolerates republish; rate limiter with
  several concurrent limiter instances never exceeds the configured window.
- End-to-end with mock provider (httptest or container): transient failures retried
  then sent; permanent failure not retried; "accepted but response lost" retried with
  the same key ⇒ one delivery; consent revoked before send ⇒ failed(consent_revoked);
  429 with Retry-After respected.
- Mock provider unit tests: idempotency, fake-recipient guard, failure injection
  determinism with a fixed seed.

## Verification
make lint; make test; docker compose up -d --scale worker=3; approve the example
campaign; watch deliveries by status in psql; curl mock-provider /v1/stats (no
duplicates, max rate ≤ 100/s per channel); docker compose kill one worker mid-run,
start it again, confirm completion with no duplicates.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

### Milestone 6 — Live monitoring

STATUS: final

```
# Milestone 6 — Live monitoring

## Goal
After approval the marketer watches a live dashboard showing how many messages are
queued, processing, sent, failed and retrying per channel, updating in real time.
Operators get Prometheus metrics and health checks for every service.

## Context
M1–M5 are done: deliveries table with statuses, outbox, Kafka workers, Postgres rate
limiter, retries, mock provider with /v1/stats, campaign statuses through completed.
/healthz and /readyz exist on the API from M1. Read CLAUDE.md "Frontend" and
.claude/rules/frontend.md (SSE cleanup, reconnection without duplicates). Keep it
simple: no new infrastructure. Inspect the delivery repository and the existing
health package first and state your plan briefly.

## Scope
1. Campaign progress read model (`internal/monitoring` or in campaign service):
   - One aggregate query: counts GROUP BY channel, status for a campaign, plus failure
     counts by last_error_code and sent count in the last N seconds (throughput).
     Supporting index on (campaign_id, status).
   - Snapshot type: campaign status, per-channel counts for all five delivery statuses,
     total, percent done, failures by reason, throughput, generated_at.
2. Live endpoint: GET /api/v1/campaigns/{id}/events (SSE, consumed with EventSource).
   - Sends a full snapshot on connect, then a new snapshot only when it changed;
     heartbeat comments; a final `complete` event when the campaign is completed,
     then closes.
   - Snapshots are idempotent (full state, not increments), so reconnects never
     double-count; each event has an increasing id; Last-Event-ID just triggers a
     fresh snapshot.
   - A shared per-campaign poller (≈1s interval, configurable) fans out to all
     connected clients, so N viewers do not cause N queries per second. Poller stops
     when the last client leaves. Client disconnect releases everything (no goroutine
     leaks).
   - Exempt from the request-timeout middleware; connection limit per campaign.
   - GET /api/v1/campaigns/{id}/progress returns the same snapshot as JSON (initial
     render and fallback).
3. Prometheus metrics (client_golang) on api (/metrics) and worker (small HTTP server
   with /metrics, /healthz, /readyz):
   - HTTP: requests_total and duration by chi route pattern, method, status class.
   - Delivery: deliveries_total{channel, outcome}, provider_request_duration_seconds
     {channel}, provider_responses_total{channel, code_class},
     retries_scheduled_total{channel, reason}, rate_limiter_wait_seconds{channel},
     outbox_pending (gauge), leases_recovered_total.
   - LLM: llm_requests_total{operation, outcome}, llm_request_duration_seconds
     {operation}, llm_validation_failures_total{operation}.
   - Low cardinality only: no campaign IDs, user IDs, emails, phones or raw paths
     in labels.
4. Health: worker and mock provider expose /healthz and /readyz; readiness checks real
   dependencies (Postgres, Kafka) with short timeouts; Compose healthchecks use them.
   Responses contain no internal error details.
5. Frontend:
   - Dashboard page per campaign: per-channel status counts, progress bar, throughput,
     failures by reason, campaign status, connection indicator
     (live / reconnecting / completed).
   - After approval the review page links/redirects to the dashboard; the campaign list
     shows status badges and links.
   - EventSource closed on unmount and on `complete`; reconnect replaces state with the
     latest snapshot (no duplicated rows or double counts); error and empty states.
   - Accessible: progress bars with aria attributes, status not conveyed by color only.
6. Docs: README (dashboard, /metrics endpoints, example PromQL or curl), DECISIONS.md
   (polling snapshots vs LISTEN/NOTIFY vs per-row events, metric naming and cardinality,
   why no Prometheus/Grafana containers).

## Out of scope
Prometheus/Grafana containers, alerting, tracing. Per-recipient delivery log in the UI.
The full failure-scenario test suite and final docs (M7).

## Technical decisions
- Periodic aggregate snapshots instead of streaming every delivery change: bounded
  cost, simple reconnection, no duplicate display.
- Shared poller per campaign instead of per-connection queries.
- Prometheus exposition only; scraping/visualization left to the operator.

## Acceptance criteria
- Approving the example campaign and opening the dashboard shows counts moving through
  queued → processing → sent/failed/retrying live, ending in completed; the final
  numbers equal a direct SQL count.
- Refreshing the page or briefly stopping the API reconnects and shows correct numbers,
  never inflated.
- Two browser tabs on the same campaign cause one poller, not two.
- /metrics on api and worker expose the metrics above; values change during a
  campaign; no high-cardinality labels.
- /readyz on api and worker returns 503 when Postgres is stopped and recovers after.

## Required tests
- Integration: aggregate query against fixture deliveries in every status and channel.
- SSE handler: initial snapshot, change-only pushes, complete event and close, client
  disconnect stops work, shared poller reused by two clients and stopped after both
  leave (goroutine leak check), Last-Event-ID reconnect.
- Metrics: counters/histograms updated on success, retry, failure paths
  (prometheus testutil); label sets limited to the allowed values.
- Health handlers for worker and mock provider (ok, failing dependency, timeout).
- Frontend: dashboard renders a snapshot; reconnect replaces state without
  duplication; EventSource closed on unmount and on complete; error state.

## Verification
make lint; make test; docker compose up -d --scale worker=3; approve the example
campaign and watch the dashboard; compare with psql counts; curl api and worker
/metrics; docker compose stop postgres → both /readyz 503 → start → 200.

## When done
Report changes, files modified, commands actually run, results observed, and remaining
risks. Do not commit; suggest a commit message.
```

### Milestone 7 — Hardening and submission

STATUS: draft

```
# Milestone 7 — Hardening and submission

## Goal
Prove the core flow is robust: failure scenarios, consent enforcement, rate limits,
duplicate prevention and API validation are covered by automated tests; the stack
starts cleanly from scratch with one command; README, DECISIONS.md, .env.example and
ai-sessions/ are complete and honest. No new features.

## Context
M1–M6 are done. Read project.md (deliverables, scope note), CLAUDE.md "Testing",
"Documentation and Decisions", "Definition of Done", and .claude/rules/git.md
(ai-sessions separate from source commits). Start with a gap analysis, not code.

## Scope
1. Gap analysis (first step, report it before changing anything):
   - Map every requirement in project.md (audience, content, delivery, tracking, AI,
     deliverables) and every rule in CLAUDE.md "AI Safety", "Campaign Safety",
     "Delivery Reliability" to the code and tests that cover it.
   - List gaps and weak assertions; propose the tests to add. Wait for my confirmation
     if fixing a gap needs a non-trivial code change.
2. Scenario tests (Go, testcontainers Postgres + Kafka, api/worker components and an
   in-process mock provider; build tag or package of your choice, run by `make test`):
   a. Provider failures: 5xx bursts, 429 storms with Retry-After, timeouts, "accepted
      but response lost", permanent 4xx. Every delivery ends sent or failed; attempts
      never exceed max; gaps between attempts respect backoff and Retry-After.
   b. Consent: no consent at approval ⇒ no delivery row; consent revoked after
      approval ⇒ failed(consent_revoked), provider never called; single-channel
      consent only sends on that channel.
   c. Rate limit: several worker instances share the limiter; provider-side timestamps
      show ≤ configured limit in every 1-second sliding window, including retries.
      Use a lowered limit (e.g. 20/s) to keep the test fast; the 100/s value is checked
      in the smoke run.
   d. Duplicate prevention: same Kafka message delivered twice, outbox row republished,
      worker crash between provider success and DB update (fault-injection hook),
      lease expiry while the original worker is still sending, concurrent approve
      requests. The provider records exactly one delivery per idempotency key and per
      (campaign, user, channel).
   e. Graceful shutdown: cancel a worker mid-campaign; restart; campaign completes
      with no lost or duplicate deliveries.
3. API validation: table tests over every endpoint — malformed JSON, unknown fields,
   oversized bodies, wrong content type, invalid UUIDs, wrong state, stale version.
   Error bodies never contain SQL, stack traces or internal messages.
   Go native fuzz tests (short seed-corpus run in `make test`) for the segment rule
   validator, the template parser/renderer, and the LLM output decoder.
4. AI safety regression: confirm the prompt-injection and malformed-output corpus from
   M3 still passes end to end through the campaign endpoints.
5. Quality gates: `go test -race` for all Go tests, govulncheck, golangci-lint,
   frontend lint/type-check/tests. Fix findings or document why not.
6. Security pass: no secrets or PII (emails, phones, names, message bodies) in logs;
   no v-html; .env untracked; mock provider rejects non-fake recipients; LLM cannot
   approve or send. Run /security-review on the branch and address real findings.
7. Clean startup from scratch:
   - `make smoke`: script against the running Compose stack — create the example
     campaign (mock LLM), generate drafts, approve, wait for completed, assert mock
     provider stats (no duplicates, max rate ≤ 100/s per channel) and exit non-zero on
     failure.
   - Fresh clone + `cp .env.example .env` + `docker compose up` works with no other
     steps; restarting the stack does not reseed or duplicate data.
   - Volume removal (`docker compose down -v`) is blocked for Claude by the project
     hook: ask me to run it, then continue.
8. Documentation (final):
   - README: what it does, architecture diagram (Mermaid or ASCII), setup, walkthrough
     of the example brief, configuration table (matches .env.example), testing commands,
     mock provider knobs, monitoring endpoints, troubleshooting, requirements coverage
     table, known limitations, and what was not done (bonus items: scheduling, A/B,
     locale-based content, CI; Redis, retry/DLQ topics, auth).
   - DECISIONS.md: consistent with the code; every major decision with alternatives and
     trade-offs; delivery semantics and exactly-once limits.
   - .env.example complete, placeholders only. CLAUDE.md Commands and Repository Layout
     match reality.
9. ai-sessions/: check that exports exist for the work sessions. Scan them for API
   keys, tokens and personal data (e.g. emails, home-directory paths) and report the
   findings with file and line. Do not edit exports yourself: list what needs redacting
   and wait for my decision. Keep any ai-sessions change in a separate commit from
   source changes.

## Out of scope
New features or bonus items, refactors not needed to fix a found bug, Prometheus/Grafana
containers, CI pipeline.

## Acceptance criteria
- Gap analysis delivered; every requirement maps to at least one passing test or a
  documented limitation.
- All scenario, validation and fuzz-seed tests pass under `make test` with -race and
  without ANTHROPIC_API_KEY.
- `make lint` passes; govulncheck has no unaddressed findings.
- `make smoke` passes on a stack started from empty volumes.
- README, DECISIONS.md, .env.example and CLAUDE.md are accurate; nothing claimed that
  was not verified.
- ai-sessions scan report delivered; no secrets remain in tracked files.

## Required tests
Everything listed in Scope 2–4, plus a regression test for every bug found during this
milestone.

## Verification
make lint; make test; (me) docker compose down -v; docker compose up -d --wait;
make smoke; open the UI and run the example brief end to end; git status shows no .env
or temporary artifacts.

## When done
Report: gap analysis outcome, changes, files modified, commands actually run, actual
test results (counts, durations), smoke result, remaining risks and limitations.
Do not commit; suggest separate commit messages for source/docs and ai-sessions.
```

### Milestone 8 — Scheduled campaign delivery

STATUS: ready-for-agent

```
# Milestone 8 — Scheduled campaign delivery

## Problem Statement

A marketer can only approve a campaign for immediate sending: approval creates every
delivery and queues it in the same transaction. They cannot say "send this Friday at
10:00", and once approved there is no way to stop a campaign before it goes out. A
naive delay (a future next_attempt_at on deliveries created at approval) would lose
messages: the outbox publishes at once, the claim refuses a delivery that is not due,
and the processor drops the job as a duplicate. Relative segment windows ("added to
cart in the last 30 days") would also be measured from approval time, not from when
the messages are actually sent.

## Solution

The marketer can approve a campaign for a future time. Approval still binds the exact
reviewed content (version + content hash) and records who approved it, but no
deliveries are created yet: the campaign is `scheduled`. At the scheduled time a worker
releases it: inside one transaction it re-runs every approval check against the
audience as it is then (segment compiled with the scheduled time as reference time),
creates the deliveries and outbox messages, and moves the campaign to `approved`, from
where sending works exactly as today.

Until release the marketer can cancel (back to `drafts_ready`, approval cleared, content
editable again) or reschedule (new time, same approved content). If a release-time check
fails, the campaign moves to `release_failed` with the reasons and sends nothing. If a
due campaign is not released within a grace period (e.g. workers were down), it moves to
`expired` and sends nothing. Approving without a scheduled time behaves exactly as
before.

## User Stories

1. As a marketer, I want to approve a campaign for a specific future date and time, so that messages go out when my audience is most likely to engage.
2. As a marketer, I want to keep the "send now" approval unchanged, so that immediate campaigns work as before.
3. As a marketer, I want to enter the time in my local time zone, so that I do not have to convert to UTC.
4. As a marketer, I want the scheduled time shown in my local time zone with the exact offset, so that there is no doubt when it will send.
5. As a marketer, I want a scheduled campaign's content to be locked, so that what goes out is exactly what I approved.
6. As a marketer, I want to cancel a scheduled campaign before it is released, so that I can stop a campaign that is no longer wanted.
7. As a marketer, I want a cancelled campaign to return to editable drafts, so that I can fix the content and approve it again.
8. As a marketer, I want to reschedule a scheduled campaign to another time, so that I can move it without re-reviewing unchanged content.
9. As a marketer, I want cancel and reschedule to fail clearly if the campaign was already released, so that I never believe I stopped a campaign that is already sending.
10. As a marketer, I want cancel and reschedule to require the version I saw, so that two people acting at once cannot silently overwrite each other.
11. As a marketer, I want to be told immediately if my scheduled time is in the past, too soon, or too far in the future, so that I can pick a valid time.
12. As a marketer, I want the audience to be built when the campaign is sent, so that "last 30 days" means the 30 days before sending.
13. As a marketer, I want users who revoke consent before release to be excluded, so that we never message someone who opted out.
14. As a marketer, I want users who newly match the segment by release time to be included, so that the campaign reaches the current audience.
15. As a marketer, I want the audience count shown at approval to be labelled as an estimate for scheduled campaigns, so that I am not surprised when the final count differs.
16. As a marketer, I want the final recipient count recorded at release, so that I can compare it with the estimate.
17. As a marketer, I want a campaign whose release-time checks fail (no recipients, content too long for a new recipient's name, rule no longer valid) to move to `release_failed` with the reasons, so that nothing invalid is sent and I know what to fix.
18. As a marketer, I want a `release_failed` campaign to be cancellable back to drafts, so that I can fix it and approve it again.
19. As a marketer, I want a campaign that could not be released on time to expire instead of sending late, so that a time-sensitive offer is not delivered hours after it was meant to be.
20. As a marketer, I want an expired campaign to be cancellable back to drafts, so that I can reschedule it through a fresh approval.
21. As a marketer, I want the campaign list and dashboard to show scheduled, release_failed and expired states, so that I can see at a glance what is pending or needs attention.
22. As a marketer, I want the dashboard of a scheduled campaign to show when it will be released, so that I know what to expect.
23. As a marketer, I want the live dashboard to switch to delivery progress automatically on release, so that I do not need to reload the page.
24. As an auditor, I want scheduling, rescheduling, cancelling, release, release failure and expiry recorded in the audit log with actor, from/to status and version, so that every state change is traceable.
25. As an auditor, I want the audit log to keep never storing message content or recipient data, so that scheduling adds no PII exposure.
26. As an operator, I want release to be safe with several workers running, so that a campaign is released at most once and gets exactly one set of deliveries.
27. As an operator, I want release, delivery creation and queueing to commit or roll back together, so that a crash never leaves a released campaign without deliveries.
28. As an operator, I want a release to race safely with cancel or reschedule, so that whichever commits first wins and the other gets a clear conflict.
29. As an operator, I want due campaigns detected with the database clock, so that worker clock skew does not move release times.
30. As an operator, I want the grace period, minimum lead time and maximum horizon to be configurable with safe defaults, so that I can tune them per environment.
31. As an operator, I want configuration validation to reject a grace period shorter than the scan interval, so that campaigns cannot expire merely because the loop did not run yet.
32. As an operator, I want metrics for release outcomes and release lateness, so that I can alert on expired or failed releases.
33. As an operator, I want a large scheduled release to respect the existing per-channel rate limit, so that scheduling cannot exceed 100 messages/second per channel.
34. As a security reviewer, I want the LLM to have no path to schedule, reschedule, cancel or release a campaign, so that only humans and the release loop change these states.
35. As a security reviewer, I want scheduled-time input validated strictly by the backend (format, offset required, unknown fields rejected), so that the frontend is not a security boundary.
36. As a developer, I want the release logic to live in the campaign domain and reuse the approval checks, so that approval rules are not duplicated.
37. As a developer, I want all scheduling behaviour testable with an injected clock and in-memory fakes, so that tests are deterministic and need no real waiting.

## Implementation Decisions

State machine (campaign domain):
- New statuses: `scheduled`, `release_failed`, `expired`.
- Transitions:
    drafts_ready   → scheduled        (approve with scheduled_at; human)
    drafts_ready   → approved         (approve without scheduled_at; unchanged)
    scheduled      → scheduled        (reschedule; human)
    scheduled      → approved         (release; system)
    scheduled      → release_failed   (release checks failed; system)
    scheduled      → expired          (due but past the grace period; system)
    scheduled | release_failed | expired → drafts_ready   (cancel; human)
- `scheduled`, `release_failed` and `expired` carry an approval: content is immutable
  and edits are rejected, as for approved campaigns. Cancel clears the approval
  (approver, approved-at, scheduled time) and bumps the version, so a later approval
  needs a fresh review.
- Every human or system change bumps the version (optimistic lock).

Approve:
- Approval gains an optional scheduled time. Without it, approval is unchanged
  (creates deliveries in the same transaction).
- With it, approval runs all current checks (version, content hash, rule revalidation,
  content validation, audience length checks, at least one recipient) against the
  audience at approval time, so obvious problems are caught while a human is present;
  it stores the scheduled time and moves to `scheduled` without a delivery plan.
- Scheduled time rules: RFC3339 with an explicit offset; at least the minimum lead time
  after now; at most the maximum horizon after now. Violations are `invalid_request`
  with a field error on `scheduled_at`.
- Repeating an approval with the same content hash on an already approved/scheduled
  campaign stays idempotent and changes nothing (including the schedule); moving the
  time is only done by reschedule.

Reschedule and cancel (new service operations):
- Both take the version and content hash the reviewer saw, and an actor name validated
  like the approver name.
- Reschedule is allowed only from `scheduled`; applies the same scheduled-time rules.
- Cancel is allowed from `scheduled`, `release_failed`, `expired`.
- Any other state → `invalid_state`; stale version → `version_conflict`; different hash
  → `content_changed`. Both run under the campaign row lock, so they serialize with
  release.

Release (new service operation, system actor):
- Loads and locks the campaign. Not `scheduled` or not yet due → no-op (idempotent).
- Past scheduled time + grace period → `expired`, no deliveries.
- Otherwise re-runs the approval checks (shared with Approve, not copied), compiling the
  segment with the scheduled time as reference time (D19). Success → `approved` with a
  delivery plan in the same write (existing materialization: deliveries + outbox in one
  transaction). Failure → `release_failed` with the error codes and field errors stored
  on the campaign.
- The service decides with its injected clock; the database selects candidates.

Store / persistence:
- Migration 000005: extend the campaign status check, the approval-columns check and the
  event action check (`scheduled`, `rescheduled`, `schedule_canceled`, `released`,
  `release_failed`, `expired`); add `scheduled_at timestamptz` (set exactly when status
  is `scheduled`, kept on `release_failed`/`expired`/released campaigns for audit) and a
  release-failure column holding error codes/field errors (no content, no PII); partial
  index on scheduled_at where status = 'scheduled'. Down migration reverses it.
- New query for due campaigns: ids where status = 'scheduled' and scheduled_at <= the
  database clock, limited, skipping rows locked by other workers.

Worker:
- The maintenance loop gains a release task: fetch due ids in batches and call Release
  for each; errors are logged and retried on the next tick. The worker wires the
  campaign service (no HTTP, no LLM required for release).

Configuration (.env.example updated):
- SCHEDULE_RELEASE_GRACE (default 15m), SCHEDULE_MIN_LEAD (default 1m),
  SCHEDULE_MAX_HORIZON (default 720h). Validation: grace > DELIVERY_SCAN_INTERVAL,
  all positive, min lead < max horizon.

HTTP API (/api/v1):
- POST /campaigns/{id}/approve accepts optional `scheduled_at`.
- POST /campaigns/{id}/reschedule  {version, content_hash, scheduled_at, actor}
- POST /campaigns/{id}/cancel      {version, content_hash, actor}
- Campaign responses include `scheduled_at` and, for `release_failed`, the release
  errors. Strict JSON decoding, existing error mapping and status codes.

Monitoring:
- Progress snapshot includes `scheduled_at`; scheduled campaigns stream with zero
  deliveries until release. `expired` is terminal for the SSE stream like `completed`;
  `release_failed` is not (a cancel may follow).
- Metrics: campaign release outcomes by outcome (released, release_failed, expired) and
  a release-lateness histogram (seconds between scheduled time and release). Bounded
  labels only.

Frontend:
- Review view: "Send now" or "Schedule" with a local date/time input converted to
  RFC3339 with offset; scheduled panel with time, Reschedule and Cancel; release-failed
  panel showing the backend's reasons; estimate label on audience counts for scheduled
  campaigns. List and dashboard show the new states. No client-side copies of backend
  rules beyond basic input shape; backend errors are displayed.

Documentation:
- DECISIONS.md: new entries for scheduling (audience at release, amending D39 for
  scheduled campaigns), expiry instead of late send, release_failed, reference time =
  scheduled time, alternatives considered (materializing at approval with held
  deliveries). README: flow, configuration table, API, "Not done" updated.

## Testing Decisions

- Good tests assert externally visible behaviour: returned campaign state, recorded
  events, error codes, delivery plans in the write, delivered messages — not internal
  helpers or SQL text.
- Primary seam: the campaign service with the campaigntest in-memory fakes and an
  injected clock (prior art: approve, edit and generation service tests). Covers:
  approve with schedule (valid, past, too soon, too far, missing offset); scheduled
  approval creates no delivery plan; idempotent re-approval; edits rejected while
  scheduled; reschedule (valid, wrong state, stale version, wrong hash, invalid time);
  cancel from each allowed state and rejection from others; release before due (no-op),
  at due (approved + delivery plan compiled with the scheduled time as reference),
  past grace (expired, no plan), on check failure (release_failed with reasons, no
  plan); release of a non-scheduled campaign is a no-op; audit events for each.
- Postgres integration (testcontainers, prior art: campaign store lifecycle, concurrent
  approvals, longest real first name): migration constraints accept/reject the new
  states; due query respects the database clock and skip-locked; concurrent releases
  by several callers produce one set of deliveries and one release event; release vs
  cancel race yields exactly one winner; consent revoked and newly matching users
  between scheduling and release are excluded/included.
- One end-to-end delivery test (prior art: e2e drain tests): schedule → maintenance
  release → deliveries sent through the mock provider.
- HTTP handler tests (prior art: campaigns and validation table tests): new fields and
  endpoints, strict decoding, error mapping; no internal messages in error bodies.
- Config tests for the new variables and their validation.
- Frontend view/component tests (Vitest, prior art: views/__tests__) for schedule,
  reschedule, cancel and the new states.
- Extend the AI-safety endpoint corpus check: no LLM path reaches schedule, reschedule,
  cancel or release.

## Out of Scope

- Recurring or cron-style schedules.
- Per-recipient time zones, quiet hours, send-time optimization.
- Editing content of a scheduled campaign without cancelling.
- Automatic retry of release_failed campaigns; sending expired campaigns late.
- A/B variants, locale-based content, authentication and roles.

## Further Notes

- Lateness bound: release happens within one DELIVERY_SCAN_INTERVAL of the scheduled
  time while workers run; beyond the grace period the campaign expires.
- The audience count at approval is an estimate for scheduled campaigns; per-recipient
  rendering before each send remains the final content check (D42).
- Verification: make fmt-check; make lint; make test; docker compose up -d; schedule the
  example campaign ~2 minutes ahead, watch it release on the dashboard; cancel and
  reschedule another; stop the worker past the grace period and observe `expired`;
  make smoke still passes.
- When done: report changes, files modified, commands actually run, results observed,
  and remaining risks. Do not commit; suggest a commit message.
```
