-- +goose Up
CREATE TABLE carts (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id    uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER carts_set_updated_at
    BEFORE UPDATE ON carts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE cart_items (
    cart_id    uuid NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    variant_id uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    quantity   integer NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cart_id, variant_id)
);

-- Supports the ON DELETE CASCADE from product_variants.
CREATE INDEX cart_items_variant_id_idx ON cart_items (variant_id);

CREATE TRIGGER cart_items_set_updated_at
    BEFORE UPDATE ON cart_items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE cart_items;
DROP TABLE carts;
