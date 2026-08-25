-- +goose Up
-- +goose StatementBegin
-- Server-authoritative activity counters. They are derived from the server
-- clock (UTC calendar days) so a client cannot forge a streak.
ALTER TABLE users
    ADD COLUMN last_active_date    DATE            NULL     AFTER password_hash,
    ADD COLUMN distinct_login_days INT UNSIGNED    NOT NULL DEFAULT 0 AFTER last_active_date,
    ADD COLUMN streak_days         INT UNSIGNED    NOT NULL DEFAULT 0 AFTER distinct_login_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN streak_days,
    DROP COLUMN distinct_login_days,
    DROP COLUMN last_active_date;
-- +goose StatementEnd
