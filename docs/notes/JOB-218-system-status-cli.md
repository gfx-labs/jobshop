# `jobshop system status` (JOB-218, 2026-11-16)

Reading tick numbers used to need a Go program dialing `admin.sock` for the
`system.status` RPC (the one Desk shows). `jobshop system status` calls it
and prints the `rpc.SystemStatus` JSON: `last_tick`, `tick_seconds`, `steps`
(seconds per step of the last pass), `p95_seconds` and `samples` (the last 60
passes).

It uses the RPC's own check: the admin socket, or a session whose pool lists
`pool-list` under `admin`. Any other session socket gets the grant error
straight away (`system status needs the admin socket or pool admin grant
"pool-list"`), not a hang. `--socket` or `JOBSHOP_SOCKET` picks the socket,
as for every client command.

Test: `TestSystemStatus` (pkg/transport/cli/system_test.go) runs it against
the in-process controller over the admin socket, a granted session and an
ungranted one. `llm/cli.md` has the help text.

No `--watch`: rerun it, or read the `slow tick` log records.
