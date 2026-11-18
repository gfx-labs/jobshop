package hooks

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// putCollection keeps records in memory and counts the calls that write.
// The Collection methods it does not define panic: Put does not use them.
type putCollection struct {
	store.Collection
	recs          map[string]store.Record
	puts, updates int
}

type putStore struct{ col *putCollection }

func (s putStore) Collection(context.Context, store.CollectionSpec) (store.Collection, error) {
	return s.col, nil
}

func (c *putCollection) Get(_ context.Context, key string) (store.Record, error) {
	if r, ok := c.recs[key]; ok {
		return r, nil
	}
	return store.Record{}, domain.ErrNotFound
}

func (c *putCollection) Put(_ context.Context, r store.Record) (store.Record, bool, error) {
	c.puts++
	if old, ok := c.recs[r.Key]; ok {
		return old, false, nil
	}
	c.recs[r.Key] = r
	return r, true, nil
}

func (c *putCollection) Update(_ context.Context, r store.Record) (store.Record, error) {
	c.updates++
	c.recs[r.Key] = r
	return r, nil
}

// TestPutRepeatSkipsWrite (#1288): offering an invocation that exists and
// is not superseded or retrying makes no store write, so a trigger that
// offers it every tick does not queue for the SQLite writer.
func TestPutRepeatSkipsWrite(t *testing.T) {
	t.Parallel()
	col := &putCollection{recs: map[string]store.Record{}}
	e := New(putStore{col}, nil, t.TempDir(), time.Now, slog.New(slog.DiscardHandler))
	in := Invocation{Trigger: "scan", Entry: "triggers.star:start"}

	require.NoError(t, e.Put(t.Context(), "k", in))
	assert.Equal(t, 1, col.puts, "the first offer is written")
	for range 5 {
		require.NoError(t, e.Put(t.Context(), "k", in))
	}
	assert.Equal(t, 1, col.puts, "repeats read only")

	// A retried invocation is still refreshed.
	key := hookKey("k")
	rec := col.recs[key]
	rec.State = "retry"
	col.recs[key] = rec
	require.NoError(t, e.Put(t.Context(), "k", in))
	assert.Equal(t, 1, col.updates)
	assert.Equal(t, "pending", col.recs[key].State)
}
