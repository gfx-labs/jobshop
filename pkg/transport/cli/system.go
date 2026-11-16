package cli

import "github.com/gfx-labs/jobshop/pkg/transport/rpc"

type systemCmd struct {
	Status systemStatusCmd `cmd:"" help:"Print the controller's supervisor pass timings as JSON: last_tick, tick_seconds, steps (seconds per step), p95_seconds and samples over the last 60 passes. Needs the admin socket or the pool-list grant; a session without it gets a grant error."`
}

type systemStatusCmd struct{}

func (systemStatusCmd) Run(g *Globals) error {
	return g.call(rpc.MethodSystemStatus, struct{}{}, nil)
}
