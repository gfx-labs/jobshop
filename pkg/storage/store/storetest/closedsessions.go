package storetest

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// testClosedSessions checks ClosedSessions lists closed session tickets
// only, by id, and reports fields.ended set or not (#1361).
func testClosedSessions(t *testing.T, s store.Store, _ *Clock) {
	ctx := t.Context()
	mk := func(kind domain.Kind, fields string, closeIt bool) string {
		t.Helper()
		tk, err := store.Create(ctx, s, "test", store.NewTicket{Project: proj, Kind: kind, Fields: json.RawMessage(fields)})
		require.NoError(t, err)
		if closeIt {
			_, err = store.Close(ctx, s, "test", tk.ID, nil, "done")
			require.NoError(t, err)
		}
		return tk.ID
	}
	got, err := s.ClosedSessions(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "no sessions yet")

	filled := mk(domain.KindSession, `{"agent":"jcode","ended":{"host_pid":7}}`, true)
	bare := mk(domain.KindSession, `{"agent":"jcode"}`, true)
	nulled := mk(domain.KindSession, `{"agent":"jcode","ended":null}`, true)
	mk(domain.KindSession, `{"agent":"jcode","ended":{"host_pid":8}}`, false) // open
	mk(domain.KindTask, `{}`, true)                                           // closed, not a session

	want := []store.ClosedSession{{ID: filled, Ended: true}, {ID: bare}, {ID: nulled}}
	slices.SortFunc(want, func(a, b store.ClosedSession) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	got, err = s.ClosedSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
