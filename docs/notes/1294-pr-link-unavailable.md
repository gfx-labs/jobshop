# "PR unavailable" apart from "no PR yet" (#1294, 2026-11-19)

Follows `JOB-131-inflight-pr-links.md` ("Not done"). <!-- notes:ignore -->

`run.show` now returns `pr_state` on the run result (`rpc.RunResult.PRState`),
next to `links`. It is per run, because Desk's in-flight row is per run.

- `linked`: at least one GitHub pull link (run vars and meta, or a PR a step
  recorded).
- `unavailable`: no PR found, and the lookup was incomplete: a step's
  comments could not be read, or the run has no step to read.
- `none`: every step was read and none names a PR.

A comment read error no longer fails `run.show` (it used to fail the whole
overview): the links found elsewhere are returned, the error is logged, and
the state says `unavailable`. A cancelled caller still gets the error.

Desk's in-flight table shows an italic `pr unavailable` with a tooltip in the
pr column (and under the title in the compact layout) for `unavailable`. `none`
keeps the dash. A PR link always wins over the marker.

Checks: `TestRunShowStepPRLinks` (none, linked), `TestRunLinksPRUnavailable`
(unavailable), and `ui/e2e/run-links.spec.js` "unavailable PR data" (mocked
overview; the controller cannot be made to fail its own store read). Run it
with `DESK_URL=... npx playwright test run-links.spec.js -g 1294`.
