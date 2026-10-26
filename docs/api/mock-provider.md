---
type: Service
title: Mock provider
description: The local fake SMS/email provider with idempotency keys, a fake-recipient guard, deterministic fault injection, and stats.
resource: http://localhost:9090
tags: [api, delivery, mock]
---

# Endpoints

- `POST /v1/sms {"to","body"}` and `POST /v1/email {"to","subject","body"}`
  require an `Idempotency-Key` header. The first accepted request for a key
  is delivered; later requests with the same key return the original
  `message_id` with `"duplicate": true`.
- The provider only accepts the fake recipient ranges (`@example.com`,
  `+9995…`) and rejects everything else with 422, a second safety net
  behind the database constraints.
- `GET /v1/messages?channel=&limit=` lists accepted messages (newest first).
- `GET /v1/stats` counts per channel: `received`, `delivered`,
  `duplicates_suppressed`, `duplicate_content` (the same message delivered
  to the same person under a different key; should stay 0), injected
  faults, `throttled`, and `max_per_second` (most requests in any
  one-second window).

# Fault injection

Faults are set with `MOCK_*` variables (see
[Configuration](../operations/configuration.md)). Decisions are a hash of
`MOCK_SEED`, the idempotency key, and the attempt number for that key, so a
fixed seed reproduces them however requests interleave.

State is in memory: restarting the mock provider forgets delivered keys and
stats.

# Examples

```bash
curl -s localhost:9090/v1/stats | jq .channels
```
