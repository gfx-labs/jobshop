# Quiet-tick close-out (#1293, JOB-191)

2026-11-19. The JOB-191 line (a quiet tick's `run_triggers` under 0.1s) is
**not met**. One cost is removed here; the rest is listed below.

## Measured (selfdev, 0.10.41, up 04:28 to 06:40 UTC, 289 ticks)

From `jobshop_tick_step_duration_seconds` and `jobshop_trigger_duration_seconds`
(`curl localhost:9464/metrics`). p50/p95 are interpolated inside the
histogram buckets, so they are rough. No `slow tick` WARN from this
controller in the window.

| step or trigger | mean | p50 | p95 |
|---|---|---|---|
| `run_triggers` (whole step) | 200ms | ~190ms | ~425ms |
| `scan` | 65ms | ~71ms | ~98ms |
| `acphost-scan` | 58ms | ~63ms | ~96ms |
| `replan` | 21ms | ~20ms | ~45ms |
| `acphost-pr-unmerged` | 20ms | ~19ms | ~42ms |
| `acphost-replan` | 17ms | ~18ms | ~25ms |
| `pr-unmerged` | 15ms | ~17ms | ~27ms |
| `curate` | 2.8ms | | |
| `master-red`, `release-pr` (event, 25 runs) | 4 to 8ms | | |

`jobshop system status` at 06:37: tick 0.246s, p95 0.382s over 60 ticks,
`run_triggers` 0.179s. The two scan triggers are a third of the step each;
their sum with the other four is the step's mean.

## Where the scan cost is

Copy of the live database (1.2GB), each scan signal run 10 times on a
stub host (throwaway test, not kept), per call:

| call | before | after |
|---|---|---|
| `signalSessions` (session_silent) | 36ms | 0.6ms |
| `workflowTickets` (every scan trigger, in `Detect`) | 13ms | 13ms |
| `signalSteps` (step_idle) | 4.2ms | 4.2ms |
| `signalNeedsHuman` | 4.2ms | 4.2ms |
| `signalReadyUnclaimed`, `signalBlocked` | under 0.4ms | |

`signalSessions` does one `List{Holder: <session>, Status: in_progress,
Limit: 1}` per open session (3 on selfdev), 12ms each. `tickets_holder` is a
partial index (`WHERE holder <> ''`); SQLite cannot show from a bound value
that `holder` is not empty, so it did not use the index and read the
priority index until the limit, which for a session that holds nothing is
every ticket.

## What changed

`sqlite` `List` adds the literal `t.holder <> ''` when `Filter.Holder` is
set. Every `Holder` list gets the index, not only this one (session close,
scale, wait, quarantine). Test: `TestTicketHolderPlan` checks the plan names
`tickets_holder` (fails with "SCAN t; USE TEMP B-TREE FOR ORDER BY" without
the change). Postgres is unchanged: it plans with the bound values.

## What is left

Expected on selfdev, not yet measured live: `scan` and `acphost-scan` lose
about 36ms each, so the step's mean goes from 200ms to about 130ms. Still
over 0.1s. The next cost is `workflowTickets`: 13ms in each of the four scan
triggers per tick (about 52ms), listing runs and decoding each one's fields.
It is a separate change (filter by `f_workflow`, or share the result in a
pass) and filed as #1299. Re-measure on the first window of 2h after
this change is deployed.

The 7.3s pattern is not traced here and not called fixed.
