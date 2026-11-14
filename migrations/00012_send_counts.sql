-- How many emails were sent, hour by hour, and what became of them: kept
-- apart from the emails' own records, so the numbers outlive them. A
-- record is pruned after the time the operator keeps it; its count stays.

LOCK TABLE messages IN SHARE ROW EXCLUSIVE MODE;

-- One row per hour (UTC) an email was sent in and domain it was sent from,
-- with how many of those emails stand in each state now. An email is
-- counted once, under the hour it was sent: when its state moves, it moves
-- from one column to another of the same row. The columns are the states
-- a message can be in, name for name.
CREATE TABLE send_counts (
    hour       timestamptz NOT NULL,
    domain     text NOT NULL,
    sending    integer NOT NULL DEFAULT 0,
    sent       integer NOT NULL DEFAULT 0,
    deferred   integer NOT NULL DEFAULT 0,
    delivered  integer NOT NULL DEFAULT 0,
    bounced    integer NOT NULL DEFAULT 0,
    complained integer NOT NULL DEFAULT 0,
    failed     integer NOT NULL DEFAULT 0,
    unknown    integer NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, domain)
);

-- The last 62 days are counted from the records on hand; nothing older is
-- shown anywhere, and an older count would be pruned at once.
INSERT INTO send_counts (hour, domain, sending, sent, deferred, delivered, bounced, complained, failed, unknown)
SELECT date_trunc('hour', created_at, 'UTC'), sender_domain,
       count(*) FILTER (WHERE state = 'sending'),
       count(*) FILTER (WHERE state = 'sent'),
       count(*) FILTER (WHERE state = 'deferred'),
       count(*) FILTER (WHERE state = 'delivered'),
       count(*) FILTER (WHERE state = 'bounced'),
       count(*) FILTER (WHERE state = 'complained'),
       count(*) FILTER (WHERE state = 'failed'),
       count(*) FILTER (WHERE state = 'unknown')
FROM messages WHERE created_at > now() - interval '62 days' GROUP BY 1, 2;

-- The counting is the database's: whatever writes a message's state, the
-- count follows in the same transaction, and no writer can forget it. A
-- deleted message is not taken out of its count.
--
-- Each statement's messages are counted together, one hour and domain
-- after the other in a fixed order, so two transactions that each touch
-- several hours never wait on each other crosswise.

CREATE FUNCTION count_new_messages() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO send_counts (hour, domain, sending, sent, deferred, delivered, bounced, complained, failed, unknown)
    SELECT date_trunc('hour', created_at, 'UTC'), sender_domain,
           count(*) FILTER (WHERE state = 'sending'),
           count(*) FILTER (WHERE state = 'sent'),
           count(*) FILTER (WHERE state = 'deferred'),
           count(*) FILTER (WHERE state = 'delivered'),
           count(*) FILTER (WHERE state = 'bounced'),
           count(*) FILTER (WHERE state = 'complained'),
           count(*) FILTER (WHERE state = 'failed'),
           count(*) FILTER (WHERE state = 'unknown')
    FROM made GROUP BY 1, 2 ORDER BY 1, 2
    ON CONFLICT (hour, domain) DO UPDATE SET
        sending = send_counts.sending + EXCLUDED.sending,
        sent = send_counts.sent + EXCLUDED.sent,
        deferred = send_counts.deferred + EXCLUDED.deferred,
        delivered = send_counts.delivered + EXCLUDED.delivered,
        bounced = send_counts.bounced + EXCLUDED.bounced,
        complained = send_counts.complained + EXCLUDED.complained,
        failed = send_counts.failed + EXCLUDED.failed,
        unknown = send_counts.unknown + EXCLUDED.unknown;
    RETURN NULL;
END $$;

-- A message whose state moved leaves one column of its hour's row for
-- another, in one write. Its hour's row may be gone, pruned on the
-- counts' own time: then the move is not counted, and no row is made for
-- it. Only a message whose hour or domain itself changed has a row made.
CREATE FUNCTION count_moved_messages() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    d record;
BEGIN
    FOR d IN
        SELECT hour, domain, bool_or(moved) AS moved,
               sum(sending)::int AS sending,
               sum(sent)::int AS sent,
               sum(deferred)::int AS deferred,
               sum(delivered)::int AS delivered,
               sum(bounced)::int AS bounced,
               sum(complained)::int AS complained,
               sum(failed)::int AS failed,
               sum(unknown)::int AS unknown
        FROM (
            SELECT date_trunc('hour', o.created_at, 'UTC') AS hour, o.sender_domain AS domain, false AS moved,
                   -(o.state = 'sending')::int AS sending,
                   -(o.state = 'sent')::int AS sent,
                   -(o.state = 'deferred')::int AS deferred,
                   -(o.state = 'delivered')::int AS delivered,
                   -(o.state = 'bounced')::int AS bounced,
                   -(o.state = 'complained')::int AS complained,
                   -(o.state = 'failed')::int AS failed,
                   -(o.state = 'unknown')::int AS unknown
            FROM was o JOIN now_is n USING (id)
            WHERE (o.state, o.created_at, o.sender_domain) IS DISTINCT FROM (n.state, n.created_at, n.sender_domain)
            UNION ALL
            SELECT date_trunc('hour', n.created_at, 'UTC'), n.sender_domain,
                   (o.created_at, o.sender_domain) IS DISTINCT FROM (n.created_at, n.sender_domain),
                   (n.state = 'sending')::int,
                   (n.state = 'sent')::int,
                   (n.state = 'deferred')::int,
                   (n.state = 'delivered')::int,
                   (n.state = 'bounced')::int,
                   (n.state = 'complained')::int,
                   (n.state = 'failed')::int,
                   (n.state = 'unknown')::int
            FROM was o JOIN now_is n USING (id)
            WHERE (o.state, o.created_at, o.sender_domain) IS DISTINCT FROM (n.state, n.created_at, n.sender_domain)
        ) change GROUP BY 1, 2 ORDER BY 1, 2
    LOOP
        UPDATE send_counts SET
            sending = greatest(sending + d.sending, 0),
            sent = greatest(sent + d.sent, 0),
            deferred = greatest(deferred + d.deferred, 0),
            delivered = greatest(delivered + d.delivered, 0),
            bounced = greatest(bounced + d.bounced, 0),
            complained = greatest(complained + d.complained, 0),
            failed = greatest(failed + d.failed, 0),
            unknown = greatest(unknown + d.unknown, 0)
        WHERE hour = d.hour AND domain = d.domain;
        IF NOT FOUND AND d.moved THEN
            INSERT INTO send_counts (hour, domain, sending, sent, deferred, delivered, bounced, complained, failed, unknown)
            VALUES (d.hour, d.domain, greatest(d.sending, 0), greatest(d.sent, 0), greatest(d.deferred, 0), greatest(d.delivered, 0), greatest(d.bounced, 0), greatest(d.complained, 0), greatest(d.failed, 0), greatest(d.unknown, 0))
            ON CONFLICT (hour, domain) DO UPDATE SET
                sending = greatest(send_counts.sending + EXCLUDED.sending, 0),
                sent = greatest(send_counts.sent + EXCLUDED.sent, 0),
                deferred = greatest(send_counts.deferred + EXCLUDED.deferred, 0),
                delivered = greatest(send_counts.delivered + EXCLUDED.delivered, 0),
                bounced = greatest(send_counts.bounced + EXCLUDED.bounced, 0),
                complained = greatest(send_counts.complained + EXCLUDED.complained, 0),
                failed = greatest(send_counts.failed + EXCLUDED.failed, 0),
                unknown = greatest(send_counts.unknown + EXCLUDED.unknown, 0);
        END IF;
    END LOOP;
    RETURN NULL;
END $$;

CREATE TRIGGER messages_counted_new AFTER INSERT ON messages
    REFERENCING NEW TABLE AS made FOR EACH STATEMENT EXECUTE FUNCTION count_new_messages();
CREATE TRIGGER messages_counted_moved AFTER UPDATE ON messages
    REFERENCING OLD TABLE AS was NEW TABLE AS now_is FOR EACH STATEMENT EXECUTE FUNCTION count_moved_messages();
