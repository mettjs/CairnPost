-- The first schema: who may call, what was sent, and which sends were
-- already taken. No table holds a subject or a body.

CREATE TABLE api_keys (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text NOT NULL CHECK (btrim(name) <> ''),
    -- The public start of the key: shown in lists, used to find the row.
    prefix     text NOT NULL UNIQUE,
    -- SHA-256 of the whole key. The key itself is shown once and never kept.
    hash       bytea NOT NULL,
    scope      text NOT NULL CHECK (scope IN ('send', 'admin')),
    -- The one sending domain this key may send from; NULL for any.
    domain     text CHECK (domain IS NULL OR domain = lower(btrim(domain))),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE TABLE messages (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    api_key_id  uuid NOT NULL REFERENCES api_keys (id),
    sender      text NOT NULL,
    recipient   text NOT NULL,
    driver      text NOT NULL,
    -- The upstream's own id for the message, once it took it.
    upstream_id text,
    -- sending: handed to the upstream, its answer not yet known.
    -- sent:    the upstream took it.
    -- failed:  the upstream refused it; nothing was sent.
    -- unknown: the answer was lost and the send was made again.
    state       text NOT NULL CHECK (state IN ('sending', 'sent', 'failed', 'unknown')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_sent_names_its_upstream CHECK (state <> 'sent' OR upstream_id IS NOT NULL)
);

CREATE INDEX messages_by_recipient ON messages (recipient, created_at DESC);

-- One row per Idempotency-Key a caller presented, for as long as the key is
-- remembered. The primary key is what makes two racing requests one send.
CREATE TABLE idempotency_keys (
    api_key_id  uuid NOT NULL REFERENCES api_keys (id),
    key         text NOT NULL,
    -- A keyed digest of the request, under the caller's API key, which this
    -- database does not hold: the row cannot be used to guess a body.
    fingerprint bytea NOT NULL,
    message_id  uuid NOT NULL REFERENCES messages (id),
    state       text NOT NULL CHECK (state IN ('in_flight', 'done')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- When the latest try under this key began.
    tried_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, key)
);

CREATE INDEX idempotency_keys_by_message ON idempotency_keys (message_id);
