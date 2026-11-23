package control

import (
	"context"
	"os"
	"path/filepath"
	"time"

	host "github.com/gfx-labs/acphost/pkg/acphost"

	"github.com/gfx-labs/jobshop/pkg/config"
	"github.com/gfx-labs/jobshop/pkg/domain"
)

// pruneBatch bounds the directories one housekeeping pass prunes, so the
// first pass after enabling the setting on thousands of directories does
// not hold the housekeeping lock for long. The rest follow on later passes.
const pruneBatch = 200

// keptSessionFiles are what stays of a pruned session directory: the
// journal in either form and its index (Desk, recall, `session attach`,
// the session list's tail), spec.json (what the session was started
// with) and exit.json (the exit reason session.list shows for a dead
// host). Everything else is host runtime state, sockets or scratch.
var keptSessionFiles = map[string]bool{
	host.JournalFile:           true,
	host.CompressedJournalFile: true,
	host.IndexFile:             true,
	"spec.json":                true,
	host.ExitFile:              true,
}

// pruneSessions removes the runtime files, sockets, scratch and worktree
// of closed sessions whose host is dead and which closed more than
// sessions.prune_after ago (#1324). It works from sessDead, the ids the
// scan found closed with a dead host, so a dir with an open ticket, no
// ticket or a live host is never a candidate. Off when the setting is
// empty. It runs after scanSessions, which fills sessDead.
func (c *Controller) pruneSessions(ctx context.Context, cfg *config.Config) {
	after := cfg.Sessions.PruneAfterDuration()
	if after <= 0 {
		return
	}
	cutoff := c.now().Add(-after)
	n := 0
	for _, id := range c.pruneCandidates() {
		if ctx.Err() != nil || n >= pruneBatch {
			break
		}
		if c.pruneSession(ctx, cfg, id, cutoff) {
			n++
		}
	}
	if n > 0 {
		c.log.InfoContext(ctx, "session dirs pruned", "dirs", n)
	}
}

// pruneSession prunes one directory and reports whether it removed
// anything. A directory with nothing left to remove is marked pruned and
// not looked at again.
func (c *Controller) pruneSession(ctx context.Context, cfg *config.Config, id string, cutoff time.Time) bool {
	dir := c.root.SessionDir(id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.markPruned(id)
		}
		return false
	}
	var rm []string
	for _, e := range entries {
		if !keptSessionFiles[e.Name()] {
			rm = append(rm, e.Name())
		}
	}
	if len(rm) == 0 {
		c.markPruned(id)
		return false
	}
	// The ticket is read again here, not trusted from the scan: it may
	// have been reopened since.
	t, err := c.store.Get(ctx, id)
	if err != nil || t.Kind != domain.KindSession || t.Status != domain.StatusClosed ||
		t.ClosedAt.IsZero() || !t.ClosedAt.Before(cutoff) {
		return false
	}
	if c.hostAliveFn()(dir) {
		c.forgetDead(id)
		return false
	}
	// A worktree is removed through git so the pool repo forgets it.
	// removeWorktree is a no-op without one.
	c.removeWorktree(cfg, t, "session prune")
	for _, name := range rm {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			continue // removed already, by git for the worktree
		}
		if name == "work" {
			// git did not remove it (pool gone from the config, repo
			// moved): a plain delete would leave a locked worktree
			// entry in the repo, so it stays for a person.
			c.log.WarnContext(ctx, "session prune: worktree left", "session", id)
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			c.log.WarnContext(ctx, "session prune: remove", "session", id, "name", name, "err", err)
		}
	}
	c.markPruned(id)
	return true
}

// pruneCandidates lists the dead-closed ids not yet pruned.
func (c *Controller) pruneCandidates() []string {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	var ids []string
	for id := range c.sessDead {
		if !c.sessPruned[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (c *Controller) markPruned(id string) {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	if c.sessPruned == nil {
		c.sessPruned = map[string]bool{}
	}
	c.sessPruned[id] = true
}
