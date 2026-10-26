---
type: Reference
title: Not done
description: Bonus items and features intentionally left out of the project.
tags: [scope, limitations]
---

- Bonus items: A/B variants, locale-based content (the mock answers in the
  brief's language, but there is no per-recipient language). Scheduled
  sending is done (one-off times only; no recurring schedules or
  per-recipient time zones).
- CI does not run `make smoke` or check frontend formatting (no formatter
  such as Prettier is set up), and it does not publish images or deploy.
- Authentication and roles (see [Known limitations](known-limitations.md)).
- Redis, retry or dead-letter topics: retries go through Postgres and the
  outbox; failed deliveries stay as `failed` rows.
- Prometheus/Grafana containers, alerting, tracing, and a per-recipient
  delivery log in the UI.
