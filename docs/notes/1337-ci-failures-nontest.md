# ci-failures: non-test failures and rerun-passed runs (#1344, target #1337)

2026-11-24. `jobshop report ci-failures` (see `JOB-13-ci-failures-report.md`)
only saw `go test` failures of failed master runs. It now also shows:

- `runs[].failed_jobs`: for each failed master run, every job of the latest
  attempt with conclusion `failure` or `timed_out` (cancelled and skipped
  jobs are not failures): `id`, `name`, first failed `step`, `conclusion`,
  `url`, and `error`, the first line of the job's failed log with `panic:`,
  `FAIL`, `Error:` or `error:`, cut to 200 characters. `no_artifact` is
  unchanged.
- `reruns`: recent completed runs with `attempt > 1`, on master and on PR
  branches (#1334 failed on a PR run and passed on retry). Each has `branch`,
  `attempt`, the final `conclusion` (`success` = passed on rerun) and
  `failed_jobs` from its earlier attempts, each with its `attempt`.
- `jobs`: failed jobs of both lists grouped by job and step name, like
  `tests`: `count` (failed job instances), `recovered` (of those, in runs
  that finally passed), `error` (newest instance that has one), `runs`,
  `last`. Most failures first.
- `warnings`: a gh read that failed (jobs of a run, a log, the rerun list).
  The report skips it and goes on; an unreadable log leaves `error` empty.

All new fields are additive and omitted when empty.

## gh calls

`--limit` N (default 5) failed runs, `--rerun-limit` M (default 5) completed
runs scanned for reruns on any branch of the workflow. For F failed runs
with J failed jobs each, and R reruns with A attempts and J' failed jobs per
earlier attempt:

- 1 `run list` (failed) + F downloads + F `run view --json jobs` + F·J logs
- 1 `run list` (completed, no branch filter) + R·(A-1) `run view --attempt`
  + R·(A-1)·J' logs

With the defaults and J=1, A=2, J'=1 that is 27 calls, 21 of them for the
new fields (before, 6). Raise
`--rerun-limit` to see further back: reruns are rare, so 5 runs often hold
none.

Code: `pkg/tooling/flaky/jobs.go`, `runs.go`.
