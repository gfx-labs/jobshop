package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
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

// readCounter counts the store reads of a scan: Get, List and
// ClosedSessions calls.
type readCounter struct {
	store.Store
	gets, lists, closedReads atomic.Int64
}

func (r *readCounter) ClosedSessions(ctx context.Context) ([]store.ClosedSession, error) {
	r.closedReads.Add(1)
	return r.Store.ClosedSessions(ctx)
}

func (r *readCounter) Get(ctx context.Context, id string) (domain.Ticket, error) {
	r.gets.Add(1)
	return r.Store.Get(ctx, id)
}

func (r *readCounter) List(ctx context.Context, f store.Filter) ([]domain.Ticket, error) {
	r.lists.Add(1)
	return r.Store.List(ctx, f)
}

// TestScanSessionsFirstScanReadsOnce (#1345): the first scan after a start
// has a directory for every session ever made, nearly all closed with a
// dead host. It used to read one ticket per directory (N Gets). It now
// reads the closed tickets in one List, shared with the Ended backfill, so
// the walk makes no Get and the reads do not grow with N. Once Ended is
// filled, a scan with a few new directories keeps its per-directory Get
// and does not list the closed tickets again.
func TestScanSessionsFirstScanReadsOnce(t *testing.T) {
	t.Parallel()
	walkReads := func(t *testing.T, dirs int) (gets, lists int64) {
		t.Helper()
		var log lockedBuf
		c, _, _ := offTickController(t, quietYAML, &log)
		c.ctx = t.Context()
		ctx := t.Context()
		rc := &readCounter{Store: c.store}
		c.store = rc
		for range dirs {
			s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
			require.NoError(t, os.MkdirAll(c.root.SessionDir(s.ID), 0o700))
			_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
			require.NoError(t, err)
		}
		c.hostAlive = func(string) bool { return false }
		var gets0 int64
		c.scanHook = func(name string) {
			switch name {
			case "list_closed":
				gets0 = rc.gets.Load()
			case "walk":
				gets = rc.gets.Load() - gets0
			}
		}
		lists0 := rc.lists.Load()
		c.scanSessions(ctx)
		lists = rc.lists.Load() - lists0
		assert.Equal(t, dirs, len(c.sessDead), "every closed dead directory is cached")
		return gets, lists
	}
	g1, l1 := walkReads(t, 20)
	g2, l2 := walkReads(t, 90)
	assert.Zero(t, g1, "walk of 20 directories: before #1345 this was 20 Gets")
	assert.Zero(t, g2, "walk of 90 directories: before #1345 this was 90 Gets")
	assert.Equal(t, int64(1), l1, "list_open only: the closed read is ClosedSessions, not a List of tickets (#1361)")
	assert.Equal(t, l1, l2, "list calls do not grow with the directory count")

	t.Run("steady state", func(t *testing.T) {
		t.Parallel()
		var log lockedBuf
		c, _, _ := offTickController(t, quietYAML, &log)
		c.ctx = t.Context()
		ctx := t.Context()
		rc := &readCounter{Store: c.store}
		c.store = rc
		c.hostAlive = func(string) bool { return false }
		mk := func() {
			s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
			require.NoError(t, os.MkdirAll(c.root.SessionDir(s.ID), 0o700))
			_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
			require.NoError(t, err)
		}
		for range 30 {
			mk()
		}
		c.scanSessions(ctx)
		require.True(t, c.endedFilled)
		for range 3 {
			mk()
		}
		gets0, lists0 := rc.gets.Load(), rc.lists.Load()
		c.scanSessions(ctx)
		assert.Equal(t, int64(3), rc.gets.Load()-gets0, "one Get per new directory")
		assert.Equal(t, int64(1), rc.lists.Load()-lists0, "only list_open")
		assert.Equal(t, int64(1), rc.closedReads.Load(), "ClosedSessions ran on the first scan only")
	})
}

// TestScanSessionsBackfillEnded (#1361): the first scan fills Ended on a
// closed session without it, leaves one that has it, leaves one whose
// host still runs, and ignores open sessions; a later scan reads no
// closed sessions. The closed read is ids and an ended flag, so this pins
// that the flag matches fields.ended.
func TestScanSessionsBackfillEnded(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, _ := offTickController(t, quietYAML, &log)
	c.ctx = t.Context()
	ctx := t.Context()
	rc := &readCounter{Store: c.store}
	c.store = rc
	mk := func(fields string, closeIt bool) string {
		t.Helper()
		s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(fields)})
		require.NoError(t, os.MkdirAll(c.root.SessionDir(s.ID), 0o700))
		if closeIt {
			_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
			require.NoError(t, err)
		}
		return s.ID
	}
	filled := mk(`{"agent":"fake","ended":{"host_pid":99}}`, true)
	bare := mk(`{"agent":"fake"}`, true)
	running := mk(`{"agent":"fake"}`, true)
	open := mk(`{"agent":"fake"}`, false)
	c.hostAlive = func(dir string) bool { return dir == c.root.SessionDir(running) }

	ended := func(id string) *domain.SessionEnded {
		t.Helper()
		tk, err := c.store.Get(ctx, id)
		require.NoError(t, err)
		f, err := domain.DecodeFields[domain.SessionFields](tk)
		require.NoError(t, err)
		return f.Ended
	}
	c.scanSessions(ctx)
	require.True(t, c.endedFilled)
	if e := ended(filled); assert.NotNil(t, e) {
		assert.Equal(t, 99, e.HostPid, "an existing Ended is not rewritten")
	}
	assert.NotNil(t, ended(bare), "a closed session without Ended is filled")
	assert.Nil(t, ended(running), "a running host is filled once it is reaped")
	assert.Nil(t, ended(open), "an open session is not touched")
	assert.Equal(t, []string{running}, c.scannedOrphans(ctx, nil))
	assert.Equal(t, int64(1), rc.closedReads.Load())

	c.scanSessions(ctx)
	assert.Equal(t, int64(1), rc.closedReads.Load(), "no closed read once Ended is filled")
}

// TestScanSessionsParallelProbe (#1354): a walk with many directories to
// probe overlaps the probes, but still reports each directory once, in
// directory order, with the same orphans as a sequential walk. The first
// probe waits for a second one to start, so a sequential walk would hang.
func TestScanSessionsParallelProbe(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, _ := offTickController(t, quietYAML, &log)
	c.ctx = t.Context()
	n := parallelProbeMin * 3
	var ids, live []string
	for i := range n {
		id := fmt.Sprintf("js-s-%04d", i)
		require.NoError(t, os.MkdirAll(c.root.SessionDir(id), 0o700))
		ids = append(ids, id)
		if i%50 == 7 {
			live = append(live, id)
		}
	}
	var mu sync.Mutex
	probes := map[string]int{}
	overlap := make(chan struct{})
	var first atomic.Bool
	var release sync.Once
	c.hostAlive = func(dir string) bool {
		id := filepath.Base(dir)
		mu.Lock()
		probes[id]++
		mu.Unlock()
		if first.CompareAndSwap(false, true) {
			<-overlap
		} else {
			release.Do(func() { close(overlap) })
		}
		return slices.Contains(live, id)
	}
	var seen []string
	got := c.orphansWith(t.Context(), nil, c.newClosedSessions(), func(id string, _ time.Duration) { seen = append(seen, id) })
	assert.Equal(t, live, got, "orphans keep directory order")
	assert.Equal(t, ids, seen, "each reports every directory once, in order")
	for _, id := range ids {
		assert.Equal(t, 1, probes[id], id)
	}
}
