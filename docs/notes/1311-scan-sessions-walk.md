# scan_sessions walk (#1311)

2026-11-19. `scan_sessions` probed every session directory ever made
(`proc.Open` + `Running`, ~4.6ms each): 3964 dirs took ~18s and logged
`slow scan_sessions` on each pass.

Choice: remember, not archive. `orphansWith` keeps `sessDead`, the ids
whose ticket is closed and whose host was found dead, and skips them on
later scans. Archiving would add a config knob, move directories other
code reads by path (journals, Desk), and still leave the walk growing
until the age threshold. The cache needs no config and no disk change.

Rules:
- A dir is cached only when `HostAlive` is false and its ticket is a
  closed session. A dir with no ticket, or a ticket not closed, is probed
  every scan (a host may be spawning).
- A closed ticket whose host is still running is not cached, so it is
  still reported as an orphan; it is cached once the host is gone.
- A dir whose ticket is open or in progress is dropped from the cache
  (a closed session can be reopened).
- The cache is in memory: the first scan after a restart probes every dir
  once (the old cost, once per controller start).
- Cost after that is one `ReadDir` plus one ticket `Get` and one probe per
  new dir. `dirs` in the log still counts every dir.

Test: `TestScanSessionsSkipsDeadClosed` counts probes with
`Controller.hostAlive`.

Check after deploy: wait for the first scan after the restart, then
`jobshop report incidents --since 24h` should show no new
`slow scan_sessions` at ~4000 dirs. The first scan after the restart can
still be slow. Not checked yet (needs a deploy window).
