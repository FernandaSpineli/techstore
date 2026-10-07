-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

-- Keeps updated_at correct no matter which code path updates a row.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    email         citext NOT NULL UNIQUE,
    password_hash text NOT NULL,
    name          text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    role          text NOT NULL DEFAULT 'customer' CHECK (role IN ('customer', 'admin')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Tokens are stored as SHA-256 hashes: a database leak does not expose
-- usable tokens.
CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);

CREATE TABLE password_reset_tokens (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

-- +goose Down
DROP TABLE password_reset_tokens;
DROP TABLE refresh_tokens;
DROP TABLE users;
DROP FUNCTION set_updated_at();
