# Smart Campaign Composer — Project Instructions

Original case specification: @project.md

## Mission

Build a reliable, secure, testable campaign management application.
Marketers describe a campaign in natural language; Claude proposes an
audience segment and SMS/email drafts; humans review, edit, and approve;
approved messages are delivered only to fake users through a local
mock provider.

Never send real SMS or email.

## Working Agreement

1. Inspect the relevant implementation and tests before changing code.
2. Milestones are defined in the user's prompts. Treat the acceptance
   criteria in the current prompt as the scope of work.
3. Do not implement future milestones unless explicitly requested.
4. State the intended changes briefly before significant work.
5. Prefer small, reviewable changes over broad rewrites.
6. If requirements conflict, follow security requirements first and
   explain the conflict.

## Commands

Planned targets — keep this list in sync with the Makefile and
docker-compose.yml once they exist. Never list a command that does not work.

- `docker compose up` — full stack: Postgres, Kafka, migrations, seed data,
  mock provider, API, worker, frontend.
- `make test` — all Go and frontend tests; must pass without
  `ANTHROPIC_API_KEY` (mock LLM).
- `make lint` — golangci-lint and frontend lint/type checks.
- `go test ./internal/<pkg>/... -run <TestName>` — run a single Go test.
- `cd frontend && npm test` — frontend unit tests.

## Repository Layout (planned)

- `cmd/api`, `cmd/worker` — separate API and worker entry points.
- `internal/` — domain logic, services, and adapters (Postgres, Kafka,
  Claude client, messaging provider client).
- `migrations/` — versioned SQL migrations.
- `mock-provider/` — local mock SMS/email provider.
- `frontend/` — Vue 3 + TypeScript + Vite application.
- `ai-sessions/` — Claude session exports, written automatically by the
  `SessionEnd` hook; never hand-edited or mixed into source commits.

Update this section when the layout changes; path-scoped rules in
`.claude/rules/` depend on it.

## Technology

- Backend: Go.
- Frontend: Vue 3, TypeScript, Vite.
- Persistence: PostgreSQL and versioned SQL migrations.
- Queue: Kafka.
- AI: Claude API behind a Go interface, with a deterministic mock.
- Streaming: Server-Sent Events.
- Local environment: Docker Compose.
- Tests: Go tests, frontend unit tests, and integration tests.

Do not introduce a new framework, database, queue, or major dependency
without explaining the benefit and updating DECISIONS.md.

## Configuration

- `ANTHROPIC_API_KEY` is read from `.env`, which stays untracked.
- `.env.example` contains placeholders only; update it whenever
  configuration changes.
- Never read, print, or edit `.env`.

## Architecture

- Keep business logic independent of HTTP handlers and infrastructure.
- Use explicit interfaces at external boundaries.
- Keep domain models and API request/response types distinct where useful.
- Make dependencies flow inward toward business logic.
- Avoid unnecessary abstractions, global mutable state, and oversized files.
- Follow existing naming and package conventions.
- Do not duplicate business rules across frontend and backend.
- The backend is the source of truth for campaign status and approval.

## AI Safety

- Treat every LLM response as untrusted input.
- Use structured JSON and strict backend validation.
- Never execute LLM-generated SQL.
- Translate allowlisted segment rules into parameterized SQL.
- Reject unsupported fields, operators, values, and unknown schema fields.
- Validate generated messages and personalization variables.
- Do not let the LLM approve or send campaigns.
- Keep mock AI available for deterministic tests.
- Handle malformed output, timeouts, cancellation, and API errors.

## Campaign Safety

- Require explicit, persisted human approval before queueing messages.
- Use fake recipients and a local mock messaging provider exclusively.
- Never add a real SMS/email integration.
- Enforce channel consent when building the audience and immediately
  before delivery.
- Revalidate final rendered content after personalization.
- Enforce limits: SMS ≤ 160 characters, email subject ≤ 80 characters.
- Escape user-controlled content when rendering HTML.
- Never log API keys or unnecessary personal information.

## Delivery Reliability

- Assume queue jobs may execute more than once.
- Use durable delivery records and database uniqueness constraints.
- Use stable idempotency keys.
- Design retries with bounded attempts, exponential backoff, and jitter.
- Respect provider Retry-After values when present.
- Distinguish transient from permanent errors.
- Enforce a maximum of 100 messages/second per channel, globally across
  all workers.
- Retries must also respect rate limits.
- Document the limitations of exactly-once delivery.
- Make shutdown and worker recovery safe.

## Frontend

Detailed standards live in `.claude/rules/frontend.md`. Never treat
frontend validation as a security boundary, and keep secrets out of
browser code.

## Testing

- Add or update tests for every meaningful behavior change.
- Include negative tests and edge cases.
- Keep unit tests deterministic and independent of external APIs.
- Use integration tests for database, queue, and persistence behavior.
- Do not weaken assertions to make tests pass.
- Never skip a failing test without an explicit, documented reason.
- Run relevant tests after changes and the full suite before completion.
- Report only commands that were actually executed and results observed.

## Documentation and Decisions

- Keep README.md aligned with actual setup and behavior.
- Record significant trade-offs and alternatives in DECISIONS.md.
- Never put real credentials in documentation or example files.
- Do not fabricate Claude session exports or test results.

## Definition of Done

A task is complete when:
1. The requested behavior is implemented.
2. Relevant tests pass.
3. Formatting and applicable checks pass.
4. Security and failure behavior are considered.
5. Documentation is updated where necessary.
6. Known limitations are reported honestly.

At completion, summarize changes, files modified, commands executed,
actual test results, and remaining risks.
