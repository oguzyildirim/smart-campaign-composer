---
type: API Endpoint
title: Health
description: Liveness (/healthz) and readiness (/readyz) probes served by the api, each worker, and the mock provider.
resource: /readyz
tags: [api, health, operations]
---

The api (`:8080`), each worker (`:8081`), and the mock provider (`:9090`)
serve both probes; Compose healthchecks use `/readyz`.

- `GET /healthz`: liveness. Always `200 {"status":"ok"}` while the process
  serves HTTP.
- `GET /readyz`: readiness. Pings Postgres and Kafka concurrently within
  `READINESS_TIMEOUT`. Returns `200` when all checks pass and `503`
  otherwise:

  ```json
  {"status":"unavailable","checks":{"kafka":"ok","postgres":"unavailable"}}
  ```

Error details are logged server-side only. The mock provider has no
dependencies, so its `/readyz` is `200` whenever it serves.
