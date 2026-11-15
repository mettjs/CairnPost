-- An email may be asked for now and sent later. Until its time it waits
-- here, apart from the emails that were sent: nothing about it counts as
-- a send, and when its time comes it is sent as any other is, under the
-- id it was given here.
--
-- Unlike any table before it, this one holds what an email says, and
-- the next one its files: there is no other way to send it later. Both
-- go when the email is sent or cancelled.
CREATE TABLE scheduled_emails (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    api_key_id    uuid NOT NULL REFERENCES api_keys (id),
    sender        text NOT NULL,
    sender_domain text NOT NULL,
    -- Who it goes to, in order: [{"address": ..., "kind": "to"}].
    recipients    jsonb NOT NULL,
    send_at       timestamptz NOT NULL,
    -- scheduled: waiting for its time, or for another try.
    -- sending:   taken by a server that is sending it now.
    -- cancelled: it will not be sent; what it said is gone.
    state         text NOT NULL CHECK (state IN ('scheduled', 'sending', 'cancelled')),
    -- Why it was cancelled when nobody asked: "suppressed" (nobody it was
    -- addressed to could be sent to any more), "key_revoked" or "failed"
    -- (this server could not send it, each time it tried).
    reason        text NOT NULL DEFAULT '',
    -- When it is next to be tried: send_at, until a try finds the mail
    -- provider not taking mail.
    next_try_at   timestamptz NOT NULL,
    -- How many times it was begun: handed, or about to be, to the mail
    -- provider.
    tries         integer NOT NULL DEFAULT 0,
    -- Which taking this is: whoever took it an earlier time, and was
    -- held to have stopped, can change nothing about it any more.
    claim         integer NOT NULL DEFAULT 0,
    claimed_at    timestamptz,
    -- What it says, as JSON kept as bytes: never searched. NULL once
    -- cancelled.
    words         bytea,
    -- How much it holds, words and files: what may wait is bounded by it.
    bytes         bigint NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX scheduled_emails_due ON scheduled_emails (next_try_at) WHERE state = 'scheduled';
CREATE INDEX scheduled_emails_by_domain ON scheduled_emails (sender_domain, id DESC);
-- For finding what a stopped server had taken, and what a revoked key
-- had asked for.
CREATE INDEX scheduled_emails_taken ON scheduled_emails (claimed_at) WHERE state = 'sending';
CREATE INDEX scheduled_emails_by_key ON scheduled_emails (api_key_id) WHERE state = 'scheduled';

-- The files of an email that waits, with what they hold.
CREATE TABLE scheduled_files (
    email_id     uuid NOT NULL REFERENCES scheduled_emails (id) ON DELETE CASCADE,
    position     integer NOT NULL,
    filename     text NOT NULL,
    content_type text NOT NULL,
    content_id   text NOT NULL DEFAULT '',
    content      bytea NOT NULL,
    PRIMARY KEY (email_id, position)
);

-- An Idempotency-Key may now name an email that waits, which is not
-- among the messages until it is sent.
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_message_id_fkey;
