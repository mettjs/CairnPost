-- An email may go to several addresses: "to", "cc" and "bcc". Each
-- address keeps a row of its own, since each has its own outcome, its own
-- events and its own place on the suppression list; the rows of one email
-- name it by the id of its first address's row, which is the email's id.
ALTER TABLE messages ADD COLUMN email_id uuid;
ALTER TABLE messages ADD COLUMN kind text NOT NULL DEFAULT 'to' CHECK (kind IN ('to', 'cc', 'bcc'));
UPDATE messages SET email_id = id;
ALTER TABLE messages ALTER COLUMN email_id SET NOT NULL;
-- The rows of an email go with it.
ALTER TABLE messages ADD CONSTRAINT messages_email_id_fkey FOREIGN KEY (email_id) REFERENCES messages (id) ON DELETE CASCADE;
CREATE INDEX messages_by_email ON messages (email_id, id);
-- The addresses an email was asked to go to and did not, being suppressed
-- when it was sent: kept with the email, on its first row, so that who
-- was left out can still be said after the send's own answer.
ALTER TABLE messages ADD COLUMN left_out text[] NOT NULL DEFAULT '{}';
