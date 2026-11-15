-- An email that came for an address of a sending domain, handed on by
-- the mail provider. It is kept as it came, for an application to read,
-- for a short time the operator sets, and deleted then. What it says is
-- shown to admin keys only, and is never logged.
--
-- The same email handed on twice for the same address is one: digest is
-- the SHA-256 of raw.
CREATE TABLE inbound_emails (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    domain      text NOT NULL,
    recipient   text NOT NULL,
    -- Who it came from, as the sending server said; '' for a bounce.
    sender      text NOT NULL DEFAULT '',
    digest      bytea NOT NULL,
    raw         bytea NOT NULL,
    bytes       integer NOT NULL,
    -- What a list shows of it, as it was read when it came.
    subject     text NOT NULL DEFAULT '',
    from_header text NOT NULL DEFAULT '',
    files       integer NOT NULL DEFAULT 0,
    partial     boolean NOT NULL DEFAULT false,
    received_at timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    UNIQUE (recipient, digest)
);

CREATE INDEX inbound_emails_by_domain ON inbound_emails (domain, id DESC);
CREATE INDEX inbound_emails_by_expiry ON inbound_emails (expires_at);

-- "received" is what an endpoint is told of such an email.
ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_kinds_check;
ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_kinds_check
    CHECK (kinds <@ ARRAY['delivered', 'deferred', 'bounced', 'complained', 'failed', 'suppressed', 'received']);

-- A delivery is owed for one of three: an event, a suppression told on
-- its own, or an email that came. One that is deleted takes what was
-- owed for it along.
ALTER TABLE webhook_deliveries ADD COLUMN inbound_id uuid REFERENCES inbound_emails (id) ON DELETE CASCADE;
ALTER TABLE webhook_deliveries DROP CONSTRAINT webhook_deliveries_about_check;
ALTER TABLE webhook_deliveries ADD CONSTRAINT webhook_deliveries_about_check
    CHECK (num_nonnulls(event_id, suppression_domain, inbound_id) = 1
       AND (suppression_domain IS NULL) = (suppression_address IS NULL)
       AND (suppression_domain IS NULL) = (suppression_reason IS NULL));
CREATE UNIQUE INDEX webhook_deliveries_by_inbound
    ON webhook_deliveries (inbound_id, endpoint_id) WHERE inbound_id IS NOT NULL;
