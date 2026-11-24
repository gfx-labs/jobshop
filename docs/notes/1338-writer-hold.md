# A collection Put held the writer 43s (#1338)

2026-11-24. A `slow tick` WARN at 02:45:24 UTC (43.1s, slowest step
`run_triggers`) showed `writer_holders` with `(*collection).Put=43.051`,
`db_written=false`, `wal_delta_bytes=0`, `psi_cpu_seconds=18.75`. The
journal of that minute is not readable from an agent session, so the cause
below is from the code and a copy of the live database, not from the tick.

## What a collection write did while it held the writer

Every record of a collection with `Search` fields has a document in
`fts_<name>`, an FTS5 table declared `(key UNINDEXED, body)`. `reindex` ran
`DELETE FROM fts_<name> WHERE key = ?` on every Put of a new key and on every
Update. FTS5 has no index on an UNINDEXED column, so that delete reads every
document of the table, inside the write transaction. It writes nothing when
no row matches, which fits `db_written=false` and a flat WAL, and it is CPU
work, which fits `psi_cpu`.

Measured on a read-only open of the live database (4039 journals, 115MB of
values): `SELECT count(*) FROM fts_journals WHERE key = 'nokey'` takes 0.29s
(`fts_learnings`, 1017 rows: 8ms). Every journals Put or Update, one per
session turn, held the writer that long, and the cost grows with the table.
On a synthetic 3000-journal database a Put took 8ms and almost all of it was
that delete.

Not shown: that 43s is this alone. `writer_holders` adds up calls by label,
so 43.051 may have been many holds, and the tick log did not say which
collection. Under host CPU pressure (18.75s of 43s) the 0.3s scans are
several times slower.

## Changes

- `reindex` and `Delete` find a document by rowid through
  `fts_<name>_keymap (key, id)`. The map is created and filled from the
  existing documents when a collection registers on a database without it
  (one scan, once). Same 3000-journal database: Put 0.7ms, Update 0.8ms.
- The writer log records the collection and key of a collection write. Its
  label is `step/(*collection).Put[journals]`, so holders of different
  collections no longer add up under one name.
- The tick log adds `slowest_call_key` and `longest_hold_call`,
  `longest_hold_key`, `longest_hold_seconds`, `longest_hold_active`: the
  single longest hold in the tick, not a sum.

If another long hold shows up with the scan gone, `longest_hold_*` names
the collection and key; with `psi_cpu` high and `db_written=false` it is
then host contention.
