-- +goose Up
-- +goose StatementBegin
-- The IANA zone the streak's calendar day is cut in. NULL means the account
-- predates onboarding or never sent one, and those keep falling back to UTC.
ALTER TABLE users
    ADD COLUMN timezone VARCHAR(64) NULL AFTER birth_date;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN timezone;
-- +goose StatementEnd
