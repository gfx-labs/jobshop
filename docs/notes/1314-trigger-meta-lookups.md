# Trigger own-ticket lookups without the full history (#1314, JOB-191)

2026-11-19. #1305 found the two `replan` triggers spent about 34ms of the
131ms quiet tick on two `List` calls by `meta.trigger.name`. `replan` files
a ticket every pass, so 2039 tickets carry its name (1596 for
`acphost-replan`).

## What was slow

Both used `t.id IN (SELECT ticket_id FROM ticket_meta ...)`: read every
matching row, probe `tickets` for each, then sort.

| call | rows | before | after |
|---|---|---|---|
| newest ticket of a trigger (`Eval`), limit 1 | 1 | 10.5ms | 0.17ms |
| open tickets of a trigger (`OpenCount`) | 0 | 6.5ms | 0.15ms |
| newest 16 (`cooldownFor`) | 16 | 11.6ms | 0.35ms |

Copy of the live database (1.2GB, 12.5k tickets), modernc, 15 runs each,
median. The `acphost-replan` numbers match.

## What changed (SQLite only)

- `ticket_meta.created_at` (migration 00030) holds the ticket's created_at,
  set by the insert in `AddTicketMeta` and filled for old rows. It never
  changes. Index `ticket_meta_newest (pack, mkey, value, created_at,
  ticket_id)` replaces `ticket_meta_lookup`; it has the same prefix.
- A newest or oldest `List` with a meta filter starts from that index
  (`ticket_meta m0 CROSS JOIN tickets t`), reads it in order and stops at
  the limit. The first meta filter drives it; others stay `IN` lists.
- A meta filter on open work (kinds named, statuses only open and
  in_progress) is a correlated `EXISTS` and starts from `tickets_kind`.
  Open tickets are few; the plan depends on `tickets_kind` having
  statistics that make it selective, as on the live database (690 rows per
  kind and status of 8278).
- `storemigrate` copies `ticket_meta` by its four portable columns; Postgres
  has no `created_at` there and is unchanged.

Tests: `TestTicketMetaOrderPlan` (plans), `TestTicketMetaOrder` (results
against filters, limit, cursor, close and meta update). The 2h re-measure
of `run_triggers` after deploy is left to #1305's follow-up.
