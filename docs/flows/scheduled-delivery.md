---
type: Flow
title: Scheduled delivery
description: Approving a campaign for a later time, rescheduling or canceling it, and its at-most-once release by a worker.
tags: [campaign, scheduling, delivery]
---

# Choosing a time

Choose **Schedule** in the approve dialog and pick a date and time in your
browser's local time. The UI sends it as RFC 3339 with your UTC offset
(`2026-10-30T10:00:00+03:00`) and shows scheduled times with their exact
offset.

The backend accepts only RFC 3339 with an explicit offset and a time at
least `SCHEDULE_MIN_LEAD` (1 minute) and at most `SCHEDULE_MAX_HORIZON`
(720 hours) ahead; anything else is `400 invalid_request` with a
`scheduled_at` field error. Leave the field out to send now; an explicit
`"scheduled_at": null` is rejected the same way, so a client bug cannot turn
a schedule into an immediate send.

# Steps

1. **Approval** runs every approval check against the audience as it is
   now, so obvious problems surface while you are there. It records who
   approved, binds the version and content hash, and moves the campaign to
   `scheduled`. No deliveries are created. Audience counts are labelled as
   an estimate.
2. **Until the release** the content is locked. *Reschedule* moves the time
   (same rules, same approved content). *Cancel* returns the campaign to
   `drafts_ready`, clears the approval and the time, and bumps the version,
   so sending needs a fresh review and approval. Both need the version and
   hash you saw and your name. Once released, both fail with
   `409 invalid_state`, so you never think you stopped a campaign that is
   sending.
3. **Release.** Every worker's maintenance loop (each
   `DELIVERY_SCAN_INTERVAL`, 1 s) asks Postgres for due campaigns, using the
   database clock and skipping campaigns another worker holds. For each it
   locks the campaign and repeats every approval check with the **scheduled
   time as the segment's reference time**, so "added to cart in the last 30
   days" means the 30 days before sending. Users who revoked consent are
   left out; users who newly match are included. In one transaction:
   - the checks pass → `approved`, with the deliveries and their outbox
     messages; sending continues as for any approved campaign, under the
     same per-channel rate limit (see [Delivery](delivery.md));
   - a check fails (no recipients left, content too long for a new
     recipient's name, rule no longer valid) → `release_failed` with the
     reasons in `release_failure`; nothing is sent; cancel to fix it;
   - the campaign is due for longer than `SCHEDULE_RELEASE_GRACE` (15
     minutes; for example the workers were down) → `expired`; nothing is
     sent, rather than a time-sensitive offer going out hours late; cancel
     to approve it again for a new time.

# Guarantees

Release is at most once: under the row lock only the first caller finds the
campaign `scheduled` and due; others change nothing. A release racing with a
cancel or reschedule is decided by whichever commits first.

The LLM has no path to any of this: release is not an HTTP endpoint, and
only the worker calls it. Every step is in the audit log (`scheduled`,
`rescheduled`, `schedule_canceled`, `released`, `release_failed`,
`expired`), still without content or recipient data. The final recipient
count is the number of deliveries the release created, shown per channel on
the dashboard.

# Examples

```bash
AT=$(date -d '+2 minutes' --iso-8601=seconds)   # e.g. 2026-10-25T16:40:00+03:00
curl -s -H 'Content-Type: application/json' \
  -d "{\"version\":2,\"content_hash\":\"$HASH\",\"approved_by\":\"Deniz\",\"scheduled_at\":\"$AT\"}" \
  localhost:8080/api/v1/campaigns/$ID/approve | jq '{status, scheduled_at}'
curl -s -H 'Content-Type: application/json' \
  -d "{\"version\":3,\"content_hash\":\"$HASH\",\"actor\":\"Deniz\"}" localhost:8080/api/v1/campaigns/$ID/cancel | jq .status
```
