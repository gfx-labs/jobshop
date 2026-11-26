package hooks

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/execution/automation"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// unknownCall seeds the journal as a restart leaves it: the call was
// recorded as started and never finished (#1380).
func unknownCall(t *testing.T, capability string) (*putCollection, *Engine, context.Context, automation.Observation) {
	t.Helper()
	col := &putCollection{recs: map[string]store.Record{}}
	e := New(putStore{col}, nil, t.TempDir(), time.Now, slog.New(slog.DiscardHandler))
	ctx := context.WithValue(t.Context(), hookCallContextKey{}, hookCallContext{engine: e, invocation: "inv", attempt: 1})
	o := automation.Observation{Index: 1, Phase: "started", Capability: capability, InputDigest: "d"}
	require.NoError(t, observeHookCall(ctx, o))
	require.Equal(t, "UNKNOWN", col.recs["inv/1"].State)
	ctx = context.WithValue(t.Context(), hookCallContextKey{}, hookCallContext{engine: e, invocation: "inv", attempt: 2})
	return col, e, ctx, o
}

// A read that a restart cut short runs again on the next attempt instead of
// failing the hook and leaving a red master unhandled.
func TestReplayRunsUnfinishedRead(t *testing.T) {
	t.Parallel()
	for _, capability := range []string{"github.get", "tickets", "runs", "sessions"} {
		col, _, ctx, o := unknownCall(t, capability)
		_, found, err := replayHookCall(ctx, o)
		require.NoError(t, err, capability)
		assert.False(t, found, capability)

		o.StartedAt = time.Now()
		require.NoError(t, observeHookCall(ctx, o), capability)
		assert.Equal(t, 1, col.updates, capability)
		assert.Contains(t, string(col.recs["inv/1"].Value), `"attempt":2`, capability)
	}
}

// An arbitrary-URL GET, write or shell call with no recorded result may have had its effect, so
// it still needs reconciling, and the error says why.
func TestReplayRefusesUnfinishedWrite(t *testing.T) {
	t.Parallel()
	for _, capability := range []string{"http.get", "github.post", "shell", "github.graphql"} {
		_, _, ctx, o := unknownCall(t, capability)
		_, _, err := replayHookCall(ctx, o)
		require.ErrorContains(t, err, "UNKNOWN", capability)
		require.ErrorContains(t, observeHookCall(ctx, o), "already observed", capability)
	}
}
