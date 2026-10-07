-- +goose Up
CREATE TABLE orders (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    status      text NOT NULL DEFAULT 'pending_payment'
                CHECK (status IN ('pending_payment', 'paid', 'shipped', 'delivered', 'cancelled', 'expired')),
    currency    text NOT NULL CHECK (currency ~ '^[a-z]{3}$'),
    total_cents bigint NOT NULL CHECK (total_cents > 0),
    -- Stock for a pending order stays reserved until this moment.
    expires_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT orders_pending_has_expiry CHECK (status <> 'pending_payment' OR expires_at IS NOT NULL)
);

CREATE INDEX orders_user_id_created_at_idx ON orders (user_id, created_at DESC);
CREATE INDEX orders_status_created_at_idx ON orders (status, created_at DESC);
-- Lets the expiry sweeper find stale reservations without scanning all orders.
CREATE INDEX orders_pending_expires_at_idx ON orders (expires_at) WHERE status = 'pending_payment';

CREATE TRIGGER orders_set_updated_at
    BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Items snapshot name, SKU and price at purchase time, so later catalog
-- edits never change what the customer paid for.
CREATE TABLE order_items (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id         uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    variant_id       uuid NOT NULL REFERENCES product_variants (id) ON DELETE RESTRICT,
    product_name     text NOT NULL,
    variant_name     text NOT NULL,
    sku              text NOT NULL,
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents > 0),
    quantity         integer NOT NULL CHECK (quantity > 0),
    line_total_cents bigint GENERATED ALWAYS AS (unit_price_cents * quantity) STORED,
    UNIQUE (order_id, variant_id)
);

CREATE INDEX order_items_variant_id_idx ON order_items (variant_id);

CREATE TABLE order_status_history (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id    uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    from_status text,
    to_status   text NOT NULL,
    reason      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX order_status_history_order_id_idx ON order_status_history (order_id, created_at);

-- +goose Down
DROP TABLE order_status_history;
DROP TABLE order_items;
DROP TABLE orders;
