# Session directory retention (#1324)

2026-11-23.

Setting: `sessions.prune_after` (Go duration or `Nd`; empty is off and the
default, so behaviour is unchanged). Host-only. NixOS:
`services.jobshop.sessions.pruneAfter`.

What it does: housekeeping step `prune_sessions` runs after
`scan_sessions`. It takes the ids `scan_sessions` cached as dead (#1311:
ticket is a closed session, host not running). For each one it reads the
ticket again and prunes only if the ticket is still closed, closed longer
ago than `prune_after` (the ticket's `closed_at` is the last-activity
time: the host is dead, so the journal no longer changes), and the host is
still not running. A dir with an open or in-progress ticket, no ticket, or
a live host is never a candidate. At most 200 dirs per pass.

Kept: `journal.ndjson` or `journal.ndjson.zst`, `journal.idx`,
`spec.json`, `exit.json`. These are what Desk, `jobshop recall`,
`session attach`, `session.list` (journal tail, exit reason) and journal
shipping read. Removed: `host.log`, `host.pid`, locks, sockets,
`isolation.json`, `supervisor.*`, the agent's scratch/home dirs. The
worktree (`work`) is removed with `git worktree remove` (as at session
end); if git could not, it is left and logged `worktree left`.

Why the journal stays: it is most of the bytes and the only thing readers
want. On this host (4784 dirs, 3.2 GB) the journals are 3.0 GB (compression
is not enabled here), the other files about 0.2 GB in ~31 700 entries. So
this setting cuts the file count and the host-probe cost, not the disk
use; `journal.compress` and a later journal archive do that. The dirs
themselves stay, so the walk still lists them, but a pruned dir has no
`host.pid` and is cheap to probe.

Cost: first scan after a start still probes every dir once (#1311's
cache is in memory). Measured here with `session.HostAlive` over all 4784
dirs, no store reads: 83-128 ms (ReadDir 2 ms). The 0.88 s walk in the
#1311 log therefore is mostly the per-dir ticket read, which pruning does
not change. The first prune pass after a start does one `ReadDir` per
dead dir and marks the ones with nothing left; dirs with something to
remove cost one ticket read each.

Not kept: a session reopened after pruning has no worktree or host files.
Use an age long enough that nobody reopens (30d or more).

Post-restart check left open by #1311 (`slow scan_sessions` in
`jobshop report incidents --since 24h`): not read in this session.
`report incidents` needs the admin socket or the `report-incidents` pool
grant, and the dev pool has neither. Run it on an admin session; the
controller there started 2026-11-20 (0.10.47). Fill the number in here.

Tests: `TestPruneSessions`, `TestPruneSessionsOffByDefault`,
`TestPruneSessionsReopened` (fake clock, no sleeps),
`TestParseSessionsPrune`.
