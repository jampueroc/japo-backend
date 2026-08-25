-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN email_verified_at TIMESTAMP NULL AFTER password_hash;
-- +goose StatementEnd

-- +goose StatementBegin
-- Accounts created before this migration predate the verification flow.
-- Marking them verified is what keeps them from being locked out the moment
-- the gate is switched on.
UPDATE users SET email_verified_at = CURRENT_TIMESTAMP WHERE email_verified_at IS NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS email_verification_codes (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id     BIGINT UNSIGNED NOT NULL,
    -- The code is stored hashed. A dump of this table must not be enough to
    -- verify somebody else's address.
    code_hash   CHAR(64)        NOT NULL,
    -- A six digit code is only a million possibilities: without a cap it is
    -- brute forceable in minutes.
    attempts    INT UNSIGNED    NOT NULL DEFAULT 0,
    expires_at  TIMESTAMP       NOT NULL,
    consumed_at TIMESTAMP       NULL,
    created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    -- One live code per account: asking for a new one replaces the old.
    UNIQUE KEY uq_email_verification_user (user_id),
    CONSTRAINT fk_email_verification_user
        FOREIGN KEY (user_id) REFERENCES users (id)
        ON DELETE CASCADE
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id     BIGINT UNSIGNED NOT NULL,
    -- Hashed for the same reason as above, and unique because the token in
    -- the emailed link is what the lookup is keyed on.
    token_hash  CHAR(64)        NOT NULL,
    expires_at  TIMESTAMP       NOT NULL,
    consumed_at TIMESTAMP       NULL,
    created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_password_reset_token (token_hash),
    -- One live token per account: a new request invalidates the previous one.
    UNIQUE KEY uq_password_reset_user (user_id),
    CONSTRAINT fk_password_reset_user
        FOREIGN KEY (user_id) REFERENCES users (id)
        ON DELETE CASCADE
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS password_reset_tokens;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS email_verification_codes;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE users DROP COLUMN email_verified_at;
-- +goose StatementEnd
