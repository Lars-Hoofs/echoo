-- +goose Up
-- Recipients are resolved with the visibility of whoever starts the campaign, not of whoever
-- wrote the draft. Campaigns started before this column existed keep resolving as their creator.
ALTER TABLE campaigns ADD COLUMN started_by uuid REFERENCES users (id) ON DELETE SET NULL;
UPDATE campaigns SET started_by = created_by WHERE status <> 'draft';

-- +goose Down
ALTER TABLE campaigns DROP COLUMN started_by;
