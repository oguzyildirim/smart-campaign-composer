---
type: Runbook
title: Troubleshooting
description: Common problems with the stack, tests, AI calls, delivery, and the dashboard, and how to fix them.
tags: [operations, troubleshooting]
---

- **`required variable POSTGRES_USER is missing`**: create `.env` from
  `.env.example`.
- **`/readyz` returns 503**: run `docker compose ps` and
  `docker compose logs api`. The log names the failing dependency (see
  [Health](../api/health.md)).
- **`make test` fails with a Docker error**: integration tests need a
  running Docker daemon. On WSL 2, enable *Docker Desktop → Settings →
  Resources → WSL integration* for your distro. `make test-unit` works
  without Docker.
- **Campaign creation or the draft stream returns `502 invalid_output` or
  `503 upstream_unavailable`**: `docker compose logs api | grep "llm attempt"`
  shows each attempt's outcome and the validation problems. Briefs and
  generated text are never logged. `LLM_PROVIDER: claude requires
  ANTHROPIC_API_KEY` at startup means the provider was forced without a key.
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
