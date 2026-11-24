package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
	"github.com/gfx-labs/jobshop/pkg/storage/store/storetest"
)

func ftsDoc(summary string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"summary": summary})
	return b
}

func ftsCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(q, args...).Scan(&n))
	return n
}

func searchKeys(t *testing.T, c store.Collection, q string) []string {
	t.Helper()
	recs, err := c.Search(t.Context(), q, 10)
	require.NoError(t, err)
	var keys []string
	for _, r := range recs {
		keys = append(keys, r.Key)
	}
	return keys
}

// TestCollectionFTSKeymap (#1338): a document is replaced and removed by
// rowid through the key map, never by a scan of the FTS table on its
// UNINDEXED key, and the map follows Put, Update and Delete.
func TestCollectionFTSKeymap(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openTest(t, sqlite.Options{})
	col, err := s.Collection(ctx, storetest.TestSpec)
	require.NoError(t, err)
	for i := range 5 {
		_, _, err := col.Put(ctx, store.Record{Key: fmt.Sprint("k", i), Value: ftsDoc(fmt.Sprint("alpha doc", i))})
		require.NoError(t, err)
	}
	rec, err := col.Get(ctx, "k2")
	require.NoError(t, err)
	rec.Value = ftsDoc("bravo only")
	_, err = col.Update(ctx, rec)
	require.NoError(t, err)

	assert.Equal(t, []string{"k2"}, searchKeys(t, col, "bravo"))
	assert.NotContains(t, searchKeys(t, col, "doc2"), "k2")
	assert.Len(t, searchKeys(t, col, "alpha"), 4)

	check := func(want int) {
		t.Helper()
		db := s.DB()
		assert.Equal(t, want, ftsCount(t, db, "SELECT count(*) FROM fts_test_items"))
		assert.Equal(t, want, ftsCount(t, db, "SELECT count(*) FROM fts_test_items_keymap"))
		assert.Equal(t, want, ftsCount(t, db, "SELECT count(*) FROM fts_test_items_keymap m JOIN fts_test_items f ON f.rowid = m.id AND f.key = m.key"))
	}
	check(5)
	_, err = col.Delete(ctx, store.RecordFilter{Keys: []string{"k0", "k1"}})
	require.NoError(t, err)
	check(3)
	assert.Empty(t, searchKeys(t, col, "doc1"))
}

// TestCollectionFTSKeymapBackfill (#1338): a database whose FTS documents
// predate the key map gets it filled when the collection registers, and
// the first Update after that replaces the old document.
func TestCollectionFTSKeymapBackfill(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := testPath(t)
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	col, err := s.Collection(ctx, storetest.TestSpec)
	require.NoError(t, err)
	for i := range 3 {
		_, _, err := col.Put(ctx, store.Record{Key: fmt.Sprint("k", i), Value: ftsDoc(fmt.Sprint("old text", i))})
		require.NoError(t, err)
	}
	_, err = s.DB().ExecContext(ctx, "DROP TABLE fts_test_items_keymap")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = sqlite.Open(ctx, path, sqlite.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	col, err = s.Collection(ctx, storetest.TestSpec)
	require.NoError(t, err)
	assert.Equal(t, 3, ftsCount(t, s.DB(), "SELECT count(*) FROM fts_test_items_keymap"))
	rec, err := col.Get(ctx, "k1")
	require.NoError(t, err)
	rec.Value = ftsDoc("new words")
	_, err = col.Update(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{"k1"}, searchKeys(t, col, "new"))
	assert.Len(t, searchKeys(t, col, "old"), 2)
	assert.Equal(t, 3, ftsCount(t, s.DB(), "SELECT count(*) FROM fts_test_items"))
}

// TestWriterCallsNameCollection (#1338): the writer log names the
// collection and key of a collection write, so a long hold says which data
// it was for.
func TestWriterCallsNameCollection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openTest(t, sqlite.Options{})
	col, err := s.Collection(ctx, storetest.TestSpec)
	require.NoError(t, err)
	// Every reading of the clock is 20ms later, so each write is kept.
	var ticks atomic.Int64
	base := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	sqlite.SetWriterClock(s, func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * 20 * time.Millisecond) })

	_, _, err = col.Put(store.WithStep(ctx, "run_triggers"), store.Record{Key: "k1", Value: ftsDoc("x")})
	require.NoError(t, err)
	var got *sqlite.WriterCall
	for _, c := range s.WriterCalls() {
		if c.Key == "k1" {
			got = &c
		}
	}
	require.NotNil(t, got)
	assert.Equal(t, "test_items", got.Collection)
	assert.Equal(t, "(*collection).Put", got.Op)
	assert.Equal(t, "run_triggers/(*collection).Put[test_items]", got.Label())
}
