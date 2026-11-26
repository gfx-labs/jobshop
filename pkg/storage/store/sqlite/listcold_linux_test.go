package sqlite_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

// dropFileCache asks the kernel to drop the clean cached pages of path
// (posix_fadvise DONTNEED). It needs no root, unlike drop_caches, and only
// affects that file's pages.
func dropFileCache(t testing.TB, path string) {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) { // a closed store checkpoints and removes its WAL
		return
	}
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED))
}

// TestListColdDB times Store.List of closed sessions with a cold page cache
// and then warm, back to back, on a copy of a real database (#1382). It
// skips unless JOBSHOP_LIST_DB names a copy (it is opened read-write and
// migrated). It reads real wall time on purpose and asserts nothing about
// it. JOBSHOP_LIST_RUNS sets the repeats (default 3). Run:
//
//	JOBSHOP_LIST_DB=/tmp/x/jobshop.db go test ./pkg/storage/store/sqlite \
//	  -run TestListColdDB -v -count=1
//
// Each run drops the database and WAL pages from the kernel cache, opens a
// fresh store (so SQLite's own page cache is empty too), then lists twice.
func TestListColdDB(t *testing.T) {
	path := os.Getenv("JOBSHOP_LIST_DB")
	if path == "" {
		t.Skip("JOBSHOP_LIST_DB not set")
	}
	runs := 3
	if v := os.Getenv("JOBSHOP_LIST_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, "JOBSHOP_LIST_RUNS")
		runs = n
	}
	fi, err := os.Stat(path)
	require.NoError(t, err)

	filters := []struct {
		name string
		f    store.Filter
	}{
		{"closed-sessions", store.Filter{Kind: domain.KindSession, Status: domain.StatusClosed, Limit: -1}},
		{"steps", store.Filter{Kind: domain.KindStep, Limit: -1}},
	}
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "db=%s size=%d MiB\n", path, fi.Size()>>20)
	for _, tc := range filters {
		for i := 1; i <= runs; i++ {
			dropFileCache(t, path)
			dropFileCache(t, path+"-wal")
			start := time.Now()
			s, err := sqlite.Open(t.Context(), path, sqlite.Options{})
			require.NoError(t, err)
			open := time.Since(start)
			var d [2]time.Duration
			var rows int
			for j := range d {
				start = time.Now()
				ts, err := s.List(t.Context(), tc.f)
				require.NoError(t, err)
				d[j], rows = time.Since(start), len(ts)
			}
			require.NoError(t, s.Close())
			_, _ = fmt.Fprintf(&out, "%s run %d: rows=%d open=%s cold_list=%s warm_list=%s\n",
				tc.name, i, rows, open.Round(time.Millisecond), d[0].Round(time.Millisecond), d[1].Round(time.Millisecond))
		}
	}
	t.Log("\n" + out.String())
}
