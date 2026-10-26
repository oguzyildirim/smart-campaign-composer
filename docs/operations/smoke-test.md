---
type: Runbook
title: Smoke test
description: make smoke runs the example campaign end to end against the running stack and checks delivery invariants.
resource: ../../cmd/smoke
tags: [operations, testing]
---

With the stack running (`docker compose up -d --wait`):

```bash
make smoke    # API_PORT=… MOCK_PROVIDER_PORT=… if you changed them in .env
```

`cmd/smoke` waits for readiness, creates the example campaign, streams the
drafts, approves them, and polls progress until the campaign completes. It
then compares the result with the [mock provider's](../api/mock-provider.md)
statistics:

- every delivery is terminal;
- the provider delivered exactly the messages recorded as sent;
- no recipient got a duplicate;
- no provider-side throttling occurred;
- the peak rate is at most 100 requests per channel per second.

It exits non-zero on any failed check.
