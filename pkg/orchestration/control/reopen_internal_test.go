package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	host "github.com/gfx-labs/acphost/pkg/acphost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reopenAgent answers session/load and session/new as a test says. The
// other acp.Agent methods are nil: Reopen sends nothing else.
type reopenAgent struct {
	acp.Agent
	load func() (acp.LoadSessionResponse, error)
}

func (a reopenAgent) LoadSession(context.Context, acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return a.load()
}

func (reopenAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: "new1", ConfigOptions: configOpts(1)}, nil
}

// reopenConn connects a client to ag over pipes. The returned func closes
// both ends, as a host that died mid-call would.
func reopenConn(ag acp.Agent) (*acp.ClientSideConnection, func()) {
	cr, aw := io.Pipe() // agent -> client
	ar, cw := io.Pipe() // client -> agent
	acp.NewAgentSideConnection(ag, aw, ar)
	conn := acp.NewClientSideConnection(nopClient{}, cw, cr)
	return conn, func() { _ = aw.Close(); _ = cw.Close() }
}

type nopClient struct{ acp.Client }

var capable = json.RawMessage(`{"agentCapabilities":{"loadSession":true}}`)

// TestReopenCallerErrors: an agent's JSON-RPC error on session/load is a
// refusal (fall back to a new conversation, OnRefused called); a cancelled
// context is a transport error with no fallback (#1271).
func TestReopenCallerErrors(t *testing.T) {
	t.Parallel()
	opts := func(refused *error, fellBack *bool) host.ReopenOptions {
		return host.ReopenOptions{
			Initialize: capable, SessionID: "old1", Cwd: "/w", Journal: "/nonexistent",
			OnRefused:  func(err error) { *refused = err },
			OnFallback: func() { *fellBack = true },
		}
	}

	t.Run("rpc error falls back", func(t *testing.T) {
		t.Parallel()
		conn, closeConn := reopenConn(reopenAgent{load: func() (acp.LoadSessionResponse, error) {
			return acp.LoadSessionResponse{}, &acp.RequestError{Code: -32603, Message: "session not found", Data: map[string]any{"why": "gone"}}
		}})
		defer closeConn()
		call := &reopenCaller{ctx: t.Context(), conn: conn}
		var refused error
		var fellBack bool
		res, err := host.Reopen(call, opts(&refused, &fellBack))
		require.NoError(t, err)
		assert.False(t, res.Loaded)
		assert.Equal(t, "new1", res.SessionID)
		assert.True(t, fellBack)
		require.Error(t, refused)
		assert.True(t, host.IsRPCError(refused))
		var re *acp.RequestError
		require.ErrorAs(t, refused, &re, "the acp-go error stays reachable for the log")
		assert.Contains(t, refused.Error(), "session not found")
		assert.Contains(t, refused.Error(), "gone", "the data stays in the log line")
		assert.Len(t, call.newOpts, 1)
		assert.Empty(t, call.loadOpts)
	})

	t.Run("load keeps config options", func(t *testing.T) {
		t.Parallel()
		conn, closeConn := reopenConn(reopenAgent{load: func() (acp.LoadSessionResponse, error) {
			return acp.LoadSessionResponse{ConfigOptions: configOpts(2)}, nil
		}})
		defer closeConn()
		call := &reopenCaller{ctx: t.Context(), conn: conn}
		var refused error
		var fellBack bool
		res, err := host.Reopen(call, opts(&refused, &fellBack))
		require.NoError(t, err)
		assert.True(t, res.Loaded)
		assert.Equal(t, "old1", res.SessionID)
		assert.False(t, fellBack)
		assert.NoError(t, refused)
		assert.Len(t, call.loadOpts, 2)
	})

	// acp-go reports a peer that disconnected mid-call as a JSON-RPC
	// -32603, so it reads as a refusal; the fallback's session/new then
	// fails on the dead connection, as the hand-written fallback did.
	t.Run("lost connection fails the fallback", func(t *testing.T) {
		t.Parallel()
		var closeConn func()
		var conn *acp.ClientSideConnection
		conn, closeConn = reopenConn(reopenAgent{load: func() (acp.LoadSessionResponse, error) {
			closeConn() // the host dies before it answers
			return acp.LoadSessionResponse{}, nil
		}})
		call := &reopenCaller{ctx: t.Context(), conn: conn}
		var refused error
		var fellBack bool
		_, err := host.Reopen(call, opts(&refused, &fellBack))
		require.Error(t, err)
		assert.True(t, fellBack)
		assert.Equal(t, "session/new", call.failed)
	})

	t.Run("cancelled context is not a refusal", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		defer close(release)
		conn, closeConn := reopenConn(reopenAgent{load: func() (acp.LoadSessionResponse, error) {
			cancel() // the session is stopping
			<-release
			return acp.LoadSessionResponse{}, &acp.RequestError{Code: -32603, Message: "late"}
		}})
		defer closeConn()
		call := &reopenCaller{ctx: ctx, conn: conn}
		var refused error
		var fellBack bool
		_, err := host.Reopen(call, opts(&refused, &fellBack))
		require.Error(t, err)
		assert.False(t, host.IsRPCError(err))
		assert.False(t, fellBack)
		assert.NoError(t, refused)
		assert.Equal(t, "session/load", call.failed)
		require.Error(t, ctx.Err(), "the caller checks ctx.Err() to return r.dropped")
	})
}

// configOpts is n advertised config options.
func configOpts(n int) []acp.SessionConfigOption {
	var out []acp.SessionConfigOption
	for i := range n {
		vals := acp.SessionConfigSelectOptionsUngrouped{{Name: "v", Value: "v"}}
		out = append(out, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: acp.SessionConfigId(fmt.Sprint("opt", i)), Name: "o", Type: "select", CurrentValue: "v",
			Options: acp.SessionConfigSelectOptions{Ungrouped: &vals},
		}})
	}
	return out
}

// TestReopenCallerFail: only an agent's JSON-RPC error on a live context is
// mapped to *acphost.RPCError; every other error passes through.
func TestReopenCallerFail(t *testing.T) {
	t.Parallel()
	plain := errors.New("write: broken pipe")
	live := &reopenCaller{ctx: t.Context()}
	assert.Same(t, plain, live.fail(plain))
	rpc := live.fail(&acp.RequestError{Code: -32602, Message: "bad", Data: map[string]any{"k": 1}})
	var re *host.RPCError
	require.ErrorAs(t, rpc, &re)
	assert.Equal(t, -32602, re.Code)
	assert.JSONEq(t, `{"k":1}`, string(re.Data))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := &reopenCaller{ctx: ctx}
	refusal := &acp.RequestError{Code: -32603, Message: "x"}
	assert.Same(t, refusal, done.fail(refusal), "a cancelled context makes any error a transport error")
}
