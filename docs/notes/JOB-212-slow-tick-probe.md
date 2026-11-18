# Slow tick probe (JOB-212)

2026-11-14. `JOB-191-store-latency.md` ends with one tick that took 7.3s
(10:00:33 UTC on selfdev) while every store call, even a 4ms reader-pool list,
took about 90ms or a multiple of it. This note records what is known, what
the controller now captures, and why the cause is not named yet.

## What is known

No trace of that tick exists: the spans of a tick say which step and which
store call were slow, not why. Two readings fit "every call 90ms or a
multiple":

- **The database stalled.** A WAL checkpoint (an fsync of a 1.1 GB database),
  a writer queued behind a long write, or a housekeeping step holding the
  writer (`PRAGMA optimize` took 1.6 to 2.4s on this file). This does not
  fit the reader pool calls: they use their own connections, take no write
  lock and do not wait on a checkpoint.
- **The process stalled.** It was not scheduled, so everything it did took
  longer, whatever the call was. Causes: CPU starvation, cgroup CFS
  throttling (its period is 100ms, so stalls come in multiples of it), a GC
  stop-the-world, memory reclaim.

The reader pool calls point at the second. The host agrees: at the time of
writing, `/proc/loadavg` is 17.6 on 6 cores and `/proc/pressure/cpu` reads
`some avg60=12`, with agent sessions and builds sharing the CPUs. That is
a lead, not a finding. Nothing was changed in the scheduling settings, because
no measurement ties a stall to them.

## What changed

A tick that takes `Options.SlowTick` (default 1s; a quiet tick is under
0.1s) logs one warning, `slow tick`, and puts the same fields on the
`supervisor tick` span (`jobshop.tick.*`). The fields are the evidence for
each candidate, taken as the difference of two snapshots around the tick:

| Field | Candidate it tests |
|---|---|
| `stalls`, `stall_max_seconds`, `stall_total_seconds` | Process not running. A monitor goroutine expects to run every 20ms and records each run that is 40ms or more late. |
| `psi_cpu_seconds`, `psi_io_seconds`, `psi_memory_seconds` | Host pressure: time some task waited (`/proc/pressure`, host-wide). |
| `throttled_seconds` | The controller's cgroup was CPU-throttled (`cpu.stat` `throttled_usec`). |
| `gc_cycles`, `gc_pause_seconds` | Go GC stop-the-world time. |
| `db_written`, `wal_bytes`, `wal_delta_bytes` | The database file was written during the tick: a checkpoint copies WAL pages into it. |
| `pool_writer_waits`, `pool_writer_wait_seconds` (and `pool_reader_*`) | Calls queued for a connection, so a busy writer. |
| `housekeeping` in `hints` | A housekeeping step (outside `tickMu`) ran during the tick: `housekeeping:<step>`. |
| `slowest_step`, `slowest_step_seconds` | Which tick step. |

`hints` lists the candidates the numbers support: `process_stalled`,
`cpu_pressure`, `cgroup_throttled`, `io_pressure`, `memory_pressure`, `gc`,
`db_file_written`, `writer_queue`, `housekeeping:<step>`, or `none`. A hint is
a threshold on the numbers (see `hints` in `tickprobe.go`), not a verdict:
read the fields.

How to read it is in `docs/verify/slow-tick.md`. A pattern like the 7.3s
tick reads as `process_stalled` with `cpu_pressure` or `cgroup_throttled`
when the process was starved, and as `writer_queue` or `db_file_written`
without `process_stalled` when the database was.

## Not done

- [x] Name the slow step (JOB-221, 2026-11-15): `scan_sessions`, below.
- [x] Take it off the tick path (JOB-221): it is a housekeeping step now.
- [x] Name the substep (2026-11-18, JOB-231): `walk`, the per-directory pass
  over the session directory. See "Substep" below.

The probe does not change any setting.

## Trace

Four ticks on selfdev, 2026-11-15 (journal, `grep 'slow tick'`):

| time (UTC) | seconds | slowest_step | step seconds | hints | writer waits |
| -- | -- | -- | -- | -- | -- |
| 08:54:23 | 5.8 | scan_sessions | 3.8 | cpu_pressure, memory_pressure | 0 |
| 08:55:19 | 31.5 | scan_sessions | 31.1 | cpu_pressure | 0 |
| 08:55:21 | 1.9 | scan_sessions | 0.9 | cpu_pressure, writer_queue | 19 (3.8s) |
| 08:56:01 | 13.9 | scan_sessions | 11.2 | cpu_pressure | 5 (0.7s) |

All four: `stalls=0`, `throttled_seconds=0`, `db_written=false`,
`wal_delta_bytes=0`, no reader waits, GC pause under 12ms. The process and
the database were not stalled; the time is inside `scan_sessions`, with
host CPU pressure (psi_cpu 3 to 14s) in every record.

cause: `scan_sessions` ran under `tickMu` and does only file I/O and store
reads, not worktree removal (`removeWorktree` is on the session-end path, not
here). Per pass it (1) lists open sessions, (2) `ReadDir`s the session
directory (about 3200 entries on selfdev) and, for each, reads
`supervisor.pid`, opens `supervisor.lock` and `flock`s it, and (3) until every
closed session has `Ended`, lists all closed sessions and reads the exit file
and journal tail of up to 100 of them; a compressed journal with no seek
table or index is decompressed whole to find its last seq. Thousands of
syscalls and file reads are cheap on an idle host and take seconds when the
process is starved of CPU, and every other tick step waited behind them. The
records do not say which of (2) and (3) took the 31s: that is not proven.

fix: `scan_sessions` moved out of the tick into the housekeeping loop (own
goroutine, no `tickMu`, once a minute and at start). Its consumers read the
last result (`scannedOrphans`), and before the first scan walk the directory
themselves, so the only change is that an orphan host appears in `session.list`
within a minute instead of one tick. Each scan records its substep times
(`list_open`, `walk` with directory count and slowest directory,
`list_closed`, `backfill` with sessions filled and slowest session) as
`jobshop.scan.*` attributes on its `housekeeping scan_sessions` span, and a
scan of `Options.SlowTick` or more logs one `slow scan_sessions` warning with
the same fields. A tick that overlaps a scan carries
`housekeeping:scan_sessions` in its `hints`.
Tests: `TestScanSessionsOffTick`, `TestScanSessionsSlowSubstep`.

## Substep

2026-11-18 (JOB-231). Nine `slow scan_sessions` records on selfdev, 2026-11-16
17:28 to 2026-11-17 19:43 UTC (`journalctl --no-pager -t jobshop | grep 'slow
scan_sessions'`, selfdev pids only; the journal also holds another
controller). None since the controller restarted on 0.10.36 at 23:59 UTC on
2026-11-17.

| seconds | walk_seconds | dirs | slowest_dir_seconds |
| -- | -- | -- | -- |
| 1.08 | 1.08 | 3440 | 0.02 |
| 1.33 | 1.32 | 3494 | 0.10 |
| 3.82 | 3.82 | 3495 | 0.03 |
| 2.02 | 2.02 | 3499 | 0.10 |
| 2.39 | 2.39 | 3504 | 0.03 |
| 5.26 | 5.26 | 3526 | 0.05 |
| 2.55 | 2.55 | 3547 | 0.03 |
| 1.14 | 1.14 | 3559 | 0.02 |
| 1.19 | 1.18 | 3640 | 0.06 |

In all nine, `walk` is the whole scan (`list_open` 1 to 2ms, `list_closed` 0,
`backfill` 0, `backfilled=0`, `orphans=0`). The slowest single directory is
at most 0.10s, so no one directory (a stuck lock, a large journal) explains
it: the time is spread over about 3500 directories at 0.3 to 1.5ms each. That
is (2) in the cause above, not (3). The walk time varies 5x at nearly the same
directory count, which fits a loaded host, but these records carry no
pressure fields, so that is a reading, not a finding. The scan runs outside
`tickMu`, so it no longer delays a tick.
