# Replan backs off after idle passes (JOB-229)

2026-11-18. `replan` filed a planning ticket every 10 minutes after the
last closed, even when the planner found no target and no open issue and
changed nothing. Each pass started a plan session.

A trigger takes `cooldown_max` (Go duration, at least `cooldown`, which it
needs; not for scan triggers). The github pack sets `cooldown_max: 2h` on
`replan`.

- A pass is a ticket the trigger filed. Its window runs from its creation
  to the creation of the next one (for the newest, to now).
- A pass is idle when no ticket the trigger did not file was created or
  closed in its window: no issue taken into a run, no new run, no closed
  run or step, no escalation, no chat message.
- The cooldown doubles for each idle pass in a row, newest first, up to
  `cooldown_max` (10m, 20m, 40m, 80m, 2h for the pack). If the newest pass
  is not idle, the cooldown is the plain `cooldown`, counted from the
  close: a change that happens while it waits ends the wait at once if the
  plain cooldown has passed.
- Dedup by key, `max_open` and the cron/count conditions are unchanged.

Why derived from the store: nothing to persist or reset, a restart or a
replay gives the same answer, and "something changed" is what the store
already shows (a closed run, a started run, a Linear intake or issue scan
start). A stored counter would need a reset hook at every one of those
places.

Limits: a change that exists only on GitHub (a new or relabeled `target`,
a comment on one) is not in the store until a run starts, so a pass waits
at most `cooldown_max` for it. A planner pass that only comments or ticks
a checklist on a target counts as idle, but the merge of the PR it waited
on closes a run, which resets the wait.

Tested by `TestTriggerCooldownMax` (fake clock) and `TestParseTriggers`.
