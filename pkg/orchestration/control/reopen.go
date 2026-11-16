package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	acp "github.com/coder/acp-go-sdk"
	host "github.com/gfx-labs/acphost/pkg/acphost"
	"github.com/gfx-labs/jobshop/pkg/domain"
)

// reopenCaller lets acphost.Reopen (#1271) talk through the typed acp-go
// connection. It keeps the config options of whichever of session/load or
// session/new succeeded, which Reopen's result does not carry.
type reopenCaller struct {
	ctx  context.Context
	conn *acp.ClientSideConnection

	loadOpts, newOpts []acp.SessionConfigOption
	// failed is the method of the last call, the one that returned an
	// error when Reopen fails.
	failed string
}

// Call implements acphost.Caller for the two methods Reopen sends once it
// has the initialize result; initialize is never sent, the caller passes it
// in ReopenOptions.Initialize.
func (a *reopenCaller) Call(method string, params any) (json.RawMessage, error) {
	a.failed = method
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	switch method {
	case "session/load":
		var req acp.LoadSessionRequest
		if err := json.Unmarshal(b, &req); err != nil {
			return nil, err
		}
		resp, err := a.conn.LoadSession(a.ctx, req)
		if err != nil {
			return nil, a.fail(err)
		}
		a.loadOpts = resp.ConfigOptions
		return json.RawMessage(`{}`), nil
	case "session/new":
		var req acp.NewSessionRequest
		if err := json.Unmarshal(b, &req); err != nil {
			return nil, err
		}
		resp, err := a.conn.NewSession(a.ctx, req)
		if err != nil {
			return nil, a.fail(err)
		}
		a.newOpts = resp.ConfigOptions
		return json.Marshal(resp)
	}
	return nil, fmt.Errorf("reopen: unexpected call %s", method)
}

// fail maps an acp-go error to what Reopen reads: an agent's JSON-RPC
// error becomes an *acphost.RPCError, which Reopen treats as the agent
// refusing the call (and falls back on a refused session/load). Anything
// else, and every error once the context is done, stays as it is: a
// transport error that Reopen returns without a fallback.
func (a *reopenCaller) fail(err error) error {
	var re *acp.RequestError
	if a.ctx.Err() != nil || !errors.As(err, &re) {
		return err
	}
	data, _ := json.Marshal(re.Data)
	if re.Data == nil {
		data = nil
	}
	return agentRefusal{rpc: &host.RPCError{Code: re.Code, Message: re.Message, Data: data}, err: err}
}

// agentRefusal is an agent's JSON-RPC error. It reads as the acp-go error,
// so logs keep the code and data, and unwraps to both it and the
// *acphost.RPCError Reopen looks for.
type agentRefusal struct {
	rpc *host.RPCError
	err error
}

func (e agentRefusal) Error() string   { return e.err.Error() }
func (e agentRefusal) Unwrap() []error { return []error{e.rpc, e.err} }

// reopen reconnects the session to its conversation on a freshly started
// agent: session/load when the agent can, else (or when it refuses) a new
// conversation seeded with a summary of the old one. init is the agent's
// initialize result, already fetched. A cancelled ctx during session/load
// returns r.dropped(conn).
func (r *runner) reopen(ctx context.Context, conn *acp.ClientSideConnection, init json.RawMessage, cwd string, fields *domain.SessionFields) error {
	call := &reopenCaller{ctx: ctx, conn: conn}
	res, err := host.Reopen(call, host.ReopenOptions{
		Initialize:   init,
		SessionID:    fields.ACPSession,
		Cwd:          cwd,
		Journal:      filepath.Join(r.dir, host.JournalFile),
		SummaryBytes: host.DefaultSummaryBytes,
		OnRefused: func(err error) {
			// A failed load would fail again on every respawn and end the
			// session as crashed; a new conversation keeps going.
			r.log.WarnContext(r.ctx, "session/load failed; falling back to a new conversation", "acp_session", fields.ACPSession, "err", err)
		},
		OnFallback: func() {
			// The agent holds none of the old conversation: resend everything.
			r.resetInjected()
			fields.Injected = nil
		},
	})
	if err != nil {
		if ctx.Err() != nil && call.failed == "session/load" {
			return r.dropped(conn)
		}
		return fmt.Errorf("%s: %w", call.failed, err)
	}
	if res.Loaded {
		if err := r.c.updateSessionFields(ctx, r.id, func(f *domain.SessionFields) { noteAdvertised(f, call.loadOpts) }); err != nil {
			return err
		}
		r.log.InfoContext(r.ctx, "resumed conversation", "acp_session", fields.ACPSession, "path", "load")
		return nil
	}
	summary, source := res.Seed, "journal"
	if summary == "" {
		// No journal frames: the summary indexed for recall (JOB-14).
		summary, source = r.summarize(ctx)
	}
	return r.adoptConversation(ctx, fields, acp.SessionId(res.SessionID), call.newOpts, summary, source)
}
