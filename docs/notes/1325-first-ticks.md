# First ticks after a restart (#1325)

2026-11-23. The first ticks of a controller start were suspected slow:
#1318 recorded two `slow tick` WARNs at startup, slowest step `supervise`,
hint `cpu_pressure`.

## What the journal shows

`journalctl --user -u jobshop` holds 17 controller starts (2026-11-19 04:17
to 2026-11-20 21:54). A tick is logged only when it takes 1s or more, so a
quick first tick left no numbers. Slow ticks within ten minutes of a start:

| start | slow tick | seconds | slowest step | hints |
|---|---|---|---|---|
| 11:50:14 | 11:57:50 | 4.2 (supervise 3.4) | `supervise` | cpu_pressure |
| 14:41:03 | 14:44:43 | 8.1 (supervise 7.8) | `supervise` | cpu_pressure |
| 14:41:03 | 14:45:09 | 4.0 (supervise 3.1) | `supervise` | cpu_pressure, writer_queue |

The other 14 starts had none. All three came minutes after the start (the
tick interval is 30s), not on the first two ticks. None had a stall, a GC
pause, a database write or a slow store call: the process ran, and the host
was at load 17 to 43 on 6 cores (`psi_cpu_seconds` 1.5 to 3.7 inside the
tick). Each start also respawns every agent host and resumes its
conversation, which loads the host in the same minutes. That part is not
the tick and not ours to cut.

## What the tick itself did

A controller on a copy of the live database (1.2GB, 14.5k tickets, no agents,
no GitHub identity), tick 5s, four runs each. Timing inside `supervise`
found one query: `inProgress`, the read of every in-progress ticket. It
planned `SCAN t` plus a sort, because `status IN (?)` with a bound value does
not let SQLite use `tickets_lease`, the partial index on in-progress
tickets. A scan of the whole table is 16ms with warm pages, 0.9 to 1.3s when
the pages were cold (a fresh copy of the file), and it ran every tick. This is
the same trap as #1293.

`List` with a single `in_progress` status now adds the literal
`t.status = 'in_progress'`; the plan reads `tickets_lease` (0.1ms).
`TestTicketInProgressPlan` checks it.

## Before and after

Median seconds of four runs; the database was warm, so the cold cost above
is not in these numbers.

| step | tick 1 before | tick 1 after | tick 2 before | tick 2 after |
|---|---|---|---|---|
| `supervise` | 0.047 | 0.024 | 0.030 | 0.013 |
| `run_triggers` | 0.135 | 0.120 | 0.080 | 0.080 |
| whole tick | 0.285 | 0.240 | 0.210 | 0.200 |

## What is left

- `run_triggers` on tick 1 is about 0.05s over tick 2: every trigger is due
  once after a start. Not changed.
- On the live host a first tick may still take longer than here: the host
  load above was not reproduced. The 8s ticks are not explained by the
  scan alone (16ms warm), so this change may not remove them.
- The controller now logs `first ticks` (INFO) for ticks 1 and 2 with every
  step's seconds (`step_<name>`). The next restart then leaves the numbers
  without a slow tick. Compare it with `slow tick` records and
  `jobshop system status`.
