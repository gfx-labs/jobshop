package control

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	host "github.com/gfx-labs/acphost/pkg/acphost"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/execution/session"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// Session list state (JOB-32). session.list used to walk every session
// directory and read the exit file and journal tail of every closed
// session on each request. A closed session's host state no longer
// changes, so it is saved on the ticket when the session ends, and the
// directory walk for orphaned hosts runs once per supervisor pass.
// Liveness of open sessions is still read at request time: a stored
// value goes stale on a host crash or controller restart.

// endedBackfillBatch bounds how many old closed sessions one pass fills.
const endedBackfillBatch = 100

// hostEnded reads a stopped host's state from its session directory.
func (c *Controller) hostEnded(id string) domain.SessionEnded {
	dir := c.root.SessionDir(id)
	e := domain.SessionEnded{HostPid: session.HostPid(dir)}
	if ex, ok := host.ReadExit(dir); ok {
		e.Exit = ex.Reason
	}
	if seq, at := host.JournalTail(dir); seq > 0 {
		e.Seq, e.SeqTime = seq, &at
	}
	return e
}

// saveEnded stores the host state of a session that just ended.
func (c *Controller) saveEnded(ctx context.Context, id string) {
	e := c.hostEnded(id)
	if err := c.updateSessionFields(ctx, id, func(f *domain.SessionFields) { f.Ended = &e }); err != nil {
		c.log.WarnContext(ctx, "save ended session state", "session", id, "err", err)
	}
}

// scanTimes is where one scan spent its time (JOB-221): the substeps,
// what each walked, and the slowest single directory or session, so a
// slow scan names its cause.
type scanTimes struct {
	ListOpen, Walk, ListClosed, Backfill time.Duration
	Dirs, Orphans, Filled                int
	SlowestDir, SlowestEnded             time.Duration
	SlowestDirID, SlowestEndedID         string
}

func (s scanTimes) total() time.Duration { return s.ListOpen + s.Walk + s.ListClosed + s.Backfill }

// attrs is the span and log form of the times.
func (s scanTimes) attrs() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Float64("jobshop.scan.list_open_seconds", s.ListOpen.Seconds()),
		attribute.Float64("jobshop.scan.walk_seconds", s.Walk.Seconds()),
		attribute.Int("jobshop.scan.dirs", s.Dirs),
		attribute.Int("jobshop.scan.orphans", s.Orphans),
		attribute.Float64("jobshop.scan.slowest_dir_seconds", s.SlowestDir.Seconds()),
		attribute.String("jobshop.scan.slowest_dir", s.SlowestDirID),
		attribute.Float64("jobshop.scan.list_closed_seconds", s.ListClosed.Seconds()),
		attribute.Float64("jobshop.scan.backfill_seconds", s.Backfill.Seconds()),
		attribute.Int("jobshop.scan.backfilled", s.Filled),
		attribute.Float64("jobshop.scan.slowest_ended_seconds", s.SlowestEnded.Seconds()),
		attribute.String("jobshop.scan.slowest_ended", s.SlowestEndedID),
	}
}

// scanSessions walks the session directories: it keeps the ids with a live
// host and no open session ticket, and fills Ended on closed sessions that
// ended before it was recorded. It is a housekeeping step, not a tick
// step (JOB-221): it does file I/O for every session directory ever made
// (thousands) and, until Ended is filled, reads the journal tail of closed
// sessions, which can decompress a whole journal. Under tickMu it held the
// pass for up to 31s while the host was under CPU pressure. A scan at or
// over the slow tick threshold logs "slow scan_sessions" with the
// substep times.
func (c *Controller) scanSessions(ctx context.Context) {
	var tm scanTimes
	mark := c.scanMark()
	defer func() {
		trace.SpanFromContext(ctx).SetAttributes(tm.attrs()...)
		if tm.total() < c.slowScan() {
			return
		}
		args := []any{"seconds", fmt.Sprintf("%.3f", tm.total().Seconds())}
		for _, kv := range tm.attrs() {
			args = append(args, strings.TrimPrefix(string(kv.Key), "jobshop.scan."), kv.Value.String())
		}
		c.log.WarnContext(ctx, "slow scan_sessions", args...)
	}()
	open, err := c.store.List(ctx, store.Filter{Kind: domain.KindSession, Statuses: []domain.Status{domain.StatusOpen, domain.StatusInProgress}, Limit: math.MaxInt32})
	mark(&tm.ListOpen, "list_open")
	if err != nil {
		return
	}
	closed := c.newClosedSessions()
	// Until Ended is filled the closed tickets are needed for the backfill
	// anyway: read them once before the walk, so the walk does not read one
	// ticket per directory (#1345).
	c.sessScanMu.Lock()
	done := c.endedFilled
	c.sessScanMu.Unlock()
	var listErr error
	if !done {
		listErr = closed.load(ctx)
		mark(&tm.ListClosed, "list_closed")
	}
	orphans := c.orphansWith(ctx, open, closed, func(id string, took time.Duration) {
		tm.Dirs++
		if took > tm.SlowestDir {
			tm.SlowestDir, tm.SlowestDirID = took, id
		}
	})
	mark(&tm.Walk, "walk")
	tm.Orphans = len(orphans)
	c.sessScanMu.Lock()
	c.sessOrphans = orphans
	c.sessScanned = true
	c.sessScanMu.Unlock()
	if !done && listErr == nil {
		c.backfillEnded(ctx, orphans, closed.list, &tm, mark)
	}
}

// slowScan is the scan length that gets a log line: the slow tick
// threshold.
func (c *Controller) slowScan() time.Duration {
	if c.probe != nil {
		return c.probe.slow
	}
	return slowTickAfter
}

// scanMark returns a func that adds the time since its last call to *d, on
// the controller's clock, after calling scanHook with the substep name
// (tests advance a fake clock there to make one substep slow).
func (c *Controller) scanMark() func(d *time.Duration, name string) {
	last := c.now()
	return func(d *time.Duration, name string) {
		if c.scanHook != nil {
			c.scanHook(name)
		}
		now := c.now()
		*d += now.Sub(last)
		last = now
	}
}

// backfillEnded fills Ended on up to endedBackfillBatch closed sessions
// per pass, until none is left.
func (c *Controller) backfillEnded(ctx context.Context, orphans []string, closed []store.ClosedSession, tm *scanTimes, mark func(*time.Duration, string)) {
	defer mark(&tm.Backfill, "backfill")
	n := 0
	for _, cs := range closed {
		if cs.Ended || slices.Contains(orphans, cs.ID) {
			continue // host still running: filled once it is reaped
		}
		if n == endedBackfillBatch {
			return
		}
		n++
		tm.Filled++
		start := c.now()
		c.saveEnded(ctx, cs.ID)
		if took := c.since(start); took > tm.SlowestEnded {
			tm.SlowestEnded, tm.SlowestEndedID = took, cs.ID
		}
	}
	c.sessScanMu.Lock()
	c.endedFilled = true
	c.sessScanMu.Unlock()
}

// scannedOrphans returns the last pass's orphaned host ids. Before the
// first pass it walks the directories itself.
func (c *Controller) scannedOrphans(ctx context.Context, open []domain.Ticket) []string {
	c.sessScanMu.Lock()
	ids, ok := slices.Clone(c.sessOrphans), c.sessScanned
	c.sessScanMu.Unlock()
	if !ok {
		return c.orphans(ctx, open)
	}
	return ids
}

// closedSessionInfo is a closed session's list entry from its ticket
// alone: its host is stopped, so the saved Ended state is current.
func closedSessionInfo(t domain.Ticket) rpc.SessionInfo {
	info := rpc.SessionInfo{ID: t.ID}
	if f, err := domain.DecodeFields[domain.SessionFields](t); err == nil {
		info.DisplayName = f.DisplayName
		info.Model, info.Effort, info.Override = f.Model, f.Effort, f.Override
		if e := f.Ended; e != nil {
			info.HostPid, info.Seq, info.SeqTime, info.Exit = e.HostPid, e.Seq, e.SeqTime, e.Exit
		}
	}
	if info.DisplayName == "" {
		info.DisplayName = defaultDisplayName(t, nil)
	}
	return info
}
