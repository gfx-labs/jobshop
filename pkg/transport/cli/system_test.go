package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/orchestration/control/controltest"
	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// TestSystemStatus: `system status` prints the SystemStatus JSON over the
// admin socket and to a session with or without the pool-list grant
// (JOB-218, #1292).
func TestSystemStatus(t *testing.T) {
	root := controltest.NewRoot(t, `projects: {js: {}, other: {}}
default_project: js
agents: {worker: {command: echo}}
pools:
  plain: {agent: worker, max_sessions: 0, dir: $ROOT}
  elsewhere: {agent: worker, project: other, max_sessions: 0, dir: $ROOT}
  granted: {agent: worker, max_sessions: 0, dir: $ROOT, admin: [pool-list]}
`)
	c := controltest.StartWith(t, root)
	c.Pass(1)

	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	t.Cleanup(func() { stdout = old })

	require.Equal(t, 0, Main([]string{"--socket", root.AdminSocket(), "system", "status"}, nil))
	var st rpc.SystemStatus
	require.NoError(t, json.Unmarshal(buf.Bytes(), &st))
	assert.Positive(t, st.Samples)
	assert.NotEmpty(t, st.Steps)
	assert.False(t, st.LastTick.IsZero())

	sessionIn := func(project, pool string) string {
		var s rpc.SessionNewResult
		c.Call(rpc.MethodSessionNew, rpc.SessionNewParams{Project: project, Pool: pool}, &s)
		return s.Socket
	}
	session := func(pool string) string { return sessionIn("js", pool) }

	buf.Reset()
	require.NoError(t, systemStatusCmd{}.Run(&Globals{Socket: session("granted")}))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &st))

	// No grant: the struct is timings only, so the full one is returned.
	buf.Reset()
	var plain rpc.SystemStatus
	require.NoError(t, systemStatusCmd{}.Run(&Globals{Socket: session("plain")}))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &plain))
	assert.Positive(t, plain.Samples)
	assert.NotEmpty(t, plain.Steps)
	assert.False(t, plain.LastTick.IsZero())

	// Any project: the status is not scoped to one.
	buf.Reset()
	require.NoError(t, systemStatusCmd{}.Run(&Globals{Socket: sessionIn("other", "elsewhere")}))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &plain))
	assert.Positive(t, plain.Samples)
}
