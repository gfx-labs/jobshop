# scan_sessions timing harness

2026-11-25, #1360 (part of #1359).

Notes 1311, 1324 and 1354 each timed `scanSessions` with a throwaway test.
`TestScanTimingHarness` in
`pkg/orchestration/control/sessionscan_timing_internal_test.go` is that test,
committed once. It is opt-in: with no environment it reports SKIP, so CI
never runs it.

## Run

```
cp --reflink=auto ~/.local/state/jobshop/jobshop.db{,-wal} /tmp/scan/
JOBSHOP_SCAN_DB=/tmp/scan/jobshop.db \
JOBSHOP_SCAN_SESSIONS=~/.local/state/jobshop/sessions \
  go test ./pkg/orchestration/control -run 'TestScanTimingHarness$' -v -count=1
```

- `JOBSHOP_SCAN_DB`: a copy of `jobshop.db`. Never the live file. Copy the
  `-wal` next to it, or the newest writes are missing.
- `JOBSHOP_SCAN_SESSIONS`: the `sessions/` directory, the live one is fine.
  The harness only reads it (`ReadDir`, `supervisor.pid`, `supervisor.lock`
  `flock`, journal tails). The controller root is a temp dir holding a
  symlink to it, and the test fails if the directory's entries or mtime
  change.
- `JOBSHOP_SCAN_RUNS`: scans to run, default 4.
- `-v` shows the output (it goes through `t.Log`); `-count=1` stops the test
  cache from replaying an old result.

Output, one block per scan, from the real clock and `scanHook`:

```
scan 1: list_open=10ms list_closed=696ms walk=156ms backfill=41ms total=903ms orphans=0
  store: List 9.9ms, List 695ms
scan 2: list_open=1.5ms list_closed=- walk=2.5ms backfill=- total=4ms orphans=0
  store: List 1.5ms
```

`-` means the substep did not run in that scan: `list_closed` and `backfill`
run only until Ended is filled, as in the controller. The `store:` line is
each `List` and `Get` the scan made, in order, with its own duration, so a
slow substep can be pinned on one query (learning #1014). Scan 1 is the
controller's first scan after a start; the rest are steady state, with the
dead-directory cache filled.

## Pitfalls

- A run changes the copy. Opening it migrates it, and the first scan's
  backfill fills `Ended`, so a second run on the same copy has no backfill
  and no `list_closed`. Make a fresh copy per measurement.
- Make the copy with a reflink (`cp --reflink=auto`, btrfs or xfs). It is
  instant and shares extents with the live file, so the page cache of the
  live DB serves the copy too.
- Cold versus warm (learning #1011). A copy that was just written, or read
  by an earlier run, is in the page cache and under-reports the first scan
  after a real restart; a plain (non-reflink) copy of a 1 GB file can also
  push the sessions inodes out of the cache and over-report it. Compare
  scan 1 against scan 2 onwards, and repeat the run before believing one
  number. Dropping the page cache needs root, which agents do not have.
- Host load. Other sessions on the host share the cores and the disk: check
  `/proc/loadavg` against the core count and report it with the numbers
  (note 1354 ran at load 76-95 on 6 cores).
- The harness opens the store with the controller's SQLite driver and pragmas,
  but runs no pass, supervisor or other housekeeping, so it times the scan
  alone, not the scan competing with a tick.
- `TestScanTimingHarnessFixture` runs the same code on a small generated
  database and directory and checks that every substep is printed, so the
  harness stays working without a live copy.
