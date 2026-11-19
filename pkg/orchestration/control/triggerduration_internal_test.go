package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestTriggerDurationMetric (#1292): each trigger run in the trigger step
// records a jobshop.trigger.duration sample named by the trigger, timed on
// the host clock.
func TestTriggerDurationMetric(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, _ := offTickController(t, slowTriggerYAML, &log)
	c.store = clockStore{Store: c.store, clk: clk, trigger: "molasses", cost: 3 * time.Second}

	c.testTriggerPass(t)

	rec := httptest.NewRecorder()
	c.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := rec.Body.String()
	assert.Regexp(t, `jobshop_trigger_duration_seconds_count\{[^}]*trigger="molasses"[^}]*\} 1`, out)
	assert.Regexp(t, `jobshop_trigger_duration_seconds_sum\{[^}]*trigger="molasses"[^}]*\} 3\n`, out)
	assert.Regexp(t, `jobshop_trigger_duration_seconds_count\{[^}]*trigger="quick"[^}]*\} 1`, out)
	assert.Regexp(t, `jobshop_trigger_duration_seconds_sum\{[^}]*trigger="quick"[^}]*\} 0\n`, out)
}
