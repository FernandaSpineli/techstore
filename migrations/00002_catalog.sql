-- +goose Up
CREATE TABLE categories (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER categories_set_updated_at
    BEFORE UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Products are archived rather than deleted once they have been sold, so
-- order history keeps pointing at real rows.
CREATE TABLE products (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    category_id uuid NOT NULL REFERENCES categories (id) ON DELETE RESTRICT,
    name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    brand       text NOT NULL CHECK (char_length(brand) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    -- 'simple' config: product names and brands are mostly proper nouns, so
    -- language-specific stemming would hurt more than help.
    search      tsvector GENERATED ALWAYS AS (
                    setweight(to_tsvector('simple', name), 'A') ||
                    setweight(to_tsvector('simple', brand), 'B') ||
                    setweight(to_tsvector('simple', description), 'C')
                ) STORED,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX products_category_id_idx ON products (category_id);
CREATE INDEX products_search_idx ON products USING gin (search);
-- The public catalog only lists active products, newest first.
CREATE INDEX products_active_created_at_idx ON products (created_at DESC) WHERE status = 'active';

CREATE TRIGGER products_set_updated_at
    BEFORE UPDATE ON products
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A variant is the sellable unit (e.g. "256GB / Black"). Price lives here
-- because it varies per variant.
CREATE TABLE product_variants (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    product_id  uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    sku         text NOT NULL UNIQUE CHECK (sku ~ '^[A-Z0-9][A-Z0-9-]{1,63}$'),
    name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    attributes  jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(attributes) = 'object'),
    price_cents bigint NOT NULL CHECK (price_cents > 0),
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX product_variants_product_id_idx ON product_variants (product_id);
CREATE INDEX product_variants_price_cents_idx ON product_variants (price_cents) WHERE active;

CREATE TRIGGER product_variants_set_updated_at
    BEFORE UPDATE ON product_variants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- available = on_hand - reserved. Reserved units belong to orders awaiting
-- payment; the CHECKs make overselling impossible at the database level.
CREATE TABLE inventory (
    variant_id uuid PRIMARY KEY REFERENCES product_variants (id) ON DELETE CASCADE,
    on_hand    integer NOT NULL DEFAULT 0 CHECK (on_hand >= 0),
    reserved   integer NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_reserved_within_on_hand CHECK (reserved <= on_hand)
);

CREATE TRIGGER inventory_set_updated_at
    BEFORE UPDATE ON inventory
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE inventory;
DROP TABLE product_variants;
DROP TABLE products;
DROP TABLE categories;
