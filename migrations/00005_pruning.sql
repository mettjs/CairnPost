-- Pruning: what has been kept long enough is deleted on a schedule. An
-- Idempotency-Key goes when it would no longer be honoured, an email's
-- record (with its events and the webhooks owed for them) when nothing has
-- happened to it for as long as the operator keeps records, and a lifted
-- suppression once no email sent before the lift is still on record. A suppression in force is never
-- pruned: it outlives the email that caused it.

-- Each prune reads the oldest rows first. A message's age is counted from
-- the last thing that happened to it; whether one sent before a lift is
-- still kept is asked of when it was made.
CREATE INDEX idempotency_keys_by_age ON idempotency_keys (created_at);
CREATE INDEX messages_by_activity ON messages (updated_at);
CREATE INDEX messages_by_age ON messages (created_at);
CREATE INDEX suppressions_by_lift ON suppressions (lifted_at) WHERE lifted_at IS NOT NULL;
