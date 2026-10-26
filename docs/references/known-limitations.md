---
type: Reference
title: Known limitations
description: Known limitations of segmentation, AI generation, approval, scheduling, delivery, and monitoring.
tags: [limitations]
---

# Segmentation and AI

- Segment rules are AND-only; alternatives use `in`. There is no OR or
  nesting, and a field can appear only once (so no `order_count` range).
- Contradictory rules (e.g. `purchased_within_days: 7` together with
  `no_purchase_within_days: 30`) are valid and count 0.
- The seed only fills an empty database. Changing `SEED_USER_COUNT` later
  requires removing the Postgres volume.
- Segment preview returns counts only, not recipient lists. The campaign
  preview shows first name and city of up to 3 fake recipients per channel,
  never contact details.
- The mock LLM is keyword based: it understands the example brief and
  some variations, not arbitrary briefs.

# Campaigns and approval

- There is no authentication. `approved_by` is a free-text name, and edits
  are attributed to "marketer". Anyone who can reach the API can approve.
- The segment rule cannot be edited by hand. To change the audience, edit
  the brief and create a new campaign.
- Generating drafts again replaces the current drafts, including edits.
  The UI only offers generation before the first drafts exist.
- Content limits are checked against fixed worst-case variable lengths
  (first name 30, city 20), at approval against the longest real values in
  the audience, and per recipient right before sending.
- Audience counts are a snapshot taken at creation. Approval re-checks the
  live audience, but the stored counts are not refreshed.
- If an API process dies mid-generation, the campaign's generation lease
  blocks a new generation until it expires (generation timeout + 15 s).
- SMS limits count characters, not GSM-7/UCS-2 segments (see
  [Campaign flow](../flows/campaign-flow.md)).

# Scheduling

- For "send now", the audience is fixed at approval. Users who gain
  consent later are not added; users who revoke it are skipped at send
  time. A scheduled campaign's audience is built at release instead.
- A scheduled campaign is released within about one
  `DELIVERY_SCAN_INTERVAL` of its time while a worker runs; past
  `SCHEDULE_RELEASE_GRACE` it expires and is never sent late. A
  `release_failed` campaign is not retried automatically.
- Scheduled times are one moment for every recipient: no per-recipient
  time zones, quiet hours, or recurring schedules. Changing the content of
  a scheduled campaign requires a cancel and a new approval.
- The audience counts shown for a scheduled campaign are an estimate from
  creation; the final count is the number of deliveries its release
  creates.

# Delivery

- Delivery is effectively-once only because the provider honours the
  idempotency key. A provider without idempotency support could deliver
  twice when a response is lost or a lease expires mid-send. The mock
  provider keeps its keys in memory, so restarting it during a campaign
  loses that protection.
- The rate limit is enforced when a token is taken, not when the provider
  receives the request; network jitter can move a few requests into the
  next second. The defaults (90 + 5) leave headroom below 100.
- A delivery whose lease expires on its last attempt fails with
  `retries_exhausted` even if the provider accepted that attempt.
- There is no way to cancel or pause a sending campaign (only a scheduled
  one before its release).
- Failed deliveries stay as `failed` rows; there is no dead-letter topic
  and no manual retry.
- Published outbox rows are deleted after 24 hours.
- A worker retries a message whose handling fails (for example while
  Postgres is down) until it succeeds, and holds its consumer-group
  rebalance meanwhile; other workers keep their partitions but cannot take
  over the stuck worker's until it recovers or is stopped.
- An attempt interrupted by shutdown is given back, so repeated restarts
  in the middle of sends can make more than `DELIVERY_MAX_ATTEMPTS`
  provider calls for one delivery (all with the same idempotency key).

# Monitoring

- Dashboard numbers are up to `PROGRESS_POLL_INTERVAL` (1 s) old. A
  stream reconnecting to a different API instance gets a fresh snapshot,
  but event ids are per connection (continued from `Last-Event-ID`), not
  global.
- `outbox_pending` is counted by every worker; with several workers each
  reports the same global value (take `max`, not `sum`).
- The worker's metrics port is published on a random host port, so
  scraping it from the host needs `docker compose port`.
