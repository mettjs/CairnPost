-- Where an operator keeps what emails say, an email's files are kept
-- with it, for the same time: to see what was sent, files and all. They
-- hang on the kept words, so they go when those do, whoever deletes
-- them: the pruner, a cancelled email, the email's record.
CREATE TABLE message_files (
    message_id   uuid NOT NULL REFERENCES message_contents (message_id) ON DELETE CASCADE,
    position     integer NOT NULL,
    filename     text NOT NULL,
    content_type text NOT NULL,
    content_id   text NOT NULL DEFAULT '',
    content      bytea NOT NULL,
    PRIMARY KEY (message_id, position)
);
