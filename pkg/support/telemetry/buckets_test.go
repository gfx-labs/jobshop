package telemetry

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every duration histogram has second-based boundaries (#1282): a 40ms
// sample lands in le="0.05", not in a millisecond default bucket.
func TestDurationBuckets(t *testing.T) {
	m, err := NewMetrics()
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()
	ctx := context.Background()
	m.RequestTime.Record(ctx, 0.04)
	m.StepDuration.Record(ctx, 0.04)
	m.TickStep.Record(ctx, 0.04)
	m.Outbound.Record(ctx, 0.04)
	m.LockWait.Record(ctx, 0.04)
	m.Housekeeping.Record(ctx, 0.04)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	out := string(b)
	for _, name := range []string{
		"jobshop_rpc_duration_seconds", "jobshop_step_duration_seconds", "jobshop_tick_step_duration_seconds",
		"jobshop_outbound_duration_seconds", "jobshop_lock_wait_seconds", "jobshop_housekeeping_duration_seconds",
	} {
		bucket := func(le, n string) string {
			return `(?m)^` + name + `_bucket\{[^}]*le="` + le + `"\} ` + n + `$`
		}
		assert.Regexp(t, bucket("0.025", "0"), out, name)
		assert.Regexp(t, bucket("0.05", "1"), out, name)
		assert.NotRegexp(t, bucket("5000", "1"), out, name)
	}
}
