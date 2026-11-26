package sqlite

import (
	"context"
	"database/sql"

	"github.com/nullism/bqb"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// ClosedSessions lists every closed session ticket's id and whether its
// fields name an ended state. The first scan_sessions after a start
// listed the whole tickets for this, labels query included (#1361).
func (s *Store) ClosedSessions(ctx context.Context) ([]store.ClosedSession, error) {
	out := []store.ClosedSession{}
	err := s.each(ctx, bqb.New("SELECT id, json_extract(fields, '$.ended') IS NOT NULL FROM tickets "+
		"WHERE kind = ? AND status = ? ORDER BY id", string(domain.KindSession), string(domain.StatusClosed)),
		func(r *sql.Rows) error {
			var c store.ClosedSession
			if err := r.Scan(&c.ID, &c.Ended); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}
