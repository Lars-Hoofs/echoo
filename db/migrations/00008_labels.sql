-- +goose Up
CREATE TABLE labels (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text        NOT NULL CHECK (name <> '' AND char_length(name) <= 50),
    color_token text        NOT NULL CHECK (color_token IN ('slate', 'blue', 'teal', 'green', 'amber', 'orange', 'red', 'violet')),
    description text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 200),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX labels_name_key ON labels (lower(name));

CREATE TABLE conversation_labels (
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    label_id        uuid NOT NULL REFERENCES labels (id) ON DELETE CASCADE,
    PRIMARY KEY (conversation_id, label_id)
);
CREATE INDEX conversation_labels_label ON conversation_labels (label_id);

-- The wake-up job scans only conversations that are currently snoozed.
CREATE INDEX conversations_snoozed ON conversations (snoozed_until)
    WHERE snoozed_until IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP INDEX conversations_snoozed;
DROP TABLE conversation_labels;
DROP TABLE labels;
