-- What a paused endpoint is owed again when it is resumed is what was
-- given up since its failing began. Counting it, and resuming, finds it
-- here instead of reading everything the endpoint was ever sent.
CREATE INDEX webhook_deliveries_given_up ON webhook_deliveries (endpoint_id, attempted_at) WHERE state = 'failed';
