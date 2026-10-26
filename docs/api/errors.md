---
type: Reference
title: Errors
description: Stable error codes returned by the campaign endpoints and the draft stream.
tags: [api, errors]
---

Errors carry a stable `code`; no model text or provider error detail is
returned. During draft generation, errors after the stream started arrive
as an `error` event with the same codes (see [Campaigns](campaigns.md)).

| Status | `code`                 | Meaning                                                   |
| ------ | ---------------------- | --------------------------------------------------------- |
| 400    | `invalid_brief`        | Empty, longer than 1000 characters, or control characters |
| 400/415| `invalid_request`      | Malformed or oversized body, unknown or missing fields, wrong type; `details` lists field problems |
| 404    | `not_found`            | No campaign with that id (also for ids that are not UUIDs) |
| 409    | `invalid_state`        | The status does not allow the action (e.g. editing an approved campaign, canceling a released one) |
| 409    | `version_conflict`     | The campaign changed since it was read                    |
| 409    | `content_changed`      | The content hash does not match the stored content        |
| 409    | `generation_in_progress` | Drafts are being generated for the campaign             |
| 422    | `invalid_content`      | Approval: content fails validation, including for the real audience; `details` lists fields |
| 422    | `no_recipients`        | Approval: no selected channel has a consented recipient   |
| 422    | `unsupported_brief`    | Only unsupported criteria; `unsupported_criteria` lists them |
| 422    | `refused`              | The model declined the brief                              |
| 502    | `invalid_output`       | Output failed validation on every attempt                 |
| 503    | `upstream_unavailable` | Provider error (429/5xx/network) after SDK retries        |
| 504    | `timeout`              | The model did not answer within the call timeout          |

The progress stream additionally returns `429 too_many_viewers` (see
[Progress](progress.md)).
