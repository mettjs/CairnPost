-- The console: whoever runs CairnPost signs in to it with an admin key and
-- is then known by a session, so the key itself is typed once and never
-- kept in the browser.
CREATE TABLE console_sessions (
    -- SHA-256 of the session's token. The token is in the browser's cookie
    -- and nowhere else: this row cannot be turned back into it.
    hash       bytea PRIMARY KEY,
    -- The key that signed in. A session is that key's and no more: it ends
    -- when the key is revoked.
    api_key_id uuid NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX console_sessions_by_expiry ON console_sessions (expires_at);

-- Keys can now be made from the console, by whoever is signed in. Which
-- key made a key is kept, by its public prefix, so that when a key leaks
-- the keys it made can be found. NULL for one made on the command line.
ALTER TABLE api_keys ADD COLUMN created_by text;

-- Keys are listed newest first, a sending domain's or all of them.
CREATE INDEX api_keys_by_age ON api_keys (created_at DESC);
