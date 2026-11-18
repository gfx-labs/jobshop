# #1282: second-based histogram buckets (2026-11-18)

The duration histograms (`jobshop.rpc.duration`, `step.duration`,
`tick.step.duration`, `outbound.duration`, `lock.wait`,
`housekeeping.duration`) had the OpenTelemetry default boundaries, which
assume milliseconds. They record seconds, so every sample fell in `le="5"`.
They now use `secondBuckets` in `pkg/support/telemetry/metrics.go`: 0.005,
0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600.
`histogram_quantile` over `jobshop_tick_step_duration_seconds_bucket` now
gives p50 and p95. Series from before the deploy keep the old `le` labels.
`TestDurationBuckets` checks the boundaries on /metrics.
