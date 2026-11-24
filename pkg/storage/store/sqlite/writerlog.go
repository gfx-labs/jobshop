package sqlite

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// Writer log (#1273). A slow tick showed 25s in one step with the writer
// pool's wait count at 8: PoolWaits says the writer was busy, not who held
// it. Every write transaction records how long it queued for the writer,
// how long it held it, and what held it when it began to queue. The
// controller reads the log for a slow tick.

// writerFloor is the queue or hold time a call needs to be kept. Most
// writes take well under it and run thousands of times a tick.
const writerFloor = 10 * time.Millisecond

// writerKeep bounds the finished calls kept.
const writerKeep = 256

// WriterCall is one use of the database writer.
type WriterCall struct {
	// Step is the supervisor step that made the call ("" outside a tick),
	// Op the store method ("Expire", "Put").
	Step, Op string
	// Collection and Key name the record a collection write is for (#1338):
	// every collection shares the Put, Update and Claim code, so Op alone
	// does not say which data held the writer. Key is empty for a call on
	// no single record.
	Collection, Key string
	// Start is when the call asked for the writer.
	Start time.Time
	// Wait is how long it queued, Hold how long it kept the writer.
	Wait, Hold time.Duration
	// BlockedBy names the holder ("step/op") when the call began to queue,
	// or the last one to release when none held the writer then.
	BlockedBy string
	// Active is set for a call still holding the writer when read.
	Active bool
}

// Label is "step/op", or the op alone, with "[collection]" after the op
// of a collection write. The key is not in it: holders add up by label.
func (c WriterCall) Label() string {
	op := c.Op
	if c.Collection != "" {
		op += "[" + c.Collection + "]"
	}
	if c.Step == "" {
		return op
	}
	return c.Step + "/" + op
}

type writeTag struct{ collection, key string }

type writeTagKey struct{}

// withWriteTag marks the write transactions begun with ctx as for key of
// collection, for the writer log.
func withWriteTag(ctx context.Context, collection, key string) context.Context {
	return context.WithValue(ctx, writeTagKey{}, writeTag{collection, key})
}

// End is when the call let go of the writer (the read time, if Active).
func (c WriterCall) End() time.Time { return c.Start.Add(c.Wait + c.Hold) }

type writerHold struct {
	log   *writerLog
	label string
	call  WriterCall
	got   time.Time
}

type writerLog struct {
	mu      sync.Mutex
	active  map[*writerHold]struct{}
	prev    string // label of the last call to release
	done    []WriterCall
	nowFunc func() time.Time // tests
}

func (l *writerLog) now() time.Time {
	if l.nowFunc != nil {
		return l.nowFunc()
	}
	return time.Now()
}

// begin notes that a call asks for the writer.
func (l *writerLog) begin(ctx context.Context) *writerHold {
	h := &writerHold{log: l}
	h.call.Step, h.call.Op = store.StepOf(ctx), callerOp()
	if t, ok := ctx.Value(writeTagKey{}).(writeTag); ok {
		h.call.Collection, h.call.Key = t.collection, t.key
	}
	h.label = h.call.Label()
	h.call.Start = l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	h.call.BlockedBy = l.prev
	var oldest *writerHold
	for a := range l.active {
		if oldest == nil || a.got.Before(oldest.got) {
			oldest = a
		}
	}
	if oldest != nil {
		h.call.BlockedBy = oldest.label
	}
	return h
}

// acquired notes that the call got the writer.
func (h *writerHold) acquired() {
	l := h.log
	h.got = l.now()
	h.call.Wait = h.got.Sub(h.call.Start)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active = map[*writerHold]struct{}{}
	}
	l.active[h] = struct{}{}
}

// release notes that the call let go; an unacquired call (the pool
// refused it) is dropped.
func (h *writerHold) release() {
	l := h.log
	end := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.active[h]; !ok {
		return
	}
	delete(l.active, h)
	h.call.Hold = end.Sub(h.got)
	l.prev = h.label
	if h.call.Wait < writerFloor && h.call.Hold < writerFloor {
		return
	}
	l.done = append(l.done, h.call)
	if len(l.done) > 2*writerKeep {
		l.done = append([]WriterCall(nil), l.done[len(l.done)-writerKeep:]...)
	}
}

// calls returns the kept calls, finished and active, oldest first.
func (l *writerLog) calls() []WriterCall {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]WriterCall(nil), l.done[max(0, len(l.done)-writerKeep):]...)
	for h := range l.active {
		c := h.call
		c.Hold, c.Active = now.Sub(h.got), true
		out = append(out, c)
	}
	return out
}

// WriterCalls returns the recent calls that queued for the writer or held
// it for at least 10ms, with the calls holding it now. The controller reads
// it for a slow tick (#1273).
func (s *Store) WriterCalls() []WriterCall { return s.wlog.calls() }

// callerOp names the store method that began the write: the outermost
// function of this package on the stack, with closure suffixes cut. The
// store interface has dozens of write methods; naming each at its call
// site would touch every one.
func callerOp() string {
	var pcs [32]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	op := ""
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "/store/sqlite.") {
			op = f.Function
		}
		if !more {
			break
		}
	}
	// ".../store/sqlite.(*Store).Expire.func1" -> "Expire"
	_, op, ok := strings.Cut(op, "/store/sqlite.")
	if !ok {
		return "unknown"
	}
	op = strings.TrimPrefix(op, "(*Store).")
	if i := strings.Index(op, ".func"); i >= 0 {
		op = op[:i]
	}
	return op
}
