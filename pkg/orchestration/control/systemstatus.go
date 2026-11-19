package control

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

func (c *Controller) finishTick(start time.Time) {
	c.statusMu.Lock()
	c.status.LastTick = c.now()
	c.status.TickSeconds = time.Since(start).Seconds()
	c.status.Steps = maps.Clone(c.tickSteps)
	c.tickSamples = append(c.tickSamples, c.status.TickSeconds)
	if len(c.tickSamples) > 60 {
		c.tickSamples = slices.Clone(c.tickSamples[len(c.tickSamples)-60:])
	}
	samples := slices.Sorted(slices.Values(c.tickSamples))
	c.status.Samples = len(samples)
	c.status.P95Seconds = samples[(len(samples)-1)*95/100]
	status := c.status
	status.Steps = maps.Clone(c.status.Steps)
	c.statusMu.Unlock()
	if c.events != nil {
		c.events.publish([]rpc.Event{{Kind: rpc.EventKindSystem, System: &status, Time: status.LastTick}})
	}
}

// systemStatus answers any caller, admin socket or session of any project
// (#1292). rpc.SystemStatus holds only timings, counts and the names of
// the fixed tick steps: no paths, tickets or tokens. A field that carries
// anything else must be left out for a caller without the pool-list grant.
func (c *Controller) systemStatus(_ context.Context, _ scope, _ struct{}) (any, error) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	out := c.status
	out.Steps = maps.Clone(c.status.Steps)
	return out, nil
}
