-- +goose Up
CREATE TABLE payments (
    id                         uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id                   uuid NOT NULL REFERENCES orders (id) ON DELETE RESTRICT,
    stripe_checkout_session_id text NOT NULL UNIQUE,
    stripe_payment_intent_id   text UNIQUE,
    status                     text NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'succeeded', 'failed', 'expired')),
    amount_cents               bigint NOT NULL CHECK (amount_cents > 0),
    currency                   text NOT NULL CHECK (currency ~ '^[a-z]{3}$'),
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payments_order_id_idx ON payments (order_id);
-- At most one open checkout and one successful payment per order: a
-- duplicated request or webhook cannot charge a customer twice.
CREATE UNIQUE INDEX payments_one_pending_per_order ON payments (order_id) WHERE status = 'pending';
CREATE UNIQUE INDEX payments_one_succeeded_per_order ON payments (order_id) WHERE status = 'succeeded';

CREATE TRIGGER payments_set_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Stripe delivers webhooks at least once. Recording the event ID in the same
-- transaction as its side effects makes processing idempotent.
CREATE TABLE stripe_events (
    id           text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE stripe_events;
DROP TABLE payments;
