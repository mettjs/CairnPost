-- Webhooks: where an application is told what became of its emails, and
-- the record of telling it.

-- Whether, with this event, the address was on its sending domain's
-- suppression list. Kept with the event because the list changes: a
-- webhook sent later must say what was so then.
ALTER TABLE events ADD COLUMN suppressed boolean NOT NULL DEFAULT false;

CREATE TABLE webhook_endpoints (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    url         text NOT NULL,
    -- The sending domain whose emails it hears about; NULL for every domain.
    domain      text CHECK (domain = lower(domain) AND domain <> ''),
    -- The kinds of event it hears about; empty for every kind.
    kinds       text[] NOT NULL DEFAULT '{}'
        CHECK (kinds <@ ARRAY['delivered', 'deferred', 'bounced', 'complained', 'failed']),
    description text,
    -- The key each webhook is signed with. Unlike an API key it is kept as
    -- it is, not hashed: signing needs the key itself.
    secret      bytea NOT NULL,
    -- A disabled endpoint is owed nothing new, and what it was owed waits.
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One event owed to one endpoint. Written in the same step as the event,
-- so an event recorded is a webhook owed, whatever happens next.
CREATE TABLE webhook_deliveries (
    -- Also the webhook's id as its receiver sees it: the same on every try.
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    endpoint_id  uuid NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    event_id     uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    --   pending:   owed; tried again at next_at.
    --   delivered: the endpoint answered 2xx.
    --   failed:    every try was used up.
    state        text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'failed')),
    attempts     integer NOT NULL DEFAULT 0,
    next_at      timestamptz NOT NULL DEFAULT now(),
    -- While a try is being made the delivery is its sender's until then;
    -- a sender that dies leaves it to come due again.
    taken_until  timestamptz,
    -- What the last try met: the endpoint's status, or why there was none.
    -- Never the endpoint's own words.
    last_status  integer,
    last_failure text CHECK (last_failure IN ('timeout', 'refused', 'dns', 'tls', 'blocked', 'status', 'other')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    attempted_at timestamptz,
    delivered_at timestamptz,
    UNIQUE (endpoint_id, event_id)
);

CREATE INDEX webhook_deliveries_due ON webhook_deliveries (endpoint_id, next_at) WHERE state = 'pending';
CREATE INDEX webhook_deliveries_by_event ON webhook_deliveries (event_id);
-- An endpoint's deliveries are listed newest first; ids are in time order.
CREATE INDEX webhook_deliveries_by_endpoint ON webhook_deliveries (endpoint_id, id DESC);
