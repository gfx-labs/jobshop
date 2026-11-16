# Automatic learning expiry (JOB-220, 2026-11-16)

`jobshop learn expire` (JOB-198) was manual, so the open backlog grew back.
A housekeeping step, `expire_learnings`, now runs it.

- Config `learnings.expire_after`: Go duration or `Nd`. Default `14d`; `0`
  turns it off. A bad value fails config load. Packs cannot set it.
- The step runs at most once an hour (`learnExpireEvery`), on the
  housekeeping loop, not under the tick lock. It expires open learnings
  older than `expire_after` that have no `pr`: the same change as the
  command, through `knowledge.ExpireLearnings`.
- A learning that an open GitHub issue or PR cites stays open. The curator
  names learning ids in the issue and PR of a prompt fix, so this keeps
  the ones being worked on before the PR is marked on them. A citation is
  `learning 12`, `learnings #12, #14 and 15` or `learning ids 12 13` in a
  title or body (`citedLearnings`). Comments are not read.
- The open issues and PRs read are those of the workflow and `activity.repos`
  repos, plus any repo a candidate learning is scoped to (up to 1000 per
  repo). If that read fails nothing expires that hour and a warning is
  logged; the next hour retries. With no candidates there is no GitHub call.
- Each run that expires something logs `learnings expired` with `count`.
- Not covered: Linear issues and Jobshop tickets are not searched for
  citations. Expired learnings stay in recall; `learn update --status open`
  reopens one.
- Tests: `TestLearnExpireAuto` (old expired, cited and PR'd kept, injected
  clock), `TestLearnExpireAutoOff` (`0` disables, failed lookup expires
  nothing), `TestCitedLearnings`.
