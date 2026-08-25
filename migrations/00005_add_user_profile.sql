-- +goose Up
-- +goose StatementBegin
-- Identity captured during onboarding, after the address is verified. All
-- three are nullable: an account exists before its owner has filled anything
-- in, and a profile is considered complete once name is set.
ALTER TABLE users
    ADD COLUMN name       VARCHAR(80) NULL AFTER email,
    -- A short token rather than an enum: adding an option should be a
    -- deploy of the client, not a migration of the database.
    ADD COLUMN gender     VARCHAR(16) NULL AFTER name,
    ADD COLUMN birth_date DATE        NULL AFTER gender;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN birth_date,
    DROP COLUMN gender,
    DROP COLUMN name;
-- +goose StatementEnd
