---
type: Service
title: Seed data
description: The one-shot seed service that loads deterministic, clearly fake users, consents, cart events, and orders.
tags: [operations, seed, data]
---

The `seed` service (`cmd/seed`) loads deterministic fake data once, right
after migrations: 5000 users across 8 Turkish cities (İstanbul is the
largest), `tr`/`en` locales, mixed SMS/email consent (granted, explicitly
denied, or never asked), and cart events and orders spread over the last
90 days relative to seed time. A dozen fixed scenario users guarantee
coverage of the example segment: Istanbul cart-abandoners with every
consent combination, a user who added to cart and then purchased, and a
few very long names for SMS-length tests.

- **Clearly fake recipients.** Emails are `…@example.com` (reserved domain)
  and phones are `+9995XXXXXXXXX` (`+999` is not assigned to any country).
  CHECK constraints in the schema reject anything else.
- **Deterministic.** `SEED_USER_COUNT` and `SEED_RANDOM_SEED` fully
  determine the data (timestamps are relative to when the seed ran).
- **Idempotent.** If `users` already has rows, the seed logs
  `database already seeded; nothing to do` and exits. To reseed, remove the
  Postgres volume (`docker compose down -v`, deletes local data) and start
  again.

# Examples

```bash
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SELECT city, count(*) FROM users GROUP BY city ORDER BY 2 DESC"'
```
