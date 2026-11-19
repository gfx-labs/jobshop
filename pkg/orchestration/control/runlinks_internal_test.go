package control

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// failThread fails Thread for the ticket ids in bad.
type failThread struct {
	store.Store
	bad map[string]bool
}

func (f *failThread) Thread(ctx context.Context, id string, p store.Parts) (store.Thread, error) {
	if f.bad[id] {
		return store.Thread{}, errors.New("disk gone")
	}
	return f.Store.Thread(ctx, id, p)
}

// TestRunLinksPRUnavailable: a step whose comments cannot be read, or a run
// with no step to read, is "unavailable" when no PR is found, not "none".
// A PR found elsewhere still links and the read error does not fail the
// call (#1294).
func TestRunLinksPRUnavailable(t *testing.T) {
	c, _, _ := scanController(t)
	ctx := t.Context()
	run, err := store.Create(ctx, c.store, "controller", store.NewTicket{Project: "js", Kind: domain.KindRun, Title: "r", Fields: []byte(`{"workflow":"pr","state":"running"}`)})
	require.NoError(t, err)
	mk := func(title string) domain.Ticket {
		s, err := store.Create(ctx, c.store, "controller", store.NewTicket{Project: "js", Kind: domain.KindTask, Title: title, Pool: "dev"})
		require.NoError(t, err)
		return s
	}
	good, bad := mk("good"), mk("bad")
	fail := &failThread{Store: c.store, bad: map[string]bool{}}
	c.store = fail

	_, state, err := c.runLinks(ctx, run, []domain.Ticket{good})
	require.NoError(t, err)
	assert.Equal(t, rpc.PRNone, state, "read fine, no PR")

	_, state, err = c.runLinks(ctx, run, nil)
	require.NoError(t, err)
	assert.Equal(t, rpc.PRUnavailable, state, "no step to read")

	fail.bad[bad.ID] = true
	links, state, err := c.runLinks(ctx, run, []domain.Ticket{good, bad})
	require.NoError(t, err, "a read error does not fail run.show")
	assert.Empty(t, links)
	assert.Equal(t, rpc.PRUnavailable, state)

	_, err = store.AddComment(ctx, c.store, "controller", good.ID, "PR https://github.com/acme/widget/pull/7 head abc")
	require.NoError(t, err)
	links, state, err = c.runLinks(ctx, run, []domain.Ticket{good, bad})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://github.com/acme/widget/pull/7"}, links)
	assert.Equal(t, rpc.PRLinked, state, "a PR found is linked whatever failed")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = c.runLinks(cancelled, run, []domain.Ticket{bad})
	assert.Error(t, err, "a cancelled caller is an error")
}
