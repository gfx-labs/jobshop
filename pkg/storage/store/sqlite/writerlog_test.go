package sqlite_test

import (
	"database/sql"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

// TestWriterCallsNameHolder (#1273): a write that queued behind another
// is recorded with how long it waited and the step and operation that held
// the writer; the holder is recorded with its hold. The clock is fake, so
// the numbers are exact.
func TestWriterCallsNameHolder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openTest(t, sqlite.Options{})
	var offset atomic.Int64
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	sqlite.SetWriterClock(s, func() time.Time { return base.Add(time.Duration(offset.Load())) })

	waiterDone := make(chan error, 1)
	err := s.WriteTx(store.WithStep(ctx, "holder"), func(*sql.Tx) error {
		go func() {
			waiterDone <- s.WriteTx(store.WithStep(ctx, "waiter"), func(*sql.Tx) error { return nil })
		}()
		// The waiter has asked for the writer once the pool counts it.
		for s.PoolWaits()["writer"].Count == 0 {
			runtime.Gosched()
		}
		offset.Store(int64(2 * time.Second))
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, <-waiterDone)

	byStep := map[string]sqlite.WriterCall{}
	for _, c := range s.WriterCalls() {
		byStep[c.Step] = c
	}
	require.Contains(t, byStep, "holder")
	require.Contains(t, byStep, "waiter")
	h, w := byStep["holder"], byStep["waiter"]
	assert.Equal(t, "WriteTx", h.Op)
	assert.Equal(t, 2*time.Second, h.Hold)
	assert.Zero(t, h.Wait)
	assert.Equal(t, 2*time.Second, w.Wait)
	assert.Equal(t, "holder/WriteTx", w.BlockedBy)
	assert.Equal(t, "waiter/WriteTx", w.Label())
}

// TestWriterCallsActive: a call still holding the writer is listed, so a
// slow tick that ends while the writer is held names the holder.
func TestWriterCallsActive(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openTest(t, sqlite.Options{})
	var offset atomic.Int64
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	sqlite.SetWriterClock(s, func() time.Time { return base.Add(time.Duration(offset.Load())) })

	err := s.WriteTx(store.WithStep(ctx, "sweep"), func(*sql.Tx) error {
		offset.Store(int64(30 * time.Second))
		calls := s.WriterCalls()
		require.Len(t, calls, 1)
		assert.True(t, calls[0].Active)
		assert.Equal(t, 30*time.Second, calls[0].Hold)
		assert.Equal(t, "sweep/WriteTx", calls[0].Label())
		return nil
	})
	require.NoError(t, err)
	calls := s.WriterCalls()
	require.Len(t, calls, 1)
	assert.False(t, calls[0].Active)
}

// TestExpireWithoutDueLeaseSkipsWriter (#1273): the lease sweep runs every
// tick; with nothing due it must not queue for the writer. The sweep runs
// while a write transaction holds the writer and ends only after the
// sweep, so a sweep that took the writer would block forever.
func TestExpireWithoutDueLeaseSkipsWriter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openTest(t, sqlite.Options{})
	err := s.WriteTx(ctx, func(*sql.Tx) error {
		ids, err := s.Expire(ctx, "controller", "", "")
		require.NoError(t, err)
		assert.Empty(t, ids)
		return nil
	})
	require.NoError(t, err)
}
