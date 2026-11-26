# scan_sessions: first-scan closed read is ids only

2026-11-26, #1361 (part of #1359).

## Problem

After #1354 the first `scan_sessions` after a start spent most of its time
in `list_closed` (one `List` of ~5000 closed session tickets) and the Ended
backfill (decoding each ticket's fields to see whether `Ended` was set). The
scan needs only which session ids are closed and whether `fields.ended` is
set.

## Where the List time went

Timed on a reflink copy of this host's DB (1.2 GB, 5084 closed sessions)
with throwaway queries through modernc, warm page cache:

- raw select of all ticket columns for the filter: 31-36 ms
- `LabelsFor` (`withLabels`, one `IN ($1..$5084)`): 183-207 ms, 0 rows
  (session tickets have no labels)
- `Store.List` as a whole: 270-295 ms

So the List was mostly the labels query, not the ticket rows. Backfill was
40-53 ms of fields decoding. A cold first read of the table pages added
about 250 ms more (a fresh reflink copy has its own page cache).

## Change

New store method `ClosedSessions(ctx) ([]ClosedSession, error)`: id and
`ended` flag of every closed session, ordered by id. One query per engine
(`sqlite/closedsessions.go`, `postgres/closedsessions.go`), no labels, no
whole tickets. SQLite tests the flag with `json_extract(fields,'$.ended') IS
NOT NULL`, Postgres with `CAST(fields AS jsonb)->>'ended' IS NOT NULL`; a
JSON `null` counts as not set, as `Ended != nil` did.

`closedSessions.load` (`lifecycle.go`) uses it, and `backfillEnded` skips
entries with the flag set instead of decoding each ticket. Which directories
are orphans, cached dead or backfilled does not change. The per-directory
`Get` for a few new directories is unchanged.

Behavior difference: a closed session whose fields do not decode used to be
skipped by the backfill silently. Fields are written by the controller as
valid JSON, so this only matters for hand-edited rows: SQLite's `json_extract`
would fail the whole read (the scan logs it and falls back to per-directory
`Get`, as it did when the List failed), and a decodable row is tried by
`saveEnded`, which logs on failure.

## Method

`TestScanTimingHarness` (note 1360), a fresh reflink copy per run, real
`sessions/` dir (5091 dirs), scan 1 only. Host load average 14-27 on 6
cores. "Warm" runs `cat` the copy first.

Warm, ms (list_closed + backfill = total):

- before: 269 + 40 = 333, 296 + 39 = 360, 282 + 41 = 348
- after: 28.5 + 0 = 57, 28.3 + 0 = 53, 27.3 + 0 = 52

Cold copy (not read before), ms:

- before: 515 + 41 = 584, 608 + 53 = 690, 522 + 48 = 596
- after: 279 + 0 = 309, 273 + 0 = 302

Cold is disk reads of the table pages for `fields` (json_extract touches
every row), which the sessions' scattered rows make slow; it is not
removed. The live DB is probably warmer than a fresh copy after a restart.

## Not done

- Not changed: `withLabels` makes every large `List` pay the IN-list cost
  (#1366). Fixing it would help other callers; this
  change does not depend on it.
- No index. A partial covering index on closed sessions would cut the cold
  read but costs a migration and write overhead on every ticket change for
  about 30 ms warm that is left. The walk (20 ms) is the next cost.
