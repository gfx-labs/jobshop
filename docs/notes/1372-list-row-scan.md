# List: where the time goes past the SQL

2026-11-26, #1372 (part of #1371; follows 1366-labels-one-parameter.md).

## Problem

#1366 left `Store.List` of closed sessions at 163-186 ms against about 30 ms
for raw SQL over the same rows, and put the difference on the row scan and
`toTicket`. That split was wrong.

## Method

`BenchmarkListDB` (`pkg/storage/store/sqlite/listdb_bench_test.go`, opt in
with `JOBSHOP_LIST_DB=<copy of a DB>`) times `List` for closed sessions
(5098 rows) and steps (3089 rows). Reflink copy of this host's DB (1.2 GB),
warm, load average 18-22 on 6 cores, so figures are noisy. pprof from
`-cpuprofile`/`-memprofile`; raw-SQL comparisons ran the same statement
through modernc with every column scanned into `any`.

## What the profile showed

Closed sessions, CPU of `List`:

- about 80% inside SQLite: the select, then the `ORDER BY priority,
  created_at, id` sort. The rows are wide (body, fields, meta), and the
  sorter held at most the reader's 2 MiB default cache, so it wrote sorted
  runs to temp files (`vdbeSorterFlushPMA`, `pwrite`: 15% of `List`) and
  merged them.
- the Go side is about 20%: `scanTicket` 8%, `toTickets` 4%, slice growth
  4%, `withLabels` 6%. Allocation is 22-25 MB a call, almost all of it the
  row text itself (`columnText`, the `fields`/`meta` copies).

The raw statement is not 30 ms once it has the same `ORDER BY`: 37-42 ms
default, 22-26 ms without the sort. The 30 ms in #1366 did not sort or
scan the wide columns.

Raw statement, closed sessions, median of 25 (ms):

| variant | ms |
|---|---|
| ORDER BY, default cache | 41-42 |
| ORDER BY, cache_size 32 MiB | 28-30 |
| ORDER BY, temp_store=MEMORY | 32-33 |
| no ORDER BY | 22-26 |
| sort ids only, then join rows back | 28-31 |

## Change

Readers (`openReader`) set `cache_size` to 16 MiB (`readerCacheKiB`), so the
sorter of a few thousand wide rows stays in memory. No SQL, no scan code and
no `List` result changes. The cache fills only as pages are read: at most 4
reader connections (`readConns`) times 16 MiB. Postgres has no equivalent
here; nothing changed there.

`TestListWideRowsSortedPastCache` lists 400 tickets of 64 KiB bodies (past
the old cache) in priority, then creation order and checks every body, and
checks the reader's cache size.

## Before and after

`Store.List`, same copy, 5 runs each, binaries run back to back (ms):

- closed sessions (5098): before 56.6-58.5, after 46.4-50.6
- steps (3089): before 77.8-87.1, after 51.3-55.6

## Tried and dropped

- Scanning rows straight into `domain.Ticket` (no `[]sqldb.Ticket` and
  second slice): 2 MB less allocation of 25, no change in time beyond noise
  (closed sessions 54.7-61.7 against 56.6-58.5 without it). Not kept.
- Sort ids first, join rows back (`ORDER BY` in a subquery): 28-31 ms raw,
  the same as the cache change, but rewrites every `List` plan, which
  #1314 and #1293 depend on. Not worth it beside one pragma.
- 8 MiB cache: closed sessions 49 ms but steps still 85 ms (sort still
  spilled). 32 and 64 MiB: no better than 16 for closed sessions, steps
  47 and 45 ms against 51 for 16 MiB.

## Not done

- What is left is about 18 ms of Go: allocating and copying the row text
  into strings and `json.RawMessage`. Cutting it needs lazy `fields`/`meta`
  decoding in the `domain.Ticket` API, which is a larger change than this
  issue's scan.
- Cold-cache `List` was not measured.
