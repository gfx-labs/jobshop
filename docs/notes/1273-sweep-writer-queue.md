# Slow ticks: sweep queued on the SQLite writer (#1273)

2026-11-17.

## Finding

A slow tick with `slowest_step=sweep` (24.9s) had `pool_writer_wait_seconds=73.9`
over 8 waits and no stalls. `sweep` runs `store.Expire` first in every tick
(every 50ms). `sweepLeases` opened a write transaction (`BEGIN IMMEDIATE`, the
only writer connection) on each call, to run `ExpiredLeases` and find nothing
almost every time. That transaction queued behind whatever else held the
writer, and the whole tick waited with it. `ExpiredLeases` is indexed
(`tickets_lease`), so the scan was not the cost; the writer was.

`Expire` is already one transaction per batch of expired tickets. `stepEvents`
writes per run, only for tickets that expired.

## Change

- `sweepLeases` looks for due leases on the reader pool first and takes the
  writer only when there are some. A lease that expires just after the read
  is swept next tick. Idle sweep, uncontended, local: 99us before, 10us
  after (5000 calls: 494ms, 50ms). The gain under contention is the queue
  time, which a quiet database does not show.
- The SQLite store keeps a writer log (`Store.WriterCalls`): for each write
  transaction that queued or held the writer for 10ms or more, the step
  (`store.WithStep`, set by the controller's `step`), the store method, the
  wait, the hold, and what held the writer when it began to queue. Calls
  holding the writer at read time are included.
- A `slow tick` record adds `slowest_call`, `slowest_call_wait_seconds`,
  `slowest_call_hold_seconds`, `slowest_call_blocked_by` (the slowest call of
  the slowest step, else of any step) and `writer_holders` (up to five
  `step/op=seconds`, hold time inside the tick). Same attributes go on the
  tick span.

## Not shown

The production trace was not available to this change, so which writer held
the 25s is not known. The next `slow tick` with `writer_queue` in its hints
names it in `writer_holders`; if `sweep/Expire` still queues, it is a long
holder elsewhere (look at `slowest_call_blocked_by`).
