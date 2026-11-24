# Module per_session range assertion (#1346, 2026-11-24)

`nix/module.nix` checks that each pool's `isolation.user.per_session` ids
fit in `isolation.subIdStart + subIdCount`, so `nix flake check` and a
rebuild fail before a deploy. It now merges the user as the controller's
`IsolationFor` does, for pools in `settings.pools`:

- `settings.defaults.isolation`
- each `settings.defaults.isolation_by_label` rule whose labels the pool
  carries (`k=v` matches a pool label `k:v`, as `HasLabels`)
- `settings.agents.<agent>.isolation`
- the pool's own `isolation`

Within a level its `profiles` (from `settings.isolation_profiles`) come
first, then its own `user`; the last user set wins. The message names the
pool and the range. Tests: `nix/module-test.nix` (label rule, profile
named by a rule, defaults, agent, pool override; overflow and fit).

## Not checked in Nix

- Pools defined in a pack file (`pack.yaml`, `pack.cue`), live packs
  included. `settings.packs` and `livePacks` hold only paths and vars; the
  controller reads the pool definitions from the files at start, so they
  are not Nix values at evaluation time (and a live pack's checkout does
  not exist then). Labels a `packs` entry adds to its pools are likewise
  invisible, so a label rule that matches a pool only through them is not
  seen.

The controller's load preflight (#728, JOB-226,
`JOB-226-isolation-load-check.md`) is the backstop: it quarantines such a
pool at start or reload, against the real `/etc/subuid`.
