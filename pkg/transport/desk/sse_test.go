package desk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// afterReturnWriter records any write or flush once the handler returned.
// net/http would panic there: it has released the response's bufio writer.
type afterReturnWriter struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	returned bool
	late     int
}

func (w *afterReturnWriter) touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.returned {
		w.late++
	}
}

func (w *afterReturnWriter) Write(b []byte) (int, error) {
	w.touch()
	return w.ResponseRecorder.Write(b)
}

func (w *afterReturnWriter) Flush() {
	w.touch()
	w.ResponseRecorder.Flush()
}

func (w *afterReturnWriter) finish() {
	w.mu.Lock()
	w.returned = true
	w.mu.Unlock()
}

// lateCtl keeps the subscription callbacks and returns at once, the way
// rpc.Client.Events returns while its reader can still deliver a notification.
type lateCtl struct {
	*fakeCtl
	event   func(rpc.Event)
	session func(rpc.SessionEvent)
}

func (c *lateCtl) Events(_ context.Context, _ rpc.EventsSubscribeParams, fn func(rpc.Event)) (rpc.EventsSubscribeResult, error) {
	c.event = fn
	return rpc.EventsSubscribeResult{}, nil
}

func (c *lateCtl) Subscribe(_ context.Context, _ rpc.SessionSubscribeParams, fn func(rpc.SessionEvent)) (rpc.SessionSubscribeResult, error) {
	c.session = fn
	return rpc.SessionSubscribeResult{}, nil
}

// TestSSELateSend drives the callbacks after the handler returned (#1334).
func TestSSELateSend(t *testing.T) {
	ctl := &lateCtl{fakeCtl: &fakeCtl{}}
	h := New(Config{}, ctl, nil).Handler()
	for _, path := range []string{"/api/events", "/api/session/s1/stream"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:7480"
		w := &afterReturnWriter{ResponseRecorder: httptest.NewRecorder()}
		h.ServeHTTP(w, req)
		w.finish()
		before := w.Body.Len()

		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if ctl.event != nil && path == "/api/events" {
					ctl.event(rpc.Event{Seq: 3, Kind: "ticket"})
				}
				if ctl.session != nil && path != "/api/events" {
					ctl.session(rpc.SessionEvent{Seq: 3})
				}
			})
		}
		wg.Wait()
		assert.Zero(t, w.late, "%s wrote after the handler returned", path)
		assert.Equal(t, before, w.Body.Len(), path)
	}
}
