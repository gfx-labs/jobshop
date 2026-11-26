# Hook replay error (#1380)

2026-11-26. `triggers.star:master_red` failed once, right after a controller
restart, with `capability recorded-result lookup failed`.

## Cause

Not reproduced against the live incident (the `Replay` error was dropped, so
the log never named it). Reproduced in a test from the code path:

- `observeHookCall` writes the call record as `UNKNOWN` before the call runs.
- A restart kills the hook mid-call. `recoverHookClaims` puts the invocation
  back to `pending`; the next attempt reuses the same call key
  (`<invocation>/<index>`).
- `replayHookCall` found the `UNKNOWN` record and returned
  `recorded external outcome is unknown or failed`, which the capability layer
  reduced to the generic message. For a `github.get` that is wrong: a read has
  nothing to reconcile.

Other `Replay` errors (store read, artifact read, digest mismatch) go through
the same message; they are now in it.

## Change

- `pkg/execution/automation/observe.go` wraps the `Replay` error (`%w`), so
  the cause is in the `automation hook failed` log (`err`) and the
  invocation record.
- `pkg/orchestration/hooks/execute.go`: an unfinished `github.get`, `tickets`,
  `runs` or `sessions` call is run again (replay returns not found, the
  started record is overwritten). `http.get` (an arbitrary URL may have
  effects), writes, `shell`, `jev` and GraphQL still need reconciling.

## Red master after a failed hook

A trigger hook failure is only logged (`HookFailed` comments on runs only).
`master-red` has `on:` plus `fallback: 5m`, so `pollDue` calls the handler
again every 5 minutes (first tick after a restart), under a new
`timer/<name>/<minute>` key. A failed webhook delivery is therefore picked up
by the next poll, at most about 5 minutes later. No extra retry was added.
