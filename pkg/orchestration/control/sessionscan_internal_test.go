package control

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestScanSessionsOffTick (JOB-221): scan_sessions walks every session
// directory and reads journal tails; it runs as a housekeeping step, not
// under tickMu, so a slow scan cannot hold up the supervisor pass.
func TestScanSessionsOffTick(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, _ := offTickController(t, quietYAML, &log)
	c.tracer = noop.NewTracerProvider().Tracer("test")
	c.tickSteps = map[string]float64{}
	c.ctx = t.Context()
	var scans int
	c.scanHook = func(name string) {
		if name == "list_open" {
			scans++
		}
	}

	c.tick()
	assert.Zero(t, scans, "a tick does not scan")
	_, ok := c.tickSteps["scan_sessions"]
	assert.False(t, ok, "scan_sessions is not a tick step")

	c.housekeeping()
	assert.Equal(t, 1, scans, "housekeeping scans")
}

// TestScanSessionsSlowSubstep (JOB-221): a scan at or over the slow tick
// threshold logs "slow scan_sessions" with the time of each substep and
// the slowest session directory, so the cause of a slow scan is in the
// record. The fake clock moves inside the walk; nothing sleeps.
func TestScanSessionsSlowSubstep(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, _ := offTickController(t, quietYAML, &log)
	c.ctx = t.Context()
	require.NoError(t, os.MkdirAll(c.root.SessionDir("js-s-old"), 0o700))
	c.scanHook = func(name string) {
		if name == "walk" {
			clk.advance(31 * time.Second)
		}
	}

	c.scanSessions(t.Context())
	out := log.String()
	assert.Contains(t, out, "slow scan_sessions")
	assert.Contains(t, out, "walk_seconds=31")
	assert.Contains(t, out, "list_open_seconds=0")
	assert.Contains(t, out, "dirs=1")
	assert.Contains(t, out, "seconds=31.000")
}

// TestScanSessionsQuiet: a fast scan logs nothing.
func TestScanSessionsQuiet(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, _ := offTickController(t, quietYAML, &log)
	c.ctx = t.Context()
	c.scanSessions(t.Context())
	assert.NotContains(t, log.String(), "slow scan_sessions")
}
