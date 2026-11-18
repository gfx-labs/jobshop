# JOB-230: placeholder learning summaries (2026-11-18)

`domain.ValidLearning` rejects an agent learning whose summary, trimmed and
lowercased, is `none`, `n/a`, `na`, `-`, `nothing` or `no`. The error says
to skip the call when there is nothing to record. `jobshop learn add` and
the `correction`, `failure` and `workaround` shortcuts all send
`learn.add`, so all are covered; the CLI prints the server's error.

Learnings the controller writes (source `controller`) are not checked.
Only the whole summary is matched: `no space left on device` is accepted.
