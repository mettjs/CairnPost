-- The address somebody signed in to the console is looking for was one a
-- session, so two tabs of one sign-in narrowed each other's lists. It is
-- now kept under a handle that the page's address carries: the address
-- itself is still never in a URL, and each tab has its own.

CREATE TABLE console_lookups (
    session_hash bytea NOT NULL REFERENCES console_sessions (hash) ON DELETE CASCADE,
    -- Random, and a name for the address within this session only.
    handle       text NOT NULL,
    address      text NOT NULL,
    -- When it was last looked for: the oldest go when a session has many.
    used_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_hash, handle),
    UNIQUE (session_hash, address)
);

ALTER TABLE console_sessions DROP COLUMN looking_for;
