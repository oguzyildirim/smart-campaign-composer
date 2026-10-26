---
type: API Endpoint
title: Progress
description: Campaign delivery progress as a JSON snapshot and as a Server-Sent Events stream for the live dashboard.
resource: /api/v1/campaigns/{id}/progress
tags: [api, monitoring, sse]
---

Approving a campaign opens its dashboard (`/campaigns/{id}/dashboard`; the
campaign list and the review page link to it). It shows the campaign status,
per-channel counts for every delivery status, overall and per-channel
progress bars, the recent send rate, failures by reason, and a connection
indicator (live / reconnecting / disconnected / completed / ended for an
expired campaign). A scheduled campaign shows its release time.

```
browser ──GET /progress──► api ──► one aggregate query (snapshot)
browser ══EventSource /events══► api ◄── shared poller per campaign (1 query/s,
                                         however many viewers) ──► postgres
```

# Snapshot

`GET /api/v1/campaigns/{id}/progress` returns the snapshot as JSON:

```json
{"campaign_id":"…","campaign_status":"sending",
 "channels":[{"channel":"sms","queued":100,"processing":10,"sent":140,"failed":2,"retrying":10,"total":262,"sent_recently":90,"percent_done":54.2}, …],
 "totals":{"queued":220,"processing":16,"sent":250,"failed":6,"retrying":16,"total":508},
 "percent_done":50.39,"failures":[{"code":"provider_rejected","count":5}],
 "throughput":{"window_seconds":10,"sent":150,"per_second":15},"generated_at":"…"}
```

# Event stream

`GET /api/v1/campaigns/{id}/events` is a Server-Sent Events stream for
`EventSource`:

- a `snapshot` event on connect, another one only when the state changed,
  and a final `complete` event (then the stream closes) once the campaign
  is completed or expired;
- a scheduled campaign streams with zero deliveries and its `scheduled_at`
  until the release, then its delivery progress, without a reload;
- every event carries the full snapshot, so a reconnect or a repeated event
  can never inflate the numbers;
- event ids continue from `Last-Event-ID`; a reconnect always starts with a
  fresh snapshot;
- heartbeat comments every 15 s;
- more than `PROGRESS_MAX_VIEWERS` streams on one campaign get
  `429 too_many_viewers`.

# Examples

```bash
curl -s localhost:8080/api/v1/campaigns/$ID/progress
curl -sN localhost:8080/api/v1/campaigns/$ID/events     # Ctrl-C to stop
```

Related: [Metrics](../operations/metrics.md), [Delivery](../flows/delivery.md).
