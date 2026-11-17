# JOB-224: finding learnings at run end

2026-11-17. Scan findings no longer write a learning when the triage run
starts. 12 of 21 learnings in 72h were findings for runs that closed
`done`, with no fix and nothing to learn.

- `fireFinding` puts `signal`, `subject` and `detail` (detail clipped to
  200 bytes) in the run's `meta.trigger`. Runs started for an issue's own
  workflow (`start`, JOB-211) are intake and carry none of it.
- `recordRunFinding` (`pkg/orchestration/control/learn.go`) writes one
  `failure` learning when such a run goes `needs_human` (`setRunState`,
  `stopFailedRun`). The summary has the finding and the cause: the hook
  error, else the exhausted step's last failed report note, else its last
  comment. There is no fix text.
- A run that ends `done` or `cancelled` records nothing. A run resumed
  and failing again records again, one per `needs_human` episode.
- `triggers.Host.RecordFinding` is removed.

Tests: `TestLearnFinding` in `learn_internal_test.go`.
`packs/selfdev/prompts/curate.md` does not mention findings; unchanged.
