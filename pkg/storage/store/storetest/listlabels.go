package storetest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// testListLabelsMany checks List attaches each ticket's labels, sorted,
// when it returns many tickets at once: labels are read for all the ids in
// one query (#1366).
func testListLabelsMany(t *testing.T, s store.Store, _ *Clock) {
	ctx := t.Context()
	const n = 700
	want := map[string][]string{}
	tickets := make([]store.NewTicket, n)
	for i := range tickets {
		var labels []string
		switch i % 3 {
		case 1:
			labels = []string{"b", "a"}
		case 2:
			labels = []string{"c"}
		}
		tickets[i] = store.NewTicket{Project: proj, Kind: domain.KindTask, Title: fmt.Sprintf("t%d", i), Labels: labels}
	}
	made, err := s.Apply(ctx, "test", store.Graph{Tickets: tickets})
	require.NoError(t, err)
	require.Len(t, made, n)
	for i, tk := range made {
		switch i % 3 {
		case 1:
			want[tk.ID] = []string{"a", "b"}
		case 2:
			want[tk.ID] = []string{"c"}
		}
	}
	got, err := s.List(ctx, store.Filter{Project: proj, Kind: domain.KindTask, Limit: store.NoLimit})
	require.NoError(t, err)
	seen := 0
	for _, tk := range got {
		if w, ok := want[tk.ID]; ok {
			assert.Equal(t, w, tk.Labels, tk.ID)
			seen++
		} else if _, mine := slicesIndex(made, tk.ID); mine {
			assert.Empty(t, tk.Labels, tk.ID)
			seen++
		}
	}
	assert.Equal(t, n, seen, "every ticket listed")
}

func slicesIndex(ts []domain.Ticket, id string) (int, bool) {
	for i, tk := range ts {
		if tk.ID == id {
			return i, true
		}
	}
	return 0, false
}
