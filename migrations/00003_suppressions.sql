-- Addresses CairnPost will not send to, each for one sending domain.
--
-- An address is suppressed when a receiving server refuses it for good or
-- its owner marks an email as spam: sending there again only harms the
-- sending domain's standing. A suppression is lifted by hand.
CREATE TABLE suppressions (
    -- The sending domain the suppression holds for, lowercase.
    domain     text NOT NULL CHECK (domain = lower(domain) AND domain <> ''),
    -- The recipient's address, lowercase, so one mailbox is one row.
    address    text NOT NULL CHECK (address = lower(address) AND address <> ''),
    -- bounce: a receiving server refused it for good. complaint: marked as
    -- spam. manual: added by whoever runs CairnPost.
    reason     text NOT NULL CHECK (reason IN ('bounce', 'complaint', 'manual')),
    -- The email whose event caused it. A suppression outlives its email.
    message_id uuid REFERENCES messages (id) ON DELETE SET NULL,
    note       text,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- When it was lifted; NULL while it is in force. A lifted row is kept so
    -- that a late event about an email sent before the lift does not
    -- suppress the address again: only an email sent after it can.
    lifted_at  timestamptz,
    PRIMARY KEY (domain, address)
);

-- Deleting a message looks here for the suppressions that point at it.
CREATE INDEX suppressions_by_message ON suppressions (message_id) WHERE message_id IS NOT NULL;
