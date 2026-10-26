---
type: Reference
title: Requirements coverage
description: Where each case requirement is enforced in the code and which tests cover it.
tags: [requirements, testing]
---

| Requirement | Where it is enforced | Tests |
| --- | --- | --- |
| Segment from the LLM is validated; no LLM SQL | `segment` allowlist parser, compiler to `$n` placeholders | `TestParse_*`, `TestCompile_ValuesNeverReachSQLText`, `FuzzParse`, `TestAISafety_*` |
| No message without channel consent | consent filter at approval; re-read before each send | `TestApprove_MaterializesConsentedDeliveries`, `TestScenario_Consent`, `TestE2E_ConsentRevokedAfterApprovalIsNotSent` |
| SMS ≤ 160, subject ≤ 80, `{{first_name}}` | `content` templates; worst case at approval; per recipient before send | `content` tests, `TestApprove_LongestRealFirstName`, `FuzzRenderMessage` |
| Async queue, ≤ 100 msg/s per channel | outbox → Kafka; Postgres token bucket shared by workers | `TestTokenBucket_SharedWindowAcrossInstances`, `TestScenario_RateLimitAcrossWorkers`, `make smoke` |
| Same message never twice to a person | unique (campaign, user, channel); leased claims; idempotency key | `TestScenario_CrashAfterProviderAccepted`, `TestScenario_LeaseExpiresWhileSending`, `TestE2E_PipelineWithKafka` |
| Retry strategy | bounded attempts, exponential backoff + full jitter, `Retry-After` | `TestRetryPolicy_*`, `TestScenario_ProviderFailures` |
| Live status, metrics, health | progress snapshot + SSE, `/metrics`, `/healthz`, `/readyz` | `TestProgress_*`, `TestEvents_*`, `TestHub_*`, `TestProbeRouter_*` |
| Structured, validated LLM output | strict JSON decoding, size caps, bounded repair | `TestGenerator_*`, `FuzzDecodeSegmentOutput`, `FuzzDecodeDrafts` |
| Tests without an API key | `mockllm`; stub server for the Claude adapter | the whole suite |
| Human approval only | approval bound to version + content hash; no LLM code path | `TestApprove_*`, `TestAISafety_InjectionCorpusThroughEndpoints` |
| Scheduled sending (bonus) | `scheduled` approval, release by the worker with all approval checks at the scheduled time, expiry after a grace period | `TestRelease_*`, `TestReleaseDue*`, `TestDueScheduled_*`, `TestE2E_ScheduledCampaignIsReleasedAndDelivered`, `TestNoReleaseEndpoint` |

Details of the scenarios: [Testing](../operations/testing.md).
