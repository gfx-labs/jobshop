# Quiet-tick re-measure after #1300 and #1301 (#1305, JOB-191)

2026-11-19. The JOB-191 line (a quiet tick's `run_triggers` under 0.1s) is
still **not met**: mean 131ms. One follow-up is filed (#1314).

## Window

selfdev, jobshop 0.10.45 (fd2d231), controller up 12:01:41 UTC, read at
14:04 UTC (2h02m), 275 ticks, no restart in between. It includes 9dae8196
(#1300, `holder <> ''`) and 3295b61f (#1301, `workflowTickets` filtered by
workflow). Source: `curl localhost:9464/metrics`
(`jobshop_tick_step_duration_seconds`, `jobshop_trigger_duration_seconds`);
p50 and p95 are interpolated inside the histogram buckets, so they are
rough. No `slow tick` WARN from this controller in the window. The slowest
`run_triggers` pass was between 0.5s and 1s (3 of 275 above 0.5s); the
metrics keep no per-tick maximum. 75 of 275 passes were at or under 0.1s.

## Before and after

| step or trigger | 0.10.41 mean (289 ticks) | 0.10.45 mean | p50 | p95 |
|---|---|---|---|---|
| `run_triggers` (whole step) | 200ms | 131ms | ~149ms | ~246ms |
| `scan` | 65ms | 28ms | ~29ms | ~49ms |
| `acphost-scan` | 58ms | 23ms | ~21ms | ~47ms |
| `replan` | 21ms | 24ms | ~22ms | ~49ms |
| `acphost-replan` | 17ms | 22ms | ~19ms | ~47ms |
| `acphost-pr-unmerged` | 20ms | 16ms | ~17ms | ~25ms |
| `pr-unmerged` | 15ms | 8ms | ~8ms | ~22ms |
| `curate` | 2.8ms | 3.4ms | ~3ms | ~7ms |

The two scan triggers dropped by about 37ms and 35ms, as #1300 and #1301
predicted (expected mean ~130ms, got 131ms). The replan means did not move.

## What is left

Store calls of the quiet path on a copy of the live database (1.2GB, 12516
tickets), 15 runs each, median:

| call | rows | median |
|---|---|---|
| `replan` Eval, newest ticket by meta.trigger.name, limit 1 | 1 | 10.5ms |
| `replan` OpenCount (meta name, kind, open/in_progress) | 0 | 6.5ms |
| `acphost-replan` the same two | | 10.6ms, 5.7ms |
| `changedIn` closed since (only past the plain cooldown) | 50 | 17ms |
| `workflowTickets` runs of triage / needs-human label | 92 / 82 | 5.3ms / 2.1ms |
| `signalSteps`, `signalNeedsHuman` runs | 1 / 0 | 3.8ms, 4.0ms |
| `evalScan` runs of `scan` by meta name | 99 | 2.9ms |
| `pr-unmerged`, ready counts, sessions, labels | | under 0.3ms |

`replan` files a ticket every pass, so 2039 tickets carry
meta.trigger.name=replan (1596 for `acphost-replan`). The query plan is
`id IN (SELECT ticket_id FROM ticket_meta ...)`: read all those index rows,
probe `tickets` for each, sort. Two such queries per replan trigger are
about 34ms of the 131ms mean. `scan` is the largest trigger but spread: no
query over 5.3ms. `changedIn` closed-since is a table scan (no status plus
updated_at index).

Follow-up: #1314 (replan own-ticket lookups). The 7.3s pattern is still not
traced and not called fixed. The measurement test was throwaway and is not
kept.
