# SQLite store: quiet tick latency (JOB-191)

2026-11-10. A quiet supervisor tick took about 1s on selfdev (SQLite, 1.1 GB,
`records` 907 MB) and 0.13s on jobcity (Postgres). Four causes, one change each.

## What changed

- **Statistics.** `sqlite.Open` runs `PRAGMA optimize=0x10002` once, and the
  controller repeats it every 6h in the tick step `maintain_store`
  (`Store.Maintain`, a no-op on Postgres). Production had no `sqlite_stat1`, so
  `LastFire` for a trigger with no accepted invocation read the whole
  collection through `records_created`. A full analysis, not `analysis_limit`:
  with a sampled one the planner went back to `records_created`. After the
  first run it analyzes only what changed a lot.
- **Claim.** `Collection.Claim` finds its candidate with one query per state,
  not `state IN (a, b)`. With one state SQLite uses `records_state_created`
  and reads only that state's rows (0.05ms), where the `IN` form walked
  `records_created` through every terminal row (200ms), and `ANALYZE` did not
  change that. The best of the per-state candidates is claimed, so the order
  is still oldest first. Same shape on Postgres: its plan was already an
  index on `state`, and the split keeps it so with several states.
- **Reader pool.** `Get`, `List`, `Count` and `Search` of a collection read on
  the 4-connection reader pool (`docs/notes/store-read-pool.md`), so they no
  longer queue behind the writer's claim and kick. Reads inside a write
  transaction still use the transaction. A reader that gets SQLITE_SCHEMA
  while a collection registers its FTS5 table repeats, as `Recall` does.
- **`evaluate`.** `EditDue` checks age first with no query, then asks
  `Store.CountRuns` (one `COUNT(DISTINCT parent_id)` over steps) instead of
  listing every step and learning since the merge. `learnEvaluate` calls
  `MeasureEdit` only for an edit that is due. The decision is unchanged
  (`TestCountEditRunsMatchesMeasure` compares the SQL count with
  `EditMetrics.Runs` on the same data). The selfdev `evaluate` trigger gets
  `fallback: "10m"`: `pollDue` already honors `fallback` without `on:`.

## Numbers

A copy of the production database (2026-11-10, modernc v1.61.1, idle
connection, median of 7):

| Query | Before | After |
|---|---|---|
| Claim candidate, `hook_invocations`, none claimable | 197ms | 0.28ms |
| `LastFire` query, trigger with no accepted row | 301ms | 0.15ms |
| `evaluate` run count, 524 steps in 72h | 65ms (`Measure`) | 8ms (`CountRuns`) |

`PRAGMA optimize=0x10002` took 1.6 to 2.4s on that file the first time, at
`Open`. The controller starts once, so this is a startup cost, and the
6-hourly repeat costs microseconds when little changed.

Quiet-tick numbers from selfdev after deploy are read as in
`docs/verify/tick-latency.md` and reported on JOB-191.

## Measured on selfdev, 2026-11-18 (JOB-231)

Window: the selfdev controller on 0.10.36, pid 124786, started
2026-11-17 23:59:02 UTC, read at 2026-11-18 04:46 UTC (4h47m, 637 ticks).
Agents were running, so these are ordinary ticks, not an idle host.

How to get them (the journal holds several controllers; selfdev is the pid
whose `run started` lines say `run=js-`, and `/proc/<pid>/exe` names the
version):

```
journalctl --no-pager -t jobshop --since 2026-11-14 | grep 'slow tick'
curl -s localhost:9464/metrics | grep jobshop_tick_step_duration_seconds_
```

`/api/system` and `jobshop system status` were not readable from the agent
session (`forbidden: needs the admin socket or pool admin grant
"pool-list"`), so `tick_seconds`, `p95_seconds` and a p50 are not measured.
The metrics endpoint gives only `_sum` and `_count` per step: its histogram
buckets are the OpenTelemetry defaults (0, 5, 10, 25, ... 10000) on a metric
in seconds, so every sample is in the `le="5"` bucket and no percentile can be
read from them. The figures below are means, not p50.

| Step | Before JOB-191 (p50, 2026-11-10) | After, 0.10.36 (mean, 637 ticks) |
|---|---|---|
| `run_triggers` | 614ms | 195ms |
| `supervise` | 127ms | 25ms |
| `sweep_runs` | not recorded | 12ms |
| `sweep_attention_chats` | not recorded | 10ms |
| all tick steps summed | 0.7s to 1.9s (a quiet tick) | 249ms |

`evaluate` has no step metric of its own (it is a trigger inside
`run_triggers`), so its time is not shown separately. A p50 against a mean is
not a like-for-like comparison. No tick took 1s, so a long tail does not
explain the mean, but the p50 itself is unmeasured.

No `slow tick` (1s or more) in those 4h47m. Earlier selfdev controllers since
the probe landed (journal, `grep 'slow tick'`, selfdev pids only): 7 in 29h
(2026-11-14 to 15), 2 in 15h, 18 in 25h (14 of them 07:08 to 07:28 on
2026-11-16, `run_triggers`), 5 in 20h (2026-11-17), then 0 on 0.10.36. I do
not know which of those controllers carried which change, so the drop is not
attributed to any one of them.

What this does not show:

- The quiet tick is not under the 0.1s the verify doc expects: `run_triggers`
  alone averages 195ms. JOB-191 is not met on that line.
- The 7.3s pattern (every store call about 90ms or a multiple) has not
  recurred while the probe was running, so it is neither traced nor shown
  fixed. Four hours without a slow tick is not evidence about a pattern that
  appeared once in days.

## Not done

The 7.3s tick at 10:00:33 UTC (every store call at about 90ms or a multiple)
is not explained by these changes. Do not call it fixed until a tick with that
pattern is traced again.

2026-11-26 (#1374). Still open. Evidence: JOB-212, JOB-221, #1338 and the
quiet-tick notes (#1293, #1305, #1318, last read 2026-11-19); the 2026-11-10
journal is not readable from an agent session, so the tick itself was not
re-read. The known slow ticks do not have its pattern. `scan_sessions`
(JOB-212/221) was file I/O in one step, so other steps waited behind it and
reader-pool calls were not slowed. The 43s `(*collection).Put` hold (#1338)
was a writer hold, which does not touch reader-pool calls either. The 10:00:33
tick had a 4ms reader-pool list at about 90ms too, which fits a stalled
process (CFS throttling or CPU starvation) and neither of those. That is a
reading, not a finding: no trace of that tick exists. The `slow tick` fields
would settle it if it recurs: `stalls`, `stall_max_seconds`, `throttled_seconds`
and `psi_cpu_seconds` high with hint `process_stalled` means the process
stalled; `writer_queue` or `db_file_written` without it means the database.
It has not recurred in the windows read so far.
