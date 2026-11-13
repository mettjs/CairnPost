-- What became of an email after the upstream took it: events, and the
-- states they move a message to.

-- A message's state now goes on past "sent".
--   deferred:   the receiving server said "not now"; the upstream retries.
--   delivered:  the receiving server took it.
--   bounced:    the receiving server refused it.
--   complained: it arrived, and its recipient marked it as spam.
-- "failed" also covers an email the upstream took and then could not or
-- would not send.
ALTER TABLE messages DROP CONSTRAINT messages_state_check;
ALTER TABLE messages ADD CONSTRAINT messages_state_check CHECK (state IN
    ('sending', 'sent', 'failed', 'unknown', 'deferred', 'delivered', 'bounced', 'complained'));

-- An event names its email by the upstream's id.
CREATE INDEX messages_by_upstream ON messages (driver, upstream_id) WHERE upstream_id IS NOT NULL;

CREATE TABLE events (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    message_id        uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    driver            text NOT NULL,
    -- The upstream's own id for the event. An upstream may hand the same
    -- event over twice; this makes it one.
    upstream_event_id text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('delivered', 'deferred', 'bounced', 'complained', 'failed')),
    -- For a bounce: the address will never take mail (a hard bounce).
    permanent         boolean NOT NULL DEFAULT false,
    -- The receiving server's status ("5.1.1") and the upstream's short name
    -- for what happened ("permanent_failure"). Never a sentence: a server's
    -- own words can quote the email, and are never kept here.
    code              text,
    reason            text,
    occurred_at       timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (driver, upstream_event_id)
);

CREATE INDEX events_by_message ON events (message_id, occurred_at);
