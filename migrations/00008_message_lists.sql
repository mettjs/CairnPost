-- Emails are now listed, newest first: all of them, those from one sending
-- domain, those to one address and those in one state. An id is made in
-- time order, so the newest email has the greatest id, and each list is
-- read from an index in the order it is shown.

-- The sending domain of each message, folded as the server folds a
-- domain everywhere else, and written by the server: so that the list of
-- a domain's emails and the rule for reading one of them cannot disagree
-- over what a letter's lower case is.
ALTER TABLE messages ADD COLUMN sender_domain text;
UPDATE messages SET sender_domain = lower(split_part(sender, '@', -1));
ALTER TABLE messages ALTER COLUMN sender_domain SET NOT NULL;

CREATE INDEX messages_by_sender_domain ON messages (sender_domain, id DESC);
-- A domain's emails in one state: without this, the rarer of the two
-- would be read whole to fill a page.
CREATE INDEX messages_by_sender_domain_state ON messages (sender_domain, state, id DESC);
CREATE INDEX messages_by_state ON messages (state, id DESC);

-- To one address, however its letters were cased when it was sent to.
-- Folded by the "C" rules, which change A to Z and nothing else, whatever
-- language the database sorts in.
CREATE INDEX messages_by_folded_recipient ON messages (lower(recipient COLLATE "C"), id DESC);
-- The index by address and time, which nothing read.
DROP INDEX messages_by_recipient;

-- The address whoever is signed in to the console is looking for, kept
-- with the session so that it ends when the session does and is never in
-- a page's address.
ALTER TABLE console_sessions ADD COLUMN looking_for text;
