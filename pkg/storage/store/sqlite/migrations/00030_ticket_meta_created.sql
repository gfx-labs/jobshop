-- +goose Up
-- ticket_meta carries the ticket's created_at, so a newest or oldest List
-- with a meta filter reads the index in order and stops at its limit. It
-- took a ticket row per match and a sort: 10ms for the newest ticket of a
-- trigger with 2000 of them, twice per tick (#1314). created_at never
-- changes, so the copy needs no upkeep beyond the insert.
ALTER TABLE ticket_meta ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;
UPDATE ticket_meta SET created_at = (SELECT t.created_at FROM tickets t WHERE t.id = ticket_meta.ticket_id);
CREATE INDEX ticket_meta_newest ON ticket_meta(pack, mkey, value, created_at, ticket_id);
DROP INDEX ticket_meta_lookup;

-- +goose Down
CREATE INDEX ticket_meta_lookup ON ticket_meta(pack, mkey, value);
DROP INDEX ticket_meta_newest;
ALTER TABLE ticket_meta DROP COLUMN created_at;
