package control

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/execution/session"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// reapOrphans stops hosts whose session is not open: leftovers from a crash
// between spawn and record, or from a session closed while the controller
// was down.
func (c *Controller) reapOrphans(ctx context.Context, open []domain.Ticket) {
	for _, id := range c.orphans(ctx, open) {
		c.log.WarnContext(ctx, "stopping orphaned host", "session", id)
		c.stopHost(c.root.SessionDir(id))
	}
}

// orphans lists session directories with a live host whose session is not
// in open.
func (c *Controller) orphans(ctx context.Context, open []domain.Ticket) []string {
	return c.orphansWith(ctx, open, nil)
}

// orphansWith is orphans that calls each, when set, with every session
// directory it checked and how long the check took (JOB-221). A directory
// whose ticket is closed and whose host is dead goes in sessDead and is
// not checked again (#1311): sessions are never pruned, so without it the
// walk costs one host probe per session ever made.
func (c *Controller) orphansWith(ctx context.Context, open []domain.Ticket, each func(id string, took time.Duration)) []string {
	keep := map[string]bool{}
	for _, t := range open {
		keep[t.ID] = true
	}
	entries, err := os.ReadDir(c.root.SessionDir(""))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		id := e.Name()
		if keep[id] {
			// Open again (a closed session can be reopened): forget it
			// so the next time it closes its host is checked.
			c.forgetDead(id)
			if each != nil {
				each(id, 0)
			}
			continue
		}
		if c.isDead(id) {
			if each != nil {
				each(id, 0)
			}
			continue
		}
		start := c.now()
		if c.hostAliveFn()(c.root.SessionDir(id)) {
			out = append(out, id)
		} else if c.ticketClosed(ctx, id) {
			c.markDead(id)
		}
		if each != nil {
			each(id, c.since(start))
		}
	}
	return out
}

// hostAliveFn is session.HostAlive; tests replace hostAlive to count probes.
func (c *Controller) hostAliveFn() func(dir string) bool {
	if c.hostAlive != nil {
		return c.hostAlive
	}
	return session.HostAlive
}

// ticketClosed reports whether id is a closed session ticket. A directory
// with no ticket, or a ticket in another state, is not: its host may not
// have started yet, so it is probed again next scan.
func (c *Controller) ticketClosed(ctx context.Context, id string) bool {
	t, err := c.store.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			c.log.WarnContext(ctx, "scan sessions: get ticket", "session", id, "err", err)
		}
		return false
	}
	return t.Kind == domain.KindSession && t.Status == domain.StatusClosed
}

func (c *Controller) isDead(id string) bool {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	return c.sessDead[id]
}

func (c *Controller) markDead(id string) {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	if c.sessDead == nil {
		c.sessDead = map[string]bool{}
	}
	c.sessDead[id] = true
}

func (c *Controller) forgetDead(id string) {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	delete(c.sessDead, id)
}

// stopAll is the full shutdown: end every session, then stop.
func (c *Controller) stopAll() {
	c.mu.Lock()
	c.drain = true
	c.mu.Unlock()
	ctx := context.WithoutCancel(c.ctx)
	cfg := c.config()
	open, err := c.store.List(ctx, store.Filter{Kind: domain.KindSession, Status: domain.StatusOpen})
	if err != nil {
		c.log.Error("shutdown: list sessions", "err", err)
	}
	var wg sync.WaitGroup
	for _, t := range open {
		wg.Go(func() {
			c.stopRunner(t.ID)
			if t2, err := store.Close(ctx, c.store, "controller", t.ID, nil, "shutdown"); err != nil {
				c.log.Error("shutdown: close session", "session", t.ID, "err", err)
			} else {
				c.countClose(ctx, t2)
			}
			c.endSession(ctx, cfg, t.ID, "controller shutdown")
		})
	}
	wg.Wait()
	c.log.Info("all sessions stopped", "count", len(open))
	c.cancel()
}
