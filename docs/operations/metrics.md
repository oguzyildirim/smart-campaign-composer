---
type: Reference
title: Metrics
description: Prometheus metrics exposed on /metrics by the api and each worker, with labels and example PromQL.
resource: /metrics
tags: [operations, monitoring, prometheus]
---

Prometheus text format on `GET /metrics` of the api (`localhost:8080/metrics`)
and of each worker (port 8081, published on an ephemeral host port per
replica). nginx does not proxy `/api/metrics`. No Prometheus or Grafana runs
in the stack; point your own scraper at these endpoints.

# Schema

| Metric | Labels | Where |
| --- | --- | --- |
| `http_requests_total`, `http_request_duration_seconds` | `route` (chi pattern), `method`, `status_class` (SSE stream durations are not observed) | api, worker |
| `llm_requests_total`, `llm_request_duration_seconds`, `llm_validation_failures_total` | `operation`, `outcome` | api |
| `deliveries_total` | `channel`, `outcome` (`sent`, `failed`, `retrying`, `released`) | worker |
| `provider_request_duration_seconds`, `provider_responses_total` | `channel`, `code_class` (`2xx`, `4xx`, `429`, `5xx`, `error`) | worker |
| `retries_scheduled_total` | `channel`, `reason` (error code) | worker |
| `rate_limiter_wait_seconds` | `channel` | worker |
| `outbox_pending` (gauge), `leases_recovered_total` | none | worker |
| `campaign_releases_total` | `outcome` (`released`, `release_failed`, `expired`) | worker |
| `campaign_release_lateness_seconds` | none (scheduled time → release decision, every outcome) | worker |

Labels never carry campaign, delivery, or user ids, addresses, or raw paths.

# Examples

```bash
curl -s localhost:8080/metrics | grep -E '^(http_requests_total|llm_)'
curl -s localhost:$(docker compose port --index 1 worker 8081 | cut -d: -f2)/metrics \
  | grep -E '^(deliveries_total|provider_|retries_scheduled_total|rate_limiter_wait|outbox_pending|leases_recovered_total)'
```

```
sum by (channel) (rate(deliveries_total{outcome="sent"}[1m]))          # sends/s per channel (≤ 100)
sum by (channel, code_class) (rate(provider_responses_total[5m]))       # provider error mix
histogram_quantile(0.95, sum by (le, channel) (rate(rate_limiter_wait_seconds_bucket[5m])))
sum by (operation) (rate(llm_validation_failures_total[1h]))
sum by (outcome) (increase(campaign_releases_total{outcome!="released"}[1h]))   # alert on expired/failed releases
```

`outbox_pending` is counted by every worker; with several workers each
reports the same global value (take `max`, not `sum`).
