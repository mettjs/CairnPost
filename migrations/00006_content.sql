-- Content retention: an operator may choose to keep what an email said,
-- for a short time, to see what was sent when something goes wrong. It is
-- off unless they turn it on, and no other table ever holds an email's
-- words.
CREATE TABLE message_contents (
    message_id uuid PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    -- The subject, the bodies and the headers, as JSON kept as bytes: an
    -- email's words are stored as they came and never searched.
    content    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- When it is to be deleted, decided when it was kept: whoever prunes
    -- goes by this, whatever their own setting is by then.
    expires_at timestamptz NOT NULL
);

CREATE INDEX message_contents_by_expiry ON message_contents (expires_at);

-- With content kept, so is the sentence a receiving server gave for a
-- bounce or a delay: it says why, and it can quote the email, which is why
-- it is kept here, under the same rule, and not with the event. As bytes,
-- for the same reason: whatever a server said, the event is recorded.
CREATE TABLE event_details (
    event_id   uuid PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
    detail     bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- The same instant as its email's content: counted from the send, not
    -- from the event, so it does not outlive the words it may quote.
    expires_at timestamptz NOT NULL
);

CREATE INDEX event_details_by_expiry ON event_details (expires_at);
