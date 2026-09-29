-- +goose Up
ALTER TABLE mailboxes ADD COLUMN allow_internal_host boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE mailboxes DROP COLUMN allow_internal_host;
