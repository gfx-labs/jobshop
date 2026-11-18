package triggers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gfx-labs/jobshop/pkg/config"
	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// maxPasses bounds how many of a trigger's past tickets backoff reads.
const maxPasses = 16

// changeLimit bounds the tickets read per window. A window holds at most a
// few of the trigger's own tickets, so one other ticket is always in the
// first page when there is any.
const changeLimit = 50

// cooldownFor is the cooldown a trigger with cooldown_max waits after its
// newest ticket closed (JOB-229). A pass is a ticket the trigger filed;
// its window runs from its creation to the creation of the next one (for
// the newest, to now). A pass is idle when no ticket of anyone else was
// created or closed in its window. The cooldown doubles for each idle
// pass in a row, newest first, up to cooldown_max, and is the plain
// cooldown when the newest pass was not idle: the store changed since, so
// planning again is worth it. The state is derived from the store on each
// call: nothing to persist, and a restart or a replay gives the same
// answer.
func (e *Engine) cooldownFor(ctx context.Context, name string, tr config.Trigger, base time.Time) (time.Duration, error) {
	cd := config.Duration(tr.Cooldown, 0)
	limit := config.Duration(tr.CooldownMax, 0)
	if limit <= cd || cd <= 0 || base.IsZero() {
		return cd, nil
	}
	now := e.host.Now()
	// Past the longest wait nothing can hold: skip the reads.
	if !now.Before(base.Add(limit)) {
		return cd, nil
	}
	// Still inside the plain cooldown: held either way.
	if now.Before(base.Add(cd)) {
		return cd, nil
	}
	own, err := e.host.Store().List(ctx, store.Filter{
		MetaPack: Pack, MetaKey: "name", MetaValue: name,
		Order: store.OrderNewest, Limit: maxPasses,
	})
	if err != nil {
		return 0, err
	}
	d, end := cd, now
	for _, t := range own {
		if d >= limit {
			break
		}
		changed, err := e.changedIn(ctx, name, t.CreatedAt, end)
		if err != nil {
			return 0, err
		}
		if changed {
			break
		}
		d *= 2
		end = t.CreatedAt
	}
	return min(d, limit), nil
}

// changedIn reports whether a ticket that the trigger did not file was
// created, or closed, in [from, to).
func (e *Engine) changedIn(ctx context.Context, name string, from, to time.Time) (bool, error) {
	s := e.host.Store()
	created, err := s.List(ctx, store.Filter{
		Created: &store.TimeRange{Since: from, Until: to},
		Order:   store.OrderNewest, Limit: changeLimit,
	})
	if err != nil {
		return false, err
	}
	for _, t := range created {
		if !filedBy(t, name) {
			return true, nil
		}
	}
	closed, err := s.List(ctx, store.Filter{
		Status: domain.StatusClosed, Since: from,
		Order: store.OrderNewest, Limit: changeLimit,
	})
	if err != nil {
		return false, err
	}
	for _, t := range closed {
		if !filedBy(t, name) && !t.ClosedAt.Before(from) && t.ClosedAt.Before(to) {
			return true, nil
		}
	}
	return false, nil
}

// filedBy reports whether t carries meta.trigger.name == name.
func filedBy(t domain.Ticket, name string) bool {
	var m struct {
		Trigger struct {
			Name string `json:"name"`
		} `json:"trigger"`
	}
	return json.Unmarshal(t.Meta, &m) == nil && m.Trigger.Name == name
}
