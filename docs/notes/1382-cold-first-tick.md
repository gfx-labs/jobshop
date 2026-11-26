# Cold-cache List and first tick after start

2026-11-26, #1382 (part of #1379; the "Not done" item of
1372-list-row-scan.md). Measurement only: no behaviour changed.

## Method

Copy of this host's DB: `jobshop.db` 1200 MiB (1258938368 bytes) plus a 4 MiB
WAL, 5125 session dirs, load average 9-10 on 6 cores. Every figure is one
run on the copy, so expect noise.

Cold without root: `posix_fadvise(DONTNEED)` on the copy's `.db` and `-wal`
files (`dd if=<file> iflag=nocache count=0`, or `unix.Fadvise` in the test)
drops their clean pages from the kernel cache. Checked: `cat jobshop.db`
took 2.5 s after it and 0.06 s on the next read. Warm is the same file read
once with `cat`. A fresh reflink copy is also cold: btrfs gives the copy its
own inode and page cache (the note in 1360-scan-timing-harness.md that it
shares the live file's cache is wrong on this host). SQLite's own page cache
is cold in both cases because each run opens the store or starts the process.
The sessions directory's dentries and inodes cannot be dropped without root,
so the 4973-dir walk below is warm even in the cold runs.

- `Store.List`: `TestListColdDB` (`pkg/storage/store/sqlite/listcold_linux_test.go`,
  opt in with `JOBSHOP_LIST_DB`). Per run: drop the DB and WAL cache, open
  a store, list once (cold), list again on the same store (warm).
- First scan: `TestScanTimingHarness` (1360 note), fresh copy per run,
  cold and warm alternating, 3 each.
- First tick: the real binary (`jobshop serve --tick=1s`) on a root holding a
  fresh copy of the DB and the selfdev pack (14 pools, 13 triggers, 17
  workflows), agents set to a missing command and no GitHub token, so
  nothing leaves the host and pool spawns fail at once. 5 cold and 4 warm
  runs alternating, each on a fresh copy. The `first ticks` log record
  (#1325) gives the tick seconds and each step.

## Numbers

`Store.List` on one DB, cold then warm on the same store (ms):

| list | rows | cold | warm |
|---|---|---|---|
| closed sessions | 5117 | 289, 293, 339, 360 (first run after the copy: 909) | 43-58 (one 142 under load) |
| steps | 3105 | 492-566 | 49-61 |

Open of a cold store: 28-34 ms (first run on a fresh copy 166 ms, it replays
the WAL).

First scan after start (`scanSessions`, over 5125 dirs):

| | list_closed (`ClosedSessions`) | walk | total |
|---|---|---|---|
| cold | 303, 324, 348 ms | 23-76 ms | 334-410 ms |
| warm | 38-40 ms | 20-23 ms | 65-67 ms |

First tick (`tick=1`, seconds):

| | tick | biggest steps |
|---|---|---|
| cold | 0.275, 0.298, 0.359, 0.362, 0.578 | `run_triggers` 0.19-0.44, `sweep_attention_chats` 0.03-0.07, `supervise` 0.02-0.07 |
| warm | 0.244, 0.245, 0.298, 0.371 | `run_triggers` 0.13-0.18, `supervise` up to 0.17 |

On a bare config (no pack, so no triggers) tick 1 was 0.04-0.11 s cold (one
0.3 s) and 0.03-0.09 s warm.

## Reading

- Cold costs about 250-300 ms more on `List`/`ClosedSessions` and about
  450 ms more on steps than warm (45-55 ms). The rows are wide and read from
  disk, so reads dominate cold and the 16 MiB sort cache from #1372 does not
  change that.
- Tick 1 stays under 0.6 s cold, under the 2 s that would need a fix. The
  difference from warm is 0.03-0.3 s, inside the spread between runs. The
  tick reads few pages (open tickets by index); `run_triggers` is the largest
  step warm too (0.13-0.18 s): it runs the pack's trigger hooks, which fail
  here because the GitHub capability is off, so it may differ on the real
  host.
- scan_sessions runs off the tick (#1264), so its 0.33-0.41 s cold list does
  not delay tick 1. No `slow scan_sessions` was logged in these runs.
- One outlier: the very first run on the first copy of the bare-config
  runs, made seconds after the `List` test, logged `list_closed_seconds=2.59`
  in the scan (tick 1 was 0.205 s). It did not repeat in the later cold runs.
  I did not find the cause (disk contention is a guess).
  Treat 2.6 s as the upper end of a bad cold start, not the usual one.

## Not done

- Nothing is over 2 s, so no `slow_step` was named and no fix made.
- The sessions dir walk cold (1.38 s once in 1354-scan-sessions-walk.md) was
  not repeated: dentries cannot be dropped without root. It runs 8 probes at
  a time since #1354.
- Not measured with the real agents, GitHub token, identities or a live
  `sessions/` directory: the config above skips them on purpose.
