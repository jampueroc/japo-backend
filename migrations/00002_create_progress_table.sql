-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS progress (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id    BIGINT UNSIGNED NOT NULL,
    data       LONGTEXT        NOT NULL CHECK (JSON_VALID(data)),
    created_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    -- One document per user: this unique key is what makes
    -- INSERT ... ON DUPLICATE KEY UPDATE behave as an upsert.
    UNIQUE KEY uq_progress_user_id (user_id),
    CONSTRAINT fk_progress_user
        FOREIGN KEY (user_id) REFERENCES users (id)
        ON DELETE CASCADE
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS progress;
-- +goose StatementEnd
