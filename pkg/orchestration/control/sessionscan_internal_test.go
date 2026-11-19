package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
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

// TestScanSessionsSkipsDeadClosed (#1311): a session directory whose
// ticket is closed and host dead is probed once; the second scan probes
// only new directories. A closed session whose host still runs, and a
// directory with no ticket, are probed on every scan, and the live host
// is reported as an orphan until it stops.
func TestScanSessionsSkipsDeadClosed(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, _ := offTickController(t, quietYAML, &log)
	c.ctx = t.Context()
	ctx := t.Context()

	mk := func() domain.Ticket {
		s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
		require.NoError(t, os.MkdirAll(c.root.SessionDir(s.ID), 0o700))
		return s
	}
	closeIt := func(s domain.Ticket) {
		_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
		require.NoError(t, err)
	}
	const dead = 5
	for range dead {
		closeIt(mk())
	}
	live := mk()
	closeIt(live)
	open := mk()
	require.NoError(t, os.MkdirAll(c.root.SessionDir("js-s-noticket"), 0o700))

	alive := map[string]bool{live.ID: true}
	probes := map[string]int{}
	c.hostAlive = func(dir string) bool {
		id := filepath.Base(dir)
		probes[id]++
		return alive[id]
	}
	total := func() (n int) {
		for _, v := range probes {
			n += v
		}
		return n
	}

	c.scanSessions(ctx)
	assert.Equal(t, dead+2, total(), "first scan probes every directory but the open session")
	assert.Zero(t, probes[open.ID])
	assert.Equal(t, []string{live.ID}, c.scannedOrphans(ctx, nil), "closed ticket with a live host is an orphan")

	clear(probes)
	fresh := mk()
	closeIt(fresh)
	c.scanSessions(ctx)
	assert.Equal(t, 3, total(), "second scan probes the live host, the ticketless dir and the new dir")
	assert.Equal(t, 1, probes[live.ID])
	assert.Equal(t, 1, probes[fresh.ID])
	assert.Equal(t, []string{live.ID}, c.scannedOrphans(ctx, nil))

	alive[live.ID] = false
	clear(probes)
	c.scanSessions(ctx)
	assert.Empty(t, c.scannedOrphans(ctx, nil), "the stopped host is no longer an orphan")
	clear(probes)
	c.scanSessions(ctx)
	assert.Equal(t, 1, total(), "only the ticketless dir is probed once the live host is cached dead")
}
