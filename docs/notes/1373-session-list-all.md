# #1373 `session list --all` on a DB copy

2026-11-26. Follow-up to `614-session-attach-tail.md`, which left
`sessionInfo` batching open until `session list --all` was measured.
Result: under 500 ms, so nothing was batched.

## Setup

- Copy of gasserver's `jobshop.db` (1.26 GB) plus its WAL, opened with
  `sqlite.Open`. The session directories were the live ones, read only.
- 5105 session tickets: 5098 closed, 7 open or in progress (3 tickets of
  any kind were in progress). Output 5105 `SessionInfo`, 6.4 MB of JSON.
- Called `Controller.sessionList(All: true)` directly, in a throwaway
  test that is not committed: the live admin socket is closed to
  agents. This leaves out the socket and the CLI's rendering.
- Host load 18 to 22 on 6 cores (other sessions running), so the numbers
  are pessimistic.
- Cold is the first call in a fresh process (empty sqlite page cache and
  prepared statements). The file itself was in the OS cache, since it had
  just been copied. Warm is calls 2 to 6 in the same process. 5 processes,
  back to back.

## Numbers (ms)

| | cold (5 runs) | warm (25 calls) |
|---|---|---|
| `sessionList` total | 148, 148, 153, 165, 190 | 95 to 131, typically 100 to 107 |
| `store.List` of all sessions (incl. labels) | 60 to 69 (340 once, file not yet in OS cache) | 47 to 63 |
| `store.List` in-progress tickets | 0.3 to 0.6 | 0.2 to 0.3 |
| JSON encode of the result | 36 to 41 | 36 to 41 |

Per part of a warm 100 ms call, from a CPU profile:

- about 50 ms: the one session query (`store.List` with labels, one
  `withLabels` query, not one per session);
- about 40 ms: `closedSessionInfo` decoding each closed ticket's saved state
  (`DecodeFields`), plus GC from the 5105 tickets;
- under 1 ms: held tickets, one query for all sessions (#741).

## Why no batching

The per-session reads the note worried about are gone. A closed session
is listed from its ticket (#747): no store call, no journal or directory
read. Only open sessions go through `sessionInfoOf`, which takes the
held tickets from the one shared query. At 5105 sessions the whole call
is a fifth of the 500 ms bar, and it grows with ticket count, not with
store round trips. `sessionInfo` (one `Get` and one `List` per call) is
only used for single-session paths and orphans.

Revisit if the closed count passes about 25000, or if `--all` moves
to a remote store (postgres) where each query costs a round trip.
