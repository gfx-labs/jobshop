# run_triggers on a busy tick (#1288)

2026-11-18. Not fully explained: this change adds the probe that names the
trigger and removes one avoidable writer use. Nothing was measured after it
(the code is not deployed yet).

## Evidence (selfdev, 04:45 to 05:01 UTC, 0.10.36)

- 9 `slow tick` WARNs after 4h47m with none. In 8 of the 9 the slowest step
  was `run_triggers`, 0.88 to 2.59s of a 1.3 to 2.9s tick.
- `slowest_call` was `ClaimNext`, `Claim`, `Update` or `Put`; per-tick
  writer holds were small (0.08 to 0.30s). At 05:01:30 `ClaimNext` held the
  writer 1.289s and the tick waited 6.47s over 32 writer waits.
- Most ticks also had `process_stalled` and `cpu_pressure` (17 to 24 stalls
  up to 80ms, 1.0 to 1.5s total). Part of the time is CPU starvation from
  other processes, not the store.

## What changed

- `triggers.Engine.Run` times each trigger on the host clock and returns the
  slowest. A `slow tick` whose slowest step is `run_triggers` carries
  `slowest_trigger` and `slowest_trigger_seconds` (log and tick span). Each
  due trigger already had its own span `trigger <name>` under the step.
- `hooks.Engine.Put` reads the invocation key first (reader pool) and
  returns when it exists and is not `superseded` or `retry`. A scan trigger
  offers its handler every finding on every tick, and a handler trigger that
  holds offers `timer/<name>/<minute>`; `Collection.Put` takes the SQLite
  writer for the `INSERT ... ON CONFLICT DO NOTHING` even when the key is
  there, so each repeat queued behind `ClaimNext` for nothing. Triggers fire
  at the same times; a retried or superseded invocation is still refreshed.
- Tests: `TestSlowTickNamesTrigger` (fake clock moved by a store wrapper, no
  sleep), `TestPutRepeatSkipsWrite` (counts store writes: 1 for six offers
  instead of 6).

## Not found

The engine itself writes nowhere else on an idle pass (`Eval` only lists),
and `ClaimNext` is called by sessions, not by triggers. So a trigger step
that waits on the writer is waiting on other steps' writes. Whether the
repeated `Put` was the part that waited is not shown by the evidence above.
After deploy, read the new `slowest_trigger` on the next busy period: if a
trigger that fires nothing is still the slowest with `process_stalled`, the
cause is CPU starvation and needs a host change, not a trigger change.
