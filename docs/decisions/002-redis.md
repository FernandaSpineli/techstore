# ADR 002 — What Redis is (and is not) used for

- **Status:** accepted
- **Date:** 2026-10-07

## Context

Redis is part of the stack, but every use must earn its place: data that has
to be durable or change atomically with orders belongs in PostgreSQL.

## Decisions

### Used for

**1. Rate limiting.** Fixed-window counters (`INCR` + `EXPIRE NX` in one
transaction) per client IP: 10/min on sign-up, login, refresh and password
reset (credential stuffing, reset-email abuse) and 300/min on the whole API.
The counter must be shared by every API instance, which rules out in-memory
limiters. A fixed window can admit up to twice the limit across a window
boundary; that is acceptable for abuse protection and keeps each check to
one round trip. Health probes are exempt.

**2. Public catalog cache.** Product listings, product pages and categories
are read far more often than written. Responses are cached as ready-to-send
JSON under a namespace version (`cache:catalog:v{N}:{path}?{sorted query}`);
every successful admin write to the catalog or to stock bumps `N` with one
`INCR`, making all older entries unreachable at once. The TTL (60 s) bounds
the only staleness that remains: the `in_stock` flag after orders change
stock. That flag is informational — cart and order creation always re-check
stock in PostgreSQL under lock. `X-Cache: HIT|MISS` makes the behaviour
visible.

**3. Idempotency keys for `POST /orders`.** A client that lost a response can
retry with the same `Idempotency-Key` and receive the original `201` instead
of an error or a second order. Keys are scoped per user, expire after 24 h,
are bound to a fingerprint of the request (reuse with a different request is
`422`), and results below 500 are replayed, as in Stripe's API.

### Not used for

- **Carts** stay in PostgreSQL: every shopper is signed in, carts must
  survive restarts, and order creation reads and clears the cart in the same
  transaction that reserves stock.
- **Webhook idempotency** stays in PostgreSQL (`stripe_events`): the
  "already processed" mark must commit atomically with the order update.
- **Sessions**: access tokens are stateless JWTs and refresh tokens are
  stored (hashed) in PostgreSQL alongside the user.

## Failure mode

Redis is an optimisation and a safeguard, not a source of truth. If it is
unreachable, rate limiting and caching are skipped and idempotency keys are
ignored (each logged as a warning); the shop keeps working. Duplicate orders
are still prevented by the cart row lock, since the second request finds an
empty cart.
