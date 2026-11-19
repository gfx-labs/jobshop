package triggers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

// listSpy records the filter of every List.
type listSpy struct {
	store.Store
	lists []store.Filter
}

func (l *listSpy) List(ctx context.Context, f store.Filter) ([]domain.Ticket, error) {
	l.lists = append(l.lists, f)
	return l.Store.List(ctx, f)
}

type spyHost struct {
	Host
	s store.Store
}

func (h spyHost) Store() store.Store { return h.s }

func mkRun(t *testing.T, s store.Store, workflow string) domain.Ticket {
	t.Helper()
	f, err := json.Marshal(domain.RunFields{Workflow: workflow, State: domain.RunRunning})
	require.NoError(t, err)
	r, err := store.Create(t.Context(), s, "admin", store.NewTicket{Project: "js", Kind: domain.KindRun, Title: workflow, Fields: f})
	require.NoError(t, err)
	return r
}

// TestWorkflowTickets (#1299): the store filters runs by workflow, so a
// pass reads only the trigger's own runs, however many other runs exist.
func TestWorkflowTickets(t *testing.T) {
	t.Parallel()
	st, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "j.db"), sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	own := mkRun(t, st, "scan-fix")
	other := mkRun(t, st, "pr-work")
	sf, err := json.Marshal(domain.StepFields{Run: own.ID, Step: "fix"})
	require.NoError(t, err)
	step, err := store.Create(t.Context(), st, "admin", store.NewTicket{Project: "js", Kind: domain.KindStep, Title: "fix", ParentID: own.ID, Fields: sf})
	require.NoError(t, err)
	osf, err := json.Marshal(domain.StepFields{Run: other.ID, Step: "implement"})
	require.NoError(t, err)
	otherStep, err := store.Create(t.Context(), st, "admin", store.NewTicket{Project: "js", Kind: domain.KindStep, Title: "implement", ParentID: other.ID, Fields: osf})
	require.NoError(t, err)

	spy := &listSpy{Store: st}
	e := New(spyHost{s: spy})
	o, err := e.workflowTickets(t.Context(), "scan-fix")
	require.NoError(t, err)

	assert.True(t, o.has(own.ID))
	assert.True(t, o.has(step.ID))
	assert.False(t, o.has(other.ID))
	assert.False(t, o.has(otherStep.ID))

	var runLists int
	for _, f := range spy.lists {
		if f.Kind == domain.KindRun {
			runLists++
			assert.Equal(t, "scan-fix", f.Workflow)
			assert.Equal(t, store.NoLimit, f.Limit)
		}
	}
	assert.Equal(t, 1, runLists)
	// One run list, one children list of the open run, one escalation list.
	assert.Len(t, spy.lists, 3)

	spy.lists = nil
	o, err = e.workflowTickets(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, o.ids)
	assert.Empty(t, spy.lists)
}
