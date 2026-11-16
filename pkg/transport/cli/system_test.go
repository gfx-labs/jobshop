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
// admin socket and a session without the pool-list grant gets the grant
// error (JOB-218).
func TestSystemStatus(t *testing.T) {
	root := controltest.NewRoot(t, `projects: {js: {}}
agents: {worker: {command: echo}}
pools:
  plain: {agent: worker, max_sessions: 0, dir: $ROOT}
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

	session := func(pool string) string {
		var s rpc.SessionNewResult
		c.Call(rpc.MethodSessionNew, rpc.SessionNewParams{Project: "js", Pool: pool}, &s)
		return s.Socket
	}

	buf.Reset()
	require.NoError(t, systemStatusCmd{}.Run(&Globals{Socket: session("granted")}))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &st))

	buf.Reset()
	err := systemStatusCmd{}.Run(&Globals{Socket: session("plain")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool admin grant")
	assert.Empty(t, buf.String())
}
