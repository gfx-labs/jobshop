# List reads labels with one parameter

2026-11-26, #1366 (found in #1361, note 1361).

## Problem

`Store.List` ends in `withLabels`, which called `LabelsFor` with one bound
parameter per returned ticket (`ticket_id IN ($1,...,$N)`). On a reflink
copy of this host's DB (1.2 GB, warm, load average ~25 on 6 cores) that
cost 150-185 ms for 5000 ids, none with labels, more than the ticket rows.
It also made one statement text per list length, which fills the SQLite
statement cache (`stmtCacheMax`), and on Postgres a list over 65535 ids
fails on the parameter limit.

## Options measured

`labels` read for N ids, median of 7 runs, raw queries through modernc
(ms; "labelled" ids all have labels, 5000 rows back):

| N | IN list | json_each | chunks of 500 | chunks of 100 | one query per id |
|---|---|---|---|---|---|
| 100 | 0.3 | 0.1-0.2 | 0.3 | 0.3 | 2.0 |
| 1000 | 8 | 1.0-1.4 | 4.6-5.2 | 2.9-3.1 | 19-24 |
| 5000 | 148-152 | 3.2-7.1 | 22-26 | 13-18 | 104-106 |

The IN list grows worse than linearly. A join on the List filter would be
fastest but only works for the caller's own filter, not for `withLabels`.

## Change

- SQLite: `WHERE ticket_id IN (SELECT value FROM json_each($1))`, the ids
  as one JSON array (`sqldb/graph.go`).
- Postgres: `WHERE ticket_id = ANY($1::text[])`, the ids as one array
  (`pgdb/graph.go`).
- The statement text no longer depends on the list length. An empty list
  returns no rows without a query. `ORDER BY ticket_id, label` is kept, so
  labels come back sorted as before.

`TestConformance/ListLabelsMany` lists 700 tickets, two thirds labelled,
and checks each ticket's labels. It ran on SQLite and on a local
Postgres 17 (`TestEveryQueryPrepares` too).

## Before and after

`Store.List` on the same copy, 5 runs each after one warm-up (ms, load
~25):

- closed sessions (5084 tickets): before 298-350, after 163-186
- steps (3074 tickets): before 268-324, after 245-277 (about the same
  within noise: rows with large fields dominate)

## Not done

- About 160 ms of the closed-session List is still the select, the 28
  column scan and `toTicket`; raw SQL for the same rows is about 30 ms.
  Not looked into here.
  2026-11-26: looked into in #1372 (#1377), see `1372-list-row-scan.md`:
  the split was wrong, about 80% was inside SQLite sorting wide rows past
  the 2 MiB reader cache, and readers now get a 16 MiB cache.
