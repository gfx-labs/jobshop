# First scan_sessions reads closed tickets in one batch

2026-11-24, #1345 (part of #1337).

## Cause

`orphansWith` probes every session directory. A directory whose host is
dead and that is not in the cached-dead set (#1311) was checked with
`store.Get` to see whether its ticket is closed. On the first scan after a
start nothing is cached, so that is one `Get` per directory (about 4800 on
this host). The probe itself (`session.HostAlive`) is 83-128 ms in total.

## Change

- `closedSessions` (`lifecycle.go`) answers "is this directory's ticket
  closed". While `endedFilled` is false, `scanSessions` loads the closed
  session tickets with one `List` before the walk (the substep is still
  `list_closed`), and the walk and `backfillEnded` both use that result.
  Before, `backfillEnded` made that same `List` after the walk, so the scan
  makes no extra read.
- Once `endedFilled` is true nothing lists closed tickets on a pass. A new
  directory is checked with one `Get`, as before. If a walk has more than
  `directClosedGets` (8) such directories it loads the closed list once
  instead, so a restart that did not fill Ended, or a burst of closed
  sessions, does not cost N reads.
- #1311's cache and the "orphan = live host, no open session ticket" rule
  are unchanged.

## Store reads

Walk of N dirs, all closed with a dead host, first scan:

| | Get in walk | List in scan |
| --- | --- | --- |
| before | N (20 -> 20, 90 -> 90) | 2 (open, closed) |
| after | 0 | 2 |

`TestScanSessionsFirstScanReadsOnce` asserts this (fake clock, no sleeps)
and that a later scan with 3 new dirs makes 3 `Get` and only the open
`List`.

## Time

Not measured on this host's real data (no copy of its database in the
worktree). Measured with a temporary test, 4800 closed dead sessions in a
fresh SQLite store in a test controller, wall time of `scanSessions`
including the 100-session Ended backfill, host load average above 100 so
the numbers are noisy:

- before: 319 ms, 433 ms
- after: 183 ms, 191 ms, 192 ms, 289 ms

The production figure (1.5 s walk) also includes the controller's live
database and load; expect the walk part to fall to about the list cost
(`list_closed_seconds` 0.39 s then). Check the next restart's `slow
scan_sessions` line (or its absence).
