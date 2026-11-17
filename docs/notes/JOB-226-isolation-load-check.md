# per_session range of label-rule and pack pools (JOB-226, 2026-11-17)

JOB-83 noted that a pool getting its isolation from a label rule or a pack
is "quarantined at the first spawn" when `per_session` does not fit the
host's subuid/subgid range. That has not been true since #728: the
controller runs the acphost preflight at start and on every reload, on the
config it is about to swap in, and it runs it on the merged isolation
(`IsolationFor`: defaults, label rules, profiles, agent, pool, packs
included). The per_session range goes in as `MapCount`, so acphost checks
that `[map_uid, map_uid+range)` and the gids lie inside one
`/etc/subuid` / `/etc/subgid` entry.

- A pool that fails is quarantined with `isolation preflight:` and the
  report: `user: uids 100000..100007 are not inside one /etc/subuid entry
  (entries: 100000..100003)` plus the `usermod` fix. `jobshop pool list`
  and the needs-human escalation show it; no session spawns. A reload that
  passes clears it.
- The range is host state (`/etc/subuid`), so `config.Load` and `jobshop
  config check` do not read it: a config valid on one host would fail on
  another. The check is at start and reload, which is the earliest point
  the host is known. No new config field, so the NixOS module is unchanged
  (its assertion still covers only what `settings` shows; its comment now
  points here).
- `TestPreflightPerSessionRange` (`preflight_range_internal_test.go`):
  a pool with per_session from a label rule, and one from a profile a
  label rule attaches, both too wide for the host range, are in
  `preflightFailures` naming the range; a rule pool that fits and a plain
  pool are not. Pack pools reach the same code: packs are merged into
  `Pools` before `IsolationFor`.
