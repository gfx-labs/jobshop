# ci-failures: the error line names the failing test (#1352, target #1351)

2026-11-25. `jobshop report ci-failures` picks the `error` of a failed job
from its `--log-failed` output in this order:

1. the first `--- FAIL`, `panic:` or `FAIL <pkg>` line;
2. else the first line containing `Error:` or `error:`.

Lines with `level=INFO` or `level=DEBUG` (any case, any timestamp prefix) are
skipped in both. Before, any line containing `FAIL` or an error marker won,
so a `host exiting` INFO line or a testify `Error:` line hid the test name.

`jobs` groups are now keyed by job, step and error line, so two failing tests
in one job are two groups. The trailing duration (`(0.01s)`, `\t0.5s`) is left
out of the key, and the group shows the newest instance's line.
