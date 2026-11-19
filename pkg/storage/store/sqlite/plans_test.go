package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

// hookLike is a spec shaped like hook_invocations: a trigger field index.
var hookLike = store.CollectionSpec{
	Name:  "hook_like",
	Index: []store.Field{{Name: "trigger", Path: "trigger"}},
}

// explain returns the query plan of q on the database file at path,
// read the way the product reads it: a fresh connection to a database
// that only Open has prepared.
func explain(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+q, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		out = append(out, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(out, "; ")
}

// planDB fills a collection through one store, then reopens the file with
// Open, which is all the statistics step the product runs, and returns
// a plain connection to it. fill gets the collection and the store.
func planDB(t *testing.T, spec store.CollectionSpec, fill func(col store.Collection)) (*sqlite.Store, *sql.DB) {
	t.Helper()
	ctx := t.Context()
	path := testPath(t)
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	col, err := s.Collection(ctx, spec)
	require.NoError(t, err)
	fill(col)
	require.NoError(t, s.Close())
	s, err = sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return s, db
}

// TestClaimPlan checks the Claim candidate queries, for a collection that
// is mostly terminal rows with none claimable, do not walk records_created
// (JOB-191: 200ms per kick on a 1 GB database).
func TestClaimPlan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db := planDB(t, hookLike, func(col store.Collection) {
		for i := range 1500 {
			st := "done"
			if i%7 == 0 {
				st = "failed"
			}
			_, _, err := col.Put(ctx, store.Record{Key: fmt.Sprint(i), State: st, Value: json.RawMessage(fmt.Sprintf(`{"trigger":"t%d"}`, i%20))})
			require.NoError(t, err)
		}
	})
	texts, args, err := sqlite.ClaimSQL(ctx, s, hookLike, store.RecordFilter{States: []string{"pending", "planned"}})
	require.NoError(t, err)
	require.Len(t, texts, 2, "one query per state")
	for i, q := range texts {
		p := explain(t, db, q, args[i]...)
		require.NotContains(t, p, "records_created", p)
		require.Contains(t, p, "records_state", p)
	}
}

// TestLastFirePlan checks the query LastFire runs, for a trigger with no
// accepted invocation, uses the trigger field index on a database that
// only Open prepared. Without statistics SQLite chose records_created and
// read the whole collection (JOB-191).
func TestLastFirePlan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db := planDB(t, hookLike, func(col store.Collection) {
		for i := range 1500 {
			_, _, err := col.Put(ctx, store.Record{Key: fmt.Sprint(i), State: "done", Value: json.RawMessage(fmt.Sprintf(`{"trigger":"t%d"}`, i%20))})
			require.NoError(t, err)
		}
	})
	q, args, err := sqlite.ListSQL(ctx, s, hookLike, store.RecordFilter{
		Where: map[string]string{"trigger": "never-fired"}, States: []string{"done", "planned", "failed"},
		Order: store.OrderCreatedDesc, Limit: 1,
	})
	require.NoError(t, err)
	p := explain(t, db, q, args...)
	require.Contains(t, p, "ri_hook_like_trigger", p)
}

// TestTicketMetaPlan checks a List by meta or label reads the matching
// tickets through the lookup index, not a scan of tickets probing it per
// row. With statistics SQLite planned the correlated EXISTS form as that
// scan: 330ms for one trigger's newest ticket on selfdev (JOB-191).
func TestTicketMetaPlan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := testPath(t)
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	var g store.Graph
	for i := range 1500 {
		// Most tickets share one repo, as on a real instance; the trigger
		// names are spread thin.
		meta := fmt.Sprintf(`{"github":{"repo":"o/r%d"},"trigger":{"name":"t%d"}}`, i%50/45, i%40)
		var labels []string
		if i%10 == 0 {
			labels = []string{"plan"}
		}
		g.Tickets = append(g.Tickets, store.NewTicket{Project: "js", Kind: "task", Title: "x", Meta: json.RawMessage(meta), Labels: labels})
	}
	_, err = s.Apply(ctx, "test", g)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, f := range []store.Filter{
		{MetaPack: "trigger", MetaKey: "name", MetaValue: "t3", Order: store.OrderNewest, Limit: 1},
		{MetaPack: "trigger", MetaKey: "name", MetaValue: "never"},
		{MetaPack: "github", MetaKey: "repo", MetaValue: "o/r0", Order: store.OrderNewest, Limit: 1},
		{Kind: "run", MetaPack: "trigger", MetaKey: "name", MetaValue: "t3", Order: store.OrderNewest},
		{Label: "plan", MetaPack: "trigger", MetaKey: "name", MetaValue: "t0"},
		{Label: "plan"},
	} {
		q, args, err := sqlite.TicketListSQL(s, f)
		require.NoError(t, err)
		p := explain(t, db, q, args...)
		require.NotContains(t, p, "SCAN t ", "%+v: %s", f, p)
	}
}

// TestTicketHolderPlan checks a List by holder reads tickets_holder, a
// partial index on non-empty holders. A bound holder does not prove that, so
// without the literal term in the query SQLite walked the priority index
// for a session that holds nothing: 12ms a session on selfdev, per scan
// trigger per tick (#1293).
func TestTicketHolderPlan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := testPath(t)
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	var g store.Graph
	for i := range 1500 {
		prio := i % 5
		nt := store.NewTicket{Project: "js", Kind: "task", Title: "x", Priority: &prio}
		if i%100 == 0 {
			nt.Holder = fmt.Sprintf("s%d", i)
		}
		g.Tickets = append(g.Tickets, nt)
	}
	_, err = s.Apply(ctx, "test", g)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, f := range []store.Filter{
		{Holder: "s-never", Status: "in_progress", Limit: 1},
		{Holder: "s100", Status: "in_progress"},
		{Holder: "s-never", Statuses: []domain.Status{"in_progress", "open"}},
	} {
		q, args, err := sqlite.TicketListSQL(s, f)
		require.NoError(t, err)
		p := explain(t, db, q, args...)
		require.Contains(t, p, "tickets_holder", "%+v: %s", f, p)
	}
}

// TestTicketMetaOrderPlan checks the two meta Lists a trigger runs on a
// quiet tick (#1314), for a trigger that filed many tickets: the newest
// one reads ticket_meta in created_at order with no sort, and the open ones
// start from the open tickets, not from every ticket the trigger filed.
func TestTicketMetaOrderPlan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := testPath(t)
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	g := store.Graph{Close: map[string]string{}}
	for i := range 1500 {
		meta := fmt.Sprintf(`{"trigger":{"name":"t%d"}}`, i%3)
		id := fmt.Sprintf("js-%04d", i)
		kind := domain.Kind("task")
		if i%2 == 0 {
			kind = "batch"
		}
		nt := store.NewTicket{ID: id, Project: "js", Kind: kind, Title: "x", Meta: json.RawMessage(meta)}
		if kind == "batch" {
			nt.Fields = json.RawMessage(fmt.Sprintf(`{"key":"b%d","state":"open"}`, i))
		}
		g.Tickets = append(g.Tickets, nt)
		if i%300 != 1 {
			// Most of a trigger's tickets are closed, as on an instance.
			g.Close[id] = "done"
		}
	}
	_, err = s.Apply(ctx, "test", g)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	newest := store.Filter{MetaPack: "trigger", MetaKey: "name", MetaValue: "t1", Order: store.OrderNewest, Limit: 1}
	q, args, err := sqlite.TicketListSQL(s, newest)
	require.NoError(t, err)
	p := explain(t, db, q, args...)
	require.Contains(t, p, "ticket_meta_newest", p)
	require.NotContains(t, p, "TEMP B-TREE", p)
	require.NotContains(t, p, "SCAN t ", p)

	open := store.Filter{
		MetaPack: "trigger", MetaKey: "name", MetaValue: "t1",
		Kinds: []domain.Kind{"task"}, Statuses: []domain.Status{"open", "in_progress"}, Limit: store.NoLimit,
	}
	q, args, err = sqlite.TicketListSQL(s, open)
	require.NoError(t, err)
	p = explain(t, db, q, args...)
	require.Contains(t, p, "tickets_kind", q+" "+fmt.Sprint(args)+" "+p)
	require.NotContains(t, p, "LIST SUBQUERY", p)
}

// TestTicketMetaOrder: a meta List ordered by created_at returns the same
// tickets as one filtered after the fact, with other filters, a limit, the
// After cursor and a meta change.
func TestTicketMetaOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	s, err := sqlite.Open(ctx, testPath(t), sqlite.Options{Now: func() time.Time { now = now.Add(time.Second); return now }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var ids []string
	for i := range 12 {
		meta := fmt.Sprintf(`{"trigger":{"name":"t%d"},"x":{"k":"v"}}`, i%2)
		pool := "a"
		if i%3 == 0 {
			pool = "b"
		}
		out, err := s.Apply(ctx, "test", store.Graph{Tickets: []store.NewTicket{{Project: "js", Kind: "task", Title: fmt.Sprint(i), Pool: pool, Meta: json.RawMessage(meta)}}})
		require.NoError(t, err)
		ids = append(ids, out[0].ID)
	}
	titles := func(ts []domain.Ticket) []string {
		var out []string
		for _, tk := range ts {
			out = append(out, tk.Title)
		}
		return out
	}
	list := func(f store.Filter) []string {
		f.MetaPack, f.MetaKey = "trigger", "name"
		ts, err := s.List(ctx, f)
		require.NoError(t, err)
		return titles(ts)
	}
	require.Equal(t, []string{"10", "8", "6", "4", "2", "0"}, list(store.Filter{MetaValue: "t0", Order: store.OrderNewest}))
	require.Equal(t, []string{"0", "2", "4", "6", "8", "10"}, list(store.Filter{MetaValue: "t0", Order: store.OrderOldest}))
	require.Equal(t, []string{"10", "8"}, list(store.Filter{MetaValue: "t0", Order: store.OrderNewest, Limit: 2}))
	require.Equal(t, []string{"10", "8", "4", "2"}, list(store.Filter{MetaValue: "t0", Pool: "a", Order: store.OrderNewest}))
	require.Equal(t, []string{"6", "0"}, list(store.Filter{MetaValue: "t0", Pool: "b", Order: store.OrderNewest}))
	require.Equal(t, []string{"4", "2", "0"}, list(store.Filter{MetaValue: "t0", Order: store.OrderNewest, After: ids[6]}))
	require.Equal(t, []string{"9"}, list(store.Filter{MetaValue: "t1", Order: store.OrderNewest, Pool: "b", Limit: 1}))
	require.Equal(t, []string{"11"}, list(store.Filter{MetaValue: "t1", Order: store.OrderNewest, Limit: 1, Meta: []store.MetaMatch{{Pack: "x", Key: "k", Value: "v"}}}))

	// An open set: the same answer through the other plan.
	require.Equal(t, 6, len(list(store.Filter{MetaValue: "t0", Kinds: []domain.Kind{"task"}, Statuses: []domain.Status{"open", "in_progress"}})))
	_, err = s.Release(ctx, "test", ids[0], nil, store.Outcome{Close: "done"})
	require.NoError(t, err)
	require.Equal(t, 5, len(list(store.Filter{MetaValue: "t0", Kinds: []domain.Kind{"task"}, Statuses: []domain.Status{"open", "in_progress"}})))
	require.Equal(t, 6, len(list(store.Filter{MetaValue: "t0", Order: store.OrderNewest})))

	// A meta update moves the ticket and keeps its created_at.
	_, err = s.Update(ctx, "test", ids[10], store.Update{Meta: json.RawMessage(`{"trigger":{"name":"t1"}}`)})
	require.NoError(t, err)
	require.Equal(t, []string{"11", "10"}, list(store.Filter{MetaValue: "t1", Order: store.OrderNewest, Limit: 2}))
}
