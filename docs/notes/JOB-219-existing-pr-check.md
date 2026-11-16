# JOB-219: existing PR check on implement steps

2026-11-16

Two sessions built the same fix because nothing showed the second one an
open or just-merged PR for its issue. The controller now comments those
PRs on the `implement` step of `pr-work`, a prefixed copy of it, and
`pr-work-linear`. It only shows them: no claim, comment or close is
refused, and a GitHub error is logged and skipped.

When it runs (`pkg/orchestration/control/existingpr.go`):

- At the step claim. It always comments, so the agent can tell the check
  ran: the PR list, or "no open or merged PR ... names <issue>". A repeat
  of the same text on a re-claim is not added again. Step creation does
  not comment: nobody reads the step before a claim.
- When the step comments a `github.com/.../pull/<n>` link, and at the
  close with outcome `pr`. These comment only when a PR that is not this
  run's own names the issue.

Which PRs: the repo's 300 most recently updated PRs (3 pages of 100,
`github.Client.ListPulls`), open or merged; closed unmerged ones are
dropped. A PR names a GitHub issue `#n` when its body says
`Closes/Fixes/Resolves/Part of #n` (or `owner/name#n`), its title ends in
`(#n)`, or its branch is `issue-n` or `issue-n-<slug>`. For a Linear key
it is the key in the title or branch, case-insensitive, not followed by
another digit. The repo is the run's `meta.github.repo`.

"Another session": the controller has no GitHub identity per session, so
a PR is the run's own when any non-controller comment on the run or its
steps links it. Any other open PR makes the first line "Open PR from
another session already names <issue>"; the list marks the run's own PRs
"(this run)".

The implement prompts tell the agent to read this comment instead of
running `gh pr list`.
