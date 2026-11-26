package sqlite_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

// BenchmarkListDB times Store.List on a copy of a real database (#1372).
// Opt in with JOBSHOP_LIST_DB=<path to a copy; it is opened read-write and
// migrated>. Not part of CI:
//
//	JOBSHOP_LIST_DB=/tmp/x/jobshop.db go test ./pkg/storage/store/sqlite \
//	  -run '^$' -bench ListDB -benchtime 20x -cpuprofile cpu.out -memprofile mem.out
func BenchmarkListDB(b *testing.B) {
	path := os.Getenv("JOBSHOP_LIST_DB")
	if path == "" {
		b.Skip("JOBSHOP_LIST_DB not set")
	}
	s, err := sqlite.Open(b.Context(), path, sqlite.Options{})
	require.NoError(b, err)
	b.Cleanup(func() { _ = s.Close() })

	filters := map[string]store.Filter{
		"closed-sessions": {Kind: domain.KindSession, Status: domain.StatusClosed, Limit: -1},
		"steps":           {Kind: domain.KindStep, Limit: -1},
	}
	for name, f := range filters {
		b.Run("list/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ts, err := s.List(b.Context(), f)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(ts)), "rows")
			}
		})
	}

	// The same rows through raw SQL, scanning every column into a
	// pre-allocated set of destinations: the floor List is measured against.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	require.NoError(b, err)
	b.Cleanup(func() { _ = db.Close() })
	const cols = "t.id, t.project, t.kind, t.title, t.body, t.status, t.resolution, t.priority, t.pool, " +
		"t.holder, t.lease_until, t.fence, t.parent_id, t.defer_until, t.ephemeral, t.fields, t.meta, " +
		"t.revision, t.created_by, t.created_at, t.updated_at, t.closed_at, t.f_key, t.f_to, t.f_state, " +
		"t.f_workflow, t.f_index, t.f_waiting"
	b.Run("raw/closed-sessions", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			rows, err := db.QueryContext(b.Context(), "SELECT "+cols+
				" FROM tickets t WHERE t.kind = ? AND t.status IN (?) ORDER BY t.priority, t.created_at, t.id",
				"session", "closed")
			if err != nil {
				b.Fatal(err)
			}
			var (
				id, project, kind, title, body, status, resolution, pool, holder, createdBy string
				parent, fkey, fto, fstate, fworkflow                                        sql.NullString
				fields, meta                                                                any
				prio, lease, fence, deferUntil, eph, rev, cat, uat, clat, fwaiting          int64
				findex                                                                      sql.NullInt64
			)
			n := 0
			for rows.Next() {
				if err := rows.Scan(&id, &project, &kind, &title, &body, &status, &resolution, &prio, &pool,
					&holder, &lease, &fence, &parent, &deferUntil, &eph, &fields, &meta,
					&rev, &createdBy, &cat, &uat, &clat, &fkey, &fto, &fstate, &fworkflow, &findex, &fwaiting); err != nil {
					b.Fatal(err)
				}
				n++
			}
			if err := rows.Close(); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(n), "rows")
		}
	})
}
