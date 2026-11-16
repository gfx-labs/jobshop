# 1271: session reopen through acphost.Reopen

2026-11-16

When a host was respawned (`fresh` in `runner.session`), jobshop used to
send `session/load` itself and fall back to `session/new` plus a journal
summary by hand. It now calls `acphost.Reopen` (acphost v0.28.0) in
`pkg/orchestration/control/reopen.go` (`runner.reopen`). Nothing persisted
or logged changed: the `resumed conversation` info line, the
`session/load failed; falling back` warning (`OnRefused`), `started a new
conversation` (`adoptConversation`, shared with `newConversation`, which
still serves a conversation the agent rejects mid-session), `SeedDue` and
the `noteAdvertised` config options are as before.

`Reopen` takes the initialize result already fetched in `session()`
(`welcome.Initialize`, or the marshalled `conn.Initialize` answer), so
initialize is not sent twice. It decides load or new from
`agentCapabilities.loadSession`. A session with no ACP id still takes
the `session/new` branch of `session()` unchanged.

Adapter `reopenCaller` maps `Reopen`'s `Call` onto the typed acp-go
`conn`, and keeps the config options of the load or new that succeeded:

- An agent's JSON-RPC error (`*acp.RequestError`) on a live context
  becomes `*acphost.RPCError`, wrapped so the log line keeps the acp-go
  text and data. A refused `session/load` falls back; a refused
  `session/new` is returned as `session/new: ...`.
- Any other error, and every error once the context is done, passes
  through as a transport error: no fallback. If the context is done
  during `session/load`, `runner.reopen` returns `r.dropped(conn)`.
- acp-go reports a peer that disconnected mid-call as a JSON-RPC -32603,
  so it reads as a refusal and the fallback's `session/new` then fails, as
  before.

Differences: a `session/load` failure that is neither a JSON-RPC error nor
a cancelled context (previously a fallback) is now returned as an error
and the session restarts. When the journal has no frames the summary
still comes from the recall index (`summarize`); an unreadable journal
warns through that call, not through `Reopen`, which ignores read errors.

Tests: `reopen_internal_test.go` (RPC error, load, cancelled context, lost
connection, the mapping); the existing restart tests in `session_test.go`
run unchanged.
