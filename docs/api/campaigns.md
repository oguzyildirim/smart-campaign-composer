---
type: API Endpoint
title: Campaigns
description: The /api/v1/campaigns endpoints for creating, generating, editing, previewing, approving, and scheduling campaigns.
resource: /api/v1/campaigns
tags: [api, campaign, sse]
---

# Endpoints

| Method and path | Purpose |
| --------------- | ------- |
| `POST /api/v1/campaigns` `{"brief"}` | Create: AI segment extraction, validation, counts → `201`, `audience_ready` |
| `GET /api/v1/campaigns` | The 100 most recent campaigns |
| `GET /api/v1/campaigns/{id}` | One campaign, with `validation` once drafts exist |
| `POST /api/v1/campaigns/{id}/drafts` | Generate drafts as Server-Sent Events (see below) |
| `PATCH /api/v1/campaigns/{id}` `{"version","channels","sms_body","email_subject","email_body"}` | Replace content and channels; all fields required |
| `GET /api/v1/campaigns/{id}/preview` | Content rendered for up to 3 recipients per selected channel, final lengths, all approval checks |
| `POST /api/v1/campaigns/{id}/approve` `{"version","content_hash","approved_by","scheduled_at"?}` | Explicit human approval; with `scheduled_at` for a later release |
| `POST /api/v1/campaigns/{id}/reschedule` `{"version","content_hash","scheduled_at","actor"}` | Move a `scheduled` campaign to another time |
| `POST /api/v1/campaigns/{id}/cancel` `{"version","content_hash","actor"}` | Return a `scheduled`, `release_failed`, or `expired` campaign to `drafts_ready` |

Error responses are listed in [Errors](errors.md).

# Draft stream

The response is `text/event-stream` on a POST, read with `fetch` (not
`EventSource`, which would reconnect and silently start another paid model
call). Events:

- `delta` `{"attempt","field","text"}` with `field` one of `sms_body`,
  `email_subject`, `email_body`. A new `attempt` means a repair call
  started; discard earlier text.
- then `done` `{"status","drafts","validation","version","content_hash"}`
  or `error` `{"code","message"}`.

Heartbeat comments arrive every 15 s. Only one generation runs per campaign
(`409 generation_in_progress` otherwise). Errors after the stream started
arrive as an `error` event with the same codes as [Errors](errors.md).

# Edits

Edits must be structurally sound (known channels; UTF-8 without NUL; at
most twice each field's limit), or they get `400`. Content that breaks a
content rule (over a limit, unknown variable, HTML, empty) is saved and
reported in `validation`, so work in progress is never lost. Approval
rejects it. An edit that changes nothing does not bump the version.

# Approval

Approval runs in one transaction. It checks the state, `version`, and
`content_hash`, then re-validates the segment rule and content. It then
checks every limit against the longest real `first_name` and `city` among
each selected channel's consented recipients, and requires at least one
recipient. Approving an approved campaign with the same hash returns `200`
unchanged with no second audit event; a different hash returns `409`.
`approved_by` is free text: there is no authentication.

With `scheduled_at`, see [Scheduled delivery](../flows/scheduled-delivery.md).

# Examples

Mock mode, seeded data; output as observed, shortened:

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

# Audit log

The audit log is in `campaign_events` (action, actor, from → to status,
version). It never holds content or recipient data:

```bash
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT action, actor, from_status, to_status, version FROM campaign_events ORDER BY id"'
```
