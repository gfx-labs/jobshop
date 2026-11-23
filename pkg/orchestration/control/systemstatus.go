package control

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// firstTicksLogged is how many ticks after a start log their per-step
// seconds (#1325). Only a slow tick was logged before, so a restart whose
// first ticks stayed under the slow threshold left no numbers to compare.
const firstTicksLogged = 2

// firstTickArgs is the log record of one of the first ticks: its number,
// seconds and one step_<name> value per step, in name order.
func firstTickArgs(n int, seconds float64, steps map[string]float64) []any {
	args := []any{"tick", n, "seconds", strconv.FormatFloat(seconds, 'f', 3, 64)}
	for _, name := range slices.Sorted(maps.Keys(steps)) {
		args = append(args, "step_"+name, strconv.FormatFloat(steps[name], 'f', 4, 64))
	}
	return args
}

func (c *Controller) finishTick(start time.Time) {
	c.statusMu.Lock()
	c.status.LastTick = c.now()
	c.status.TickSeconds = time.Since(start).Seconds()
	c.status.Steps = maps.Clone(c.tickSteps)
	c.ticksDone++
	var first []any
	if c.ticksDone <= firstTicksLogged {
		first = firstTickArgs(c.ticksDone, c.status.TickSeconds, c.tickSteps)
	}
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
	if first != nil {
		c.log.Info("first ticks", first...)
	}
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
