package control

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gfx-labs/jobshop/pkg/config"
	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Opt-in timing harness for scan_sessions (#1360). See
// docs/notes/1360-scan-timing-harness.md. It reads real wall time on
// purpose and asserts nothing about it, so it is not a CI test unless the
// environment names a database copy.
const (
	scanDBEnv       = "JOBSHOP_SCAN_DB"
	scanSessionsEnv = "JOBSHOP_SCAN_SESSIONS"
	scanRunsEnv     = "JOBSHOP_SCAN_RUNS"
	defaultScanRuns = 4
)

// timedStore records the duration of each List and Get, so a slow
// substep can be traced to one query.
type timedStore struct {
	store.Store
	mu    sync.Mutex
	calls []string
}

func (s *timedStore) note(name string, start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("%s %s", name, time.Since(start).Round(time.Microsecond)))
}

func (s *timedStore) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.calls
	s.calls = nil
	return out
}

func (s *timedStore) List(ctx context.Context, f store.Filter) ([]domain.Ticket, error) {
	defer s.note("List", time.Now())
	return s.Store.List(ctx, f)
}

func (s *timedStore) Get(ctx context.Context, id string) (domain.Ticket, error) {
	defer s.note("Get", time.Now())
	return s.Store.Get(ctx, id)
}

// dirState is what a scan must not change in the sessions directory.
func dirState(t *testing.T, dir string) (names []string, mtime time.Time) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	return names, fi.ModTime()
}

// runScanTiming opens the database at dbPath (it migrates and, through the
// Ended backfill, writes to it: pass a copy) on a controller whose
// sessions directory is sessionsDir, runs scanSessions runs times on the
// real clock and writes the wall time of each substep per scan to out.
// sessionsDir is only read: the controller root is a temp dir holding a
// symlink to it.
func runScanTiming(t *testing.T, dbPath, sessionsDir string, runs int, out io.Writer) {
	t.Helper()
	ctx := t.Context()
	before, beforeMod := dirState(t, sessionsDir)

	rootDir := t.TempDir()
	require.NoError(t, os.Symlink(sessionsDir, filepath.Join(rootDir, "sessions")))
	st, err := sqlite.Open(ctx, dbPath, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ts := &timedStore{Store: st}
	c := &Controller{
		root: config.Root{Dir: rootDir}, store: ts, log: slog.New(slog.DiscardHandler),
		sessions: map[string]context.CancelFunc{}, runners: map[string]*runner{},
		drained: map[string]bool{}, poolCrashLog: map[string][]time.Time{}, poolPausedUntil: map[string]time.Time{},
	}
	c.ctx = ctx

	_, _ = fmt.Fprintf(out, "db=%s sessions=%s dirs=%d\n", dbPath, sessionsDir, len(before))
	for i := 1; i <= runs; i++ {
		steps := map[string]time.Duration{}
		start := time.Now()
		last := start
		c.scanHook = func(name string) {
			now := time.Now()
			steps[name] = now.Sub(last)
			last = now
		}
		c.scanSessions(ctx)
		total := time.Since(start)
		line := fmt.Sprintf("scan %d:", i)
		for _, name := range []string{"list_open", "list_closed", "walk", "backfill"} {
			if d, ok := steps[name]; ok {
				line += fmt.Sprintf(" %s=%s", name, d.Round(time.Microsecond))
			} else {
				line += fmt.Sprintf(" %s=-", name) // the substep did not run in this scan
			}
		}
		line += fmt.Sprintf(" total=%s orphans=%d", total.Round(time.Microsecond), len(c.scannedOrphans(ctx, nil)))
		_, _ = fmt.Fprintln(out, line)
		_, _ = fmt.Fprintf(out, "  store: %s\n", strings.Join(ts.take(), ", "))
	}

	after, afterMod := dirState(t, sessionsDir)
	assert.Equal(t, before, after, "the scan must not add or remove session directories")
	assert.True(t, beforeMod.Equal(afterMod), "the scan must not write to the sessions directory")
}

// TestScanTimingHarness times scanSessions on a copy of a real database
// and session directory. It skips unless JOBSHOP_SCAN_DB and
// JOBSHOP_SCAN_SESSIONS are set; run it with -v -count=1 to see the output.
func TestScanTimingHarness(t *testing.T) {
	dbPath, sessionsDir := os.Getenv(scanDBEnv), os.Getenv(scanSessionsEnv)
	if dbPath == "" || sessionsDir == "" {
		t.Skipf("set %s (a copy of jobshop.db) and %s to time the session scan", scanDBEnv, scanSessionsEnv)
	}
	runs := defaultScanRuns
	if v := os.Getenv(scanRunsEnv); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, scanRunsEnv)
		runs = n
	}
	var out strings.Builder
	runScanTiming(t, dbPath, sessionsDir, runs, &out)
	t.Log("\n" + out.String())
}

// TestScanTimingHarnessFixture runs the harness on a small generated
// database and directory: it prints every substep for each scan, and
// leaves the sessions directory as it found it.
func TestScanTimingHarnessFixture(t *testing.T) {
	t.Parallel()
	c, _, dir := scanControllerYAML(t, quietYAML)
	ctx := t.Context()
	for range 3 {
		s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: []byte(`{"agent":"fake"}`)})
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions", s.ID), 0o700))
		_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
		require.NoError(t, err)
	}
	require.NoError(t, c.store.(interface{ Close() error }).Close())

	var out strings.Builder
	runScanTiming(t, filepath.Join(dir, "db"), filepath.Join(dir, "sessions"), 3, &out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 1+3*2, out.String())
	assert.Contains(t, lines[0], "dirs=3")
	for _, name := range []string{"list_open=", "list_closed=", "walk=", "backfill=", "total="} {
		assert.Contains(t, lines[1], name, "the first scan runs every substep")
	}
	assert.Contains(t, lines[1], "list_closed=")
	assert.Contains(t, lines[3], "list_closed=-", "list_closed does not run once Ended is filled")
	assert.Contains(t, lines[3], "backfill=-")
	assert.Contains(t, lines[2], "List ")
}
