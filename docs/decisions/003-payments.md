# ADR 003 — Payments with Stripe Checkout

- **Status:** accepted
- **Date:** 2026-10-07

## Context

Orders need to be paid by card and, in Brazil, by delayed methods such as
boleto. The service must never handle card data, must never mark an order
paid without proof, and must cope with Stripe's at-least-once webhook
delivery.

## Decisions

**Stripe Checkout (hosted page), not Payment Intents with our own form.**
Card data never reaches TechStore, which keeps it out of PCI scope beyond
SAQ A. Line items are built from the stored order (`price_data`), so a client
cannot change what it pays.

**Only a signed webhook marks an order paid.** The `success_url` redirect is
cosmetic; anyone can open it. Webhooks are verified with the endpoint secret
and a 5-minute timestamp tolerance (replays are rejected). The event's API
version is not enforced because only stable Checkout Session fields are read.

**Idempotency at three levels:**

| Risk | Defence |
| --- | --- |
| Client double-clicks "pay" | An open session is returned instead of creating another |
| Concurrent checkout requests | Stripe idempotency key `checkout-{order}-{attempt}` returns the same session; `payments_one_pending_per_order` index |
| Webhook delivered twice | `stripe_events` row inserted in the same transaction as the effects; a duplicate is acknowledged without effect |
| Order paid twice | `payments_one_succeeded_per_order` index; a second success is flagged for manual refund |

**The amount is checked.** A session whose `amount_total`/currency differ from
the order is recorded as failed and logged as `payment.amount_mismatch`.

**Reservation outlives the session.** Stripe sessions last at least
30 minutes. Creating a checkout extends the order's stock reservation to the
session's expiry plus 5 minutes, so the expiry sweeper never closes an order
that can still be paid.

**Delayed methods.** `checkout.session.completed` with `payment_status=unpaid`
leaves the order pending; `async_payment_succeeded` pays it and
`async_payment_failed` cancels it and releases the stock. An expired session
only expires the payment: the customer can start a new checkout until the
order's reservation runs out.

**Cancelling during an open checkout is refused** (`409 PAYMENT_IN_PROGRESS`),
since the customer could still pay. Expiring the Stripe session on cancel was
considered; it adds a synchronous Stripe call to cancellation and was left
out for simplicity.

**Late payments are not silently accepted.** A payment that succeeds for an
order that is no longer pending is recorded, the order is not reopened, and
`payment.requires_refund` is logged at error level for manual handling.

**Stripe calls happen outside database transactions.** Checkout is split in
two short transactions around the API call, so a slow Stripe never holds row
locks.

## Consequences

- Refunds are a manual, logged operation; automating them is future work.
- Payments are optional outside production: without keys, checkout returns
  `503 PAYMENTS_UNAVAILABLE`. Live keys are refused unless
  `APP_ENV=production`.
