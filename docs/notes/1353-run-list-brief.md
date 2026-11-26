# run list: --brief and -s open (#1353, target #1351)

2026-11-25. `jobshop run list --brief` prints a JSON array with, per run,
only `id`, `state`, `workflow`, `key`, `title` and `resolution`, in the same
newest-first order. The default output is unchanged (full tickets, about
7 KB per run because of `fields.inputs`).

`-s open` (rpc `RunListParams.State = "open"`) lists runs that are not done <!-- notes:ignore -->
or cancelled: state `running` and `needs_human`. The controller filters by
ticket status (open or in_progress), so `--limit` counts only those runs.

Planner and ci-repair prompts use `--brief -s open` instead of piping the
full list through `jq`. To find the run of one issue, closed runs included:
`jobshop run list --brief --workflow <wf> --key issue-<n>`.
