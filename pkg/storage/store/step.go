package store

import "context"

type stepKey struct{}

// WithStep returns ctx labeled with the supervisor step that makes the
// store calls below it. A store that records who holds its writer (the
// SQLite one, #1273) names the step in the record, so a slow tick can say
// which step queued and which one held.
func WithStep(ctx context.Context, step string) context.Context {
	return context.WithValue(ctx, stepKey{}, step)
}

// StepOf returns the label WithStep set, or "".
func StepOf(ctx context.Context) string {
	s, _ := ctx.Value(stepKey{}).(string)
	return s
}
