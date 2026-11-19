# Quiet-tick re-measure after #1316 (#1318, JOB-191)

2026-11-19. The JOB-191 line (a quiet tick's `run_triggers` under 0.1s) is
**met**: mean 70.5ms. No follow-up issue is needed. The 7.3s pattern stays
not traced and not claimed fixed.

## Window

selfdev, jobshop 0.10.46 (b9f0e40, includes b92574c of #1314), controller
started 14:41:03 UTC, read at 16:50 UTC (2h09m), 283 ticks, no restart
in between. The restart was the 0.10.46 self-deploy after release PR #1317;
the earlier 14:29 start was 0.10.45+b92574c. Source:
`curl localhost:9464/metrics` (`jobshop_tick_step_duration_seconds`,
`jobshop_trigger_duration_seconds`). p50 and p95 are interpolated inside
histogram buckets, so they are rough. The metrics keep no per-tick maximum:
the slowest `run_triggers` pass was between 0.5s and 1s (1 of 283; 2 more
between 0.25s and 0.5s). 262 of 283 passes (93%) were at or under 0.1s.

Slow-tick WARNs from this controller in the window: two, both at startup
(14:44:43, 8.1s, and 14:45:09, 4.0s). The slowest step in both was
`supervise`, not `run_triggers`, with a `cpu_pressure` hint.

## Before and after

| step or trigger | 0.10.41 mean (289 ticks) | 0.10.45 mean (#1305, 275 ticks) | 0.10.46 mean (283 ticks) | p50 | p95 |
|---|---|---|---|---|---|
| `run_triggers` (whole step) | 200ms | 131ms | **70.5ms** | ~72ms | ~157ms |
| `scan` | 65ms | 28ms | 21.3ms | | |
| `acphost-scan` | 58ms | 23ms | 22.2ms | | |
| `replan` | 21ms | 24ms | 1.4ms | | |
| `acphost-replan` | 17ms | 22ms | 2.5ms | | |
| `acphost-pr-unmerged` | 20ms | 16ms | 12.6ms | | |
| `pr-unmerged` | 15ms | 8ms | 6.8ms | | |
| `curate` | 2.8ms | 3.4ms | 1.9ms | | |

Per-trigger p50 and p95 were not recomputed. Triggers that do not run every
tick (25 or 13 runs in the window), mean per run: `acphost-release-pr` 9.1ms,
`evaluate` 7.4ms, `master-red` 4.0ms, `release-pr` 3.6ms,
`acphost-master-red` 3.5ms.

The two `replan` triggers fell from 24ms and 22ms to 1.4ms and 2.5ms, a
saving of about 42ms in total, as #1314 predicted on the DB copy (34ms for the
two lookups; the rest was the open-work check and noise). The whole step
fell 60ms.

## What is left

`scan` and `acphost-scan` (about 21ms and 22ms) are the largest; #1305 found
no query in them over 5.3ms, so the cost is spread. `run_triggers` is now
about 70ms of the 0.17s tick on the live host; `supervise` (65ms mean, 18.4s
over the window) is the larger step and is outside JOB-191's line. No
follow-up is filed: nothing left in `run_triggers` is a single query worth
profiling on its own.
