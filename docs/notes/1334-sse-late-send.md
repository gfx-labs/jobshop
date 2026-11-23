# SSE late send (#1334)

2026-11-23

`rpc.Client.Events` and `Subscribe` call their callback on the connection's
reader goroutine. A notification can arrive after the Desk handler returned
(the request context ended and the call returned). net/http had already
released the response's bufio writer, so `Flush` in `sse.send` panicked
with a nil pointer and took Desk down (seen in the e2e desk trace job).

Guard: `sse` holds a mutex and a `done` flag. Handlers (`events`,
`sessionStream`, `live`) `defer e.close()`. `send` and `keepalive` write
under the mutex and drop the event once done, so no write overlaps or
follows the handler's return. `live` only writes from its handler goroutine
(the callback feeds channels), but its keepalive goes through the same guard.

`rpc.Client.Events` is unchanged: loading the callback and calling it cannot
be made atomic with `Events` returning without a lock held across user code,
so the writer owns the guard. Test: `TestSSELateSend`.
