---
type: API Endpoint
title: Segment preview
description: POST /api/v1/segments/preview validates a segment rule and returns matching and consented user counts.
resource: /api/v1/segments/preview
tags: [api, segment]
---

# Rule format

A segment rule is a small JSON document. Conditions are ANDed, and each
field may appear once.

| Field                       | Ops          | Value                                         |
| --------------------------- | ------------ | --------------------------------------------- |
| `city`                      | `eq`, `in`   | string, or array of ≤ 20 strings; 1–64 letters; case- and Turkish-letter-insensitive (`Istanbul` = `İSTANBUL`) |
| `locale`                    | `eq`, `in`   | `tr`, `en`                                    |
| `added_to_cart_within_days` | `eq`         | integer 1–365: ≥ 1 cart event in the window   |
| `no_purchase_within_days`   | `eq`         | integer 1–365: no order in the window         |
| `purchased_within_days`     | `eq`         | integer 1–365: ≥ 1 order in the window        |
| `order_count`               | `gte`, `lte` | integer 0–10000 (all-time orders)             |

"Within N days" means the last `N × 24h` up to the request time.

# Examples

```bash
curl -s -X POST http://localhost:8080/api/v1/segments/preview \
  -H 'Content-Type: application/json' \
  -d '{"version":1,"conditions":[
        {"field":"city","op":"eq","value":"Istanbul"},
        {"field":"added_to_cart_within_days","op":"eq","value":30},
        {"field":"no_purchase_within_days","op":"eq","value":30}]}'
```

```json
{"rule":{"version":1,"conditions":[ ...normalized... ]},
 "counts":{"total":390,"sms":263,"email":247}}
```

`total` counts every matching user. `sms` and `email` count only users who
granted consent for that channel. The response echoes the **normalized**
rule (canonical condition order, trimmed and de-duplicated values).

# Validation

Invalid rules return `400` with field-level details and never reach the
database:

```json
{"error":"invalid segment rule",
 "details":[{"path":"conditions[0].value","message":"city may contain only letters, spaces, hyphens, and periods"}]}
```

Rejected with `400`: malformed JSON, unknown or duplicate JSON keys,
unknown fields or operators, wrong value types (e.g. `"30"` or `30.5` for
days), out-of-range numbers, repeated fields, more than 10 conditions, and
bodies over 16 KB. A non-JSON `Content-Type` gets `415`. Server errors
return a generic `500`. The same path works through the frontend origin
(`http://localhost:5173/api/v1/...`).

See also [Campaign flow](../flows/campaign-flow.md), where the AI proposes
these rules.
