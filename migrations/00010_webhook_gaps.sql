-- Three things webhooks lacked: an endpoint that keeps failing is paused
-- instead of being tried for ever, a signing secret can be changed, and an
-- address suppressed because of an email with no record here is told too.

-- failing_since is when the first of the tries that have all failed since
-- was made; an answer clears it. When a webhook is given up and the
-- failing is long enough ago, the endpoint is paused: it is still owed
-- what happens, and nothing is tried until somebody resumes it.
ALTER TABLE webhook_endpoints ADD COLUMN failing_since timestamptz;
ALTER TABLE webhook_endpoints ADD COLUMN paused_at timestamptz;

-- The signing keys an endpoint had before a change, each with until when
-- webhooks are signed with it as well as with the new one: a receiver has
-- that long to start using the new key. Every key still within its time
-- signs, so a second change soon after a first does not cut off a
-- receiver that still holds the first key.
CREATE TABLE webhook_previous_secrets (
    endpoint_id uuid NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    secret      bytea NOT NULL,
    until       timestamptz NOT NULL
);
CREATE INDEX webhook_previous_secrets_by_endpoint ON webhook_previous_secrets (endpoint_id, until DESC);
CREATE INDEX webhook_previous_secrets_by_end ON webhook_previous_secrets (until);

-- "suppressed" is not an event of an email: it is an address put on the
-- suppression list because of a bounce or a complaint about an email that
-- has no record here, which therefore has no event to be told by.
ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_kinds_check;
ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_kinds_check
    CHECK (kinds <@ ARRAY['delivered', 'deferred', 'bounced', 'complained', 'failed', 'suppressed']);

-- A delivery is owed for an event, or for such a suppression: one of the
-- two. A suppression that is deleted takes what was owed for it along.
-- Why it was suppressed is kept with the delivery, as it was then: the
-- suppression's own row can change, and a webhook must say the same on
-- every try.
ALTER TABLE webhook_deliveries ALTER COLUMN event_id DROP NOT NULL;
ALTER TABLE webhook_deliveries ADD COLUMN suppression_domain text;
ALTER TABLE webhook_deliveries ADD COLUMN suppression_address text;
ALTER TABLE webhook_deliveries ADD COLUMN suppression_reason text;
ALTER TABLE webhook_deliveries ADD CONSTRAINT webhook_deliveries_suppression_fkey
    FOREIGN KEY (suppression_domain, suppression_address) REFERENCES suppressions (domain, address) ON DELETE CASCADE;
ALTER TABLE webhook_deliveries ADD CONSTRAINT webhook_deliveries_about_check
    CHECK ((event_id IS NULL) <> (suppression_domain IS NULL)
       AND (suppression_domain IS NULL) = (suppression_address IS NULL)
       AND (suppression_domain IS NULL) = (suppression_reason IS NULL));
CREATE UNIQUE INDEX webhook_deliveries_by_suppression
    ON webhook_deliveries (suppression_domain, suppression_address, endpoint_id)
    WHERE suppression_domain IS NOT NULL;
