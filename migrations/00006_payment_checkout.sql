-- +goose Up
-- The hosted checkout page and when it closes, so an open session can be
-- handed back to the customer instead of creating a new one. The table is
-- empty until checkout exists, so NOT NULL is safe without a default.
ALTER TABLE payments
    ADD COLUMN checkout_url text NOT NULL,
    ADD COLUMN expires_at   timestamptz NOT NULL;

-- +goose Down
ALTER TABLE payments
    DROP COLUMN expires_at,
    DROP COLUMN checkout_url;
