package control

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"sync/atomic"
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
	return c.orphansWith(ctx, open, c.newClosedSessions(), nil)
}

// orphansWith is orphans that calls each, when set, with every session
// directory it checked and how long the check took (JOB-221). A directory
// whose ticket is closed and whose host is dead goes in sessDead and is
// not checked again (#1311): sessions are never pruned, so without it the
// walk costs one host probe per session ever made. closed answers whether
// a directory's ticket is closed without a store read per directory.
func (c *Controller) orphansWith(ctx context.Context, open []domain.Ticket, closed *closedSessions, each func(id string, took time.Duration)) []string {
	keep := map[string]bool{}
	for _, t := range open {
		keep[t.ID] = true
	}
	entries, err := os.ReadDir(c.root.SessionDir(""))
	if err != nil {
		return nil
	}
	// Decide which directories need a probe first, so the probes can run
	// together; the results are applied in directory order below.
	var probe []string
	for _, e := range entries {
		id := e.Name()
		if keep[id] {
			// Open again (a closed session can be reopened): forget it
			// so the next time it closes its host is checked.
			c.forgetDead(id)
		} else if !c.isDead(id) {
			probe = append(probe, id)
		}
	}
	alive, took := c.probeHosts(probe)
	var out []string
	p := 0
	for _, e := range entries {
		id := e.Name()
		var d time.Duration
		if p < len(probe) && probe[p] == id {
			if alive[p] {
				out = append(out, id)
			} else if closed.has(ctx, id) {
				c.markDead(id)
			}
			d = took[p]
			p++
		}
		if each != nil {
			each(id, d)
		}
	}
	return out
}

// parallelProbeMin is how many directories a walk must probe before it
// probes them from several goroutines. Each probe opens two files; on the
// first scan after a start (thousands of directories, cold inode cache)
// the opens wait on the disk, and overlapping them cuts the walk (#1354).
// A steady-state scan probes a few new directories, which stay in order.
const parallelProbeMin = 64

// probeWorkers bounds the goroutines of one parallel probe.
const probeWorkers = 8

// probeHosts reports, for each id, whether its host runs and how long the
// probe took, in the order of ids.
func (c *Controller) probeHosts(ids []string) (alive []bool, took []time.Duration) {
	alive, took = make([]bool, len(ids)), make([]time.Duration, len(ids))
	fn := c.hostAliveFn()
	one := func(i int) {
		start := c.now()
		alive[i] = fn(c.root.SessionDir(ids[i]))
		took[i] = c.since(start)
	}
	if len(ids) < parallelProbeMin {
		for i := range ids {
			one(i)
		}
		return alive, took
	}
	var wg sync.WaitGroup
	var next atomic.Int64
	for range probeWorkers {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= len(ids) {
					return
				}
				one(i)
			}
		})
	}
	wg.Wait()
	return alive, took
}

// hostAliveFn is session.HostAlive; tests replace hostAlive to count probes.
func (c *Controller) hostAliveFn() func(dir string) bool {
	if c.hostAlive != nil {
		return c.hostAlive
	}
	return session.HostAlive
}

// directClosedGets is how many directories closedSessions checks with one
// Get each before it reads every closed session ticket in one List. A
// steady-state scan has a few new directories, where a Get is cheaper than
// listing thousands of tickets; the first scan after a start has thousands.
const directClosedGets = 8

// closedSessions tells whether a session directory's ticket is closed. A
// directory with no ticket, or a ticket in another state, is not: its host
// may not have started yet, so it is probed again next scan. Past
// directClosedGets lookups it answers from one List of the closed session
// tickets, which scanSessions loads first when backfillEnded needs them
// anyway (first-scan cost, #1345). One walk owns it; not safe for
// concurrent use.
type closedSessions struct {
	c       *Controller
	tickets []domain.Ticket
	ids     map[string]bool
	loaded  bool
	gets    int
}

func (c *Controller) newClosedSessions() *closedSessions { return &closedSessions{c: c} }

// load reads every closed session ticket once.
func (s *closedSessions) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	closed, err := s.c.store.List(ctx, store.Filter{Kind: domain.KindSession, Status: domain.StatusClosed, Limit: math.MaxInt32})
	if err != nil {
		return err
	}
	s.tickets, s.loaded = closed, true
	s.ids = make(map[string]bool, len(closed))
	for _, t := range closed {
		s.ids[t.ID] = true
	}
	return nil
}

func (s *closedSessions) has(ctx context.Context, id string) bool {
	if !s.loaded && s.gets >= directClosedGets {
		if err := s.load(ctx); err != nil {
			s.c.log.WarnContext(ctx, "scan sessions: list closed tickets", "err", err)
		}
	}
	if s.loaded {
		return s.ids[id]
	}
	s.gets++
	t, err := s.c.store.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.c.log.WarnContext(ctx, "scan sessions: get ticket", "session", id, "err", err)
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
	delete(c.sessPruned, id)
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
