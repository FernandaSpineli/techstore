# ADR 001 — Database schema and data access

- **Status:** accepted
- **Date:** 2026-10-07

## Context

TechStore needs a relational store for users, catalog, carts, orders and
payments. The most important correctness property of an e-commerce backend is
that it never oversells stock and never charges twice, even under concurrent
requests and duplicated webhooks.

## Decisions

**PostgreSQL 18, accessed with pgx and hand-written SQL.** Queries are short
and domain-specific; an ORM would hide the locking and conditional updates
that the inventory logic depends on. sqlc was considered but adds a code
generation step for little gain at this size.

**Migrations with goose, embedded in the binary.** `api migrate up` runs the
same SQL in every environment, with no files needed on the host. Goose takes
an advisory lock, so concurrent runs are safe. Every migration has a working
`Down`; a test migrates to zero and back.

**UUIDv7 primary keys (`uuidv7()`, native since PostgreSQL 18).** IDs in URLs
cannot be enumerated, and unlike random UUIDv4 they are time-ordered, so
B-tree inserts stay append-mostly.

**Money as `bigint` cents plus a currency code.** No floating point anywhere
near prices. Stripe also works in the smallest currency unit.

**Invariants enforced by the database, not only by Go code:**

| Invariant | Mechanism |
| --- | --- |
| Never sell more than exists | `CHECK (reserved <= on_hand)` and `CHECK (on_hand >= 0)` on `inventory` |
| One open checkout / one successful payment per order | Partial unique indexes on `payments` |
| Each Stripe event processed once | `stripe_events.id` primary key, written in the same transaction as the side effects |
| Order history survives catalog changes | `order_items` snapshot name, SKU and price; `ON DELETE RESTRICT` to variants |
| `updated_at` is always correct | `set_updated_at()` trigger |

**Stock reservation lives on the order.** An order in `pending_payment` holds
its items' quantities in `inventory.reserved` until `orders.expires_at`. A
separate reservations table was considered, but it would duplicate
`order_items` exactly; the order's status and items already describe what is
reserved.

**Search with a generated `tsvector` column and a GIN index**, using the
`simple` text configuration, because product names and brands are proper
nouns that language stemming would mangle. This avoids running a separate
search engine.

## Consequences

- Constraint violations surface as PostgreSQL errors (`23505`, `23514`, …)
  that repositories translate into domain errors.
- Products that have been sold cannot be hard-deleted; the API archives them.
- PostgreSQL 18 is required (for `uuidv7()`).
