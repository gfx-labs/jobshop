# no_merge ignores release and held PRs (JOB-223)

2026-11-17. Triage ran five times (2026-11-13..16) on `no_merge` with
nothing to do: master was idle because the only open PR was a release PR.

`no_merge` in `packs/github/triggers.star` fires when the CI branch has had
no commit for 6h and open PRs exist. It now counts a PR as open only when
it is not a draft, has none of `pr_review_skip_labels` (default `hold`) and
does not carry `release_pr_label`, the same filter `pr_unmerged` uses. The
author filter is not applied: a PR from the human still counts.

The script reads open PRs with the same GraphQL query as `pr_unmerged`
(first 100), after the commit-age check, so a recent commit costs no
GraphQL call. `pack.cue` passes `skip_labels` and `release_pr_label` to it.

Test: `TestNoMergeSkipsHeldPRs` in `pkg/execution/automation/github`.
