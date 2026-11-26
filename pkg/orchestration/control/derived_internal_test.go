package control

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// TestCloseDerivedStaleSnapshot (#1392): the sweep lists an escalation,
// a human claims it, then the sweep settles it from the old list. The
// ticket must stay open under its holder, not close with "resumed".
func TestCloseDerivedStaleSnapshot(t *testing.T) {
	t.Parallel()
	c, _, _ := scanControllerYAML(t, quietYAML)
	ctx := t.Context()
	esc := mkTicket(t, c, store.NewTicket{Title: "escalation"})
	stale := esc // listed while unheld

	_, _, err := c.store.Claim(ctx, esc.ID, "human-1", time.Hour)
	require.NoError(t, err)

	c.closeDerived(ctx, stale, resolutionSuperseded, "run r resumed", false)
	got, err := c.store.Get(ctx, esc.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInProgress, got.Status, "held: not closed under its holder")
	comments, err := store.Comments(ctx, c.store, esc.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "run r resumed. Left open because human-1 holds it; close it when done.", comments[0].Body)

	c.closeDerived(ctx, stale, resolutionSuperseded, "run r resumed", true)
	got, err = c.store.Get(ctx, esc.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusClosed, got.Status, "final close still closes a held ticket")
}
