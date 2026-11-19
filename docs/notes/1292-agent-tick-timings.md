# Agent tick timings and per-trigger duration (#1292, 2026-11-19)

**`system status` for sessions.** `jobshop system status` (RPC `system.status`)
no longer needs the admin socket or the `pool-list` grant. Any session, in any
project, reads `last_tick`, `tick_seconds`, `steps`, `p95_seconds` and
`samples`. `rpc.SystemStatus` holds only timings, counts and the names of the
fixed tick steps (`run_triggers`, `sweep`, ...), so there is no reduced
struct for non-grant callers. A field added to it that carries names, paths
or tokens must be withheld from callers without the grant.

**Per-trigger metric.** `jobshop.trigger.duration` (Prometheus
`jobshop_trigger_duration_seconds`, histogram, unit `s`, the same second
buckets as `jobshop.tick.step.duration`) has one sample per trigger per
`triggers.Engine.Run`, label `trigger=<name>`. It is timed on the host clock
around the trigger's check and evaluation, like the slowest-trigger name in
the `slow tick` log. Labels are the configured trigger names, so cardinality
is bounded by the config. `evaluate` shows up by name next to the others
instead of only inside `run_triggers`.

Tests: `TestSystemStatus` (pkg/transport/cli), `TestTriggerDurationMetric`
(pkg/orchestration/control, fake clock, no sleep).
