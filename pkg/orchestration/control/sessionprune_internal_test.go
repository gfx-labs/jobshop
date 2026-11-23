package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

const pruneYAML = quietYAML + "sessions: {prune_after: 7d}\n"

// pruneFixture writes a session dir with kept and removable files.
func pruneFixture(t *testing.T, c *Controller, id string) {
	t.Helper()
	dir := c.root.SessionDir(id)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scratch"), 0o700))
	for _, f := range []string{"journal.ndjson", "journal.idx", "spec.json", "exit.json", "host.log", "host.pid", "isolation.json", "scratch/tmp"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600))
	}
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// TestPruneSessions (#1324): with sessions.prune_after set, a closed
// session whose host is dead and which closed longer ago than the age loses
// everything but its journal, index, spec and exit files. A young one, an
// open one, one with no ticket and one whose host still runs are kept
// whole. A fake clock moves the ages; nothing sleeps.
func TestPruneSessions(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, _ := offTickController(t, pruneYAML, &log)
	ctx := t.Context()

	mk := func(closed bool) domain.Ticket {
		s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
		pruneFixture(t, c, s.ID)
		if closed {
			_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
			require.NoError(t, err)
		}
		return s
	}
	old := mk(true)
	oldLive := mk(true)
	open := mk(false)
	clk.advance(10 * 24 * time.Hour)
	young := mk(true)
	pruneFixture(t, c, "js-s-noticket")

	c.hostAlive = func(dir string) bool { return filepath.Base(dir) == oldLive.ID }
	pass := func() {
		c.scanSessions(ctx)
		c.pruneSessions(ctx, c.config())
	}

	pass()
	assert.ElementsMatch(t, []string{"exit.json", "journal.idx", "journal.ndjson", "spec.json"}, dirFiles(t, c.root.SessionDir(old.ID)))
	for _, id := range []string{oldLive.ID, open.ID, young.ID, "js-s-noticket"} {
		assert.Len(t, dirFiles(t, c.root.SessionDir(id)), 8, "%s is kept whole", id)
	}

	// The live host stops: it is dead and old, so it goes on the next pass.
	c.hostAlive = func(string) bool { return false }
	pass()
	assert.Len(t, dirFiles(t, c.root.SessionDir(oldLive.ID)), 4)
	assert.Len(t, dirFiles(t, c.root.SessionDir(young.ID)), 8)

	// The young one ages past the limit.
	clk.advance(8 * 24 * time.Hour)
	pass()
	assert.Len(t, dirFiles(t, c.root.SessionDir(young.ID)), 4)
	assert.Len(t, dirFiles(t, c.root.SessionDir(open.ID)), 8, "an open ticket is never pruned")
	assert.Len(t, dirFiles(t, c.root.SessionDir("js-s-noticket")), 8, "no ticket, no prune")
}

// TestPruneSessionsOffByDefault: without sessions.prune_after nothing is
// removed, however old the session.
func TestPruneSessionsOffByDefault(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, _ := offTickController(t, quietYAML, &log)
	ctx := t.Context()
	s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
	pruneFixture(t, c, s.ID)
	_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
	require.NoError(t, err)
	clk.advance(400 * 24 * time.Hour)
	c.hostAlive = func(string) bool { return false }

	c.scanSessions(ctx)
	c.pruneSessions(ctx, c.config())
	assert.Len(t, dirFiles(t, c.root.SessionDir(s.ID)), 8)
}

// TestPruneSessionsReopened: a closed session that is reopened after it was
// found dead is no longer a candidate.
func TestPruneSessionsReopened(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, _ := offTickController(t, pruneYAML, &log)
	ctx := t.Context()
	s := mkTicket(t, c, store.NewTicket{Kind: domain.KindSession, Pool: "dev", Fields: json.RawMessage(`{"agent":"fake"}`)})
	pruneFixture(t, c, s.ID)
	_, err := store.Close(ctx, c.store, "controller", s.ID, nil, "done")
	require.NoError(t, err)
	c.hostAlive = func(string) bool { return false }
	c.scanSessions(ctx)
	require.True(t, c.isDead(s.ID))

	_, err = store.Reopen(ctx, c.store, "controller", s.ID)
	require.NoError(t, err)
	clk.advance(30 * 24 * time.Hour)
	c.pruneSessions(ctx, c.config()) // before the next scan: the ticket is read again
	assert.Len(t, dirFiles(t, c.root.SessionDir(s.ID)), 8, "reopened: kept")
	c.scanSessions(ctx)
	assert.False(t, c.isDead(s.ID))
	c.pruneSessions(ctx, c.config())
	assert.Len(t, dirFiles(t, c.root.SessionDir(s.ID)), 8, "reopened: kept")
}
