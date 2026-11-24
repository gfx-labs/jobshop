package control

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/storage/store/sqlite"
)

const quietYAML = `projects:
  js: {}
agents:
  fake: {command: /bin/false}
pools:
  dev: {agent: fake, dir: %[1]q}
`

// writeFile writes name under dir, replacing it.
func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func psi(total int) string {
	return "some avg10=0.00 avg60=0.00 avg300=0.00 total=" + strconv.Itoa(total) + "\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
}

// TestTickProbeReport (JOB-212): the evidence for a slow tick comes from
// the difference of two snapshots, and each candidate cause sets its hint.
func TestTickProbeReport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newTickProbe(time.Second, filepath.Join(dir, "db"), nil)
	p.proc = filepath.Join(dir, "proc")
	p.cgroupDir = filepath.Join(dir, "cg")
	writeFile(t, dir, "db", "x")
	writeFile(t, dir, "db-wal", "xx")
	writeFile(t, dir, "proc/pressure/cpu", psi(1_000_000))
	writeFile(t, dir, "proc/pressure/io", psi(0))
	writeFile(t, dir, "cg/cpu.stat", "nr_periods 10\nnr_throttled 1\nthrottled_usec 5000\n")
	p.pool = func() map[string]sqlite.PoolWait {
		return map[string]sqlite.PoolWait{"writer": {Count: 1, Duration: time.Second}}
	}

	a := p.snap()
	// Quiet: nothing moved, so no hint.
	quiet := p.report(a, a, map[string]float64{"sweep": 0.01})
	assert.Equal(t, []string{"none"}, quiet.Hints)
	assert.Equal(t, "sweep", quiet.SlowestStep)

	// Evidence of a stalled process and a busy writer inside the tick.
	writeFile(t, dir, "proc/pressure/cpu", psi(1_000_000+3_000_000))
	writeFile(t, dir, "cg/cpu.stat", "nr_periods 40\nnr_throttled 20\nthrottled_usec 2005000\n")
	writeFile(t, dir, "db-wal", "xxxxxx")
	p.pool = func() map[string]sqlite.PoolWait {
		return map[string]sqlite.PoolWait{"writer": {Count: 4, Duration: 3 * time.Second}}
	}
	b := p.snap()
	b.at = a.at.Add(7 * time.Second)
	b.dbMod = a.dbMod.Add(time.Second)
	p.addStall(a.at.Add(time.Second), 90*time.Millisecond)
	p.addStall(a.at.Add(-time.Hour), 900*time.Millisecond) // before the tick: not counted
	end := p.houseStart("maintain_store", a.at.Add(time.Second))
	end(a.at.Add(3 * time.Second))
	p.houseStart("compress_journals", a.at.Add(-time.Hour))(a.at.Add(-time.Hour + time.Second)) // earlier

	r := p.report(a, b, map[string]float64{"sweep": 0.01, "supervise": 7.1})
	assert.Equal(t, "supervise", r.SlowestStep)
	assert.Equal(t, 1, r.Stalls)
	assert.Equal(t, 90*time.Millisecond, r.StallMax)
	assert.Equal(t, 3*time.Second, r.PSI["cpu"])
	assert.Equal(t, 2*time.Second, r.Throttled)
	assert.Equal(t, uint64(19), r.ThrottledPeriods)
	assert.Equal(t, int64(4), r.WALDelta)
	assert.True(t, r.DBWritten)
	assert.Equal(t, int64(3), r.Pool["writer"].Count)
	assert.Equal(t, 2*time.Second, r.Pool["writer"].Duration)
	assert.Equal(t, []string{"maintain_store"}, r.Housekeeping)
	assert.ElementsMatch(t, []string{
		"process_stalled", "cpu_pressure", "cgroup_throttled", "db_file_written", "writer_queue", "housekeeping:maintain_store",
	}, r.Hints)
}

// TestTickProbeWriter (#1273): a slow tick names the slowest store call of
// its slowest step and what held the writer, and the longest holders.
func TestTickProbeWriter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newTickProbe(time.Second, filepath.Join(dir, "db"), nil)
	p.proc = filepath.Join(dir, "proc")
	a := p.snap()
	b := a
	b.at = a.at.Add(26 * time.Second)
	at := func(d time.Duration) time.Time { return a.at.Add(d) }
	p.writers = func() []sqlite.WriterCall {
		return []sqlite.WriterCall{
			{Step: "supervise", Op: "Update", Start: at(-time.Hour), Wait: time.Second, Hold: time.Second}, // before the tick
			{Step: "supervise", Op: "Claim", Start: at(0), Hold: 20 * time.Second},
			{Step: "sweep", Op: "Expire", Start: at(time.Second), Wait: 19 * time.Second, Hold: 5 * time.Millisecond, BlockedBy: "supervise/Claim"},
			{Step: "sweep_runs", Op: "Update", Start: at(22 * time.Second), Wait: time.Second, Hold: time.Second},
		}
	}
	r := p.report(a, b, map[string]float64{"sweep": 24.9, "supervise": 20.1, "sweep_runs": 2})
	require.Equal(t, "sweep", r.SlowestStep)
	require.NotNil(t, r.SlowCall)
	assert.Equal(t, "sweep/Expire", r.SlowCall.Label())
	assert.Equal(t, "supervise/Claim", r.SlowCall.BlockedBy)
	assert.Equal(t, []string{"supervise/Claim=20.000", "sweep_runs/Update=1.000", "sweep/Expire=0.005"}, r.Holders)

	var out strings.Builder
	for _, kv := range r.attrs() {
		out.WriteString(string(kv.Key) + "=" + kv.Value.String() + "\n")
	}
	assert.Contains(t, out.String(), "jobshop.tick.slowest_call=sweep/Expire")
	assert.Contains(t, out.String(), "jobshop.tick.slowest_call_wait_seconds=19")
	assert.Contains(t, out.String(), "jobshop.tick.slowest_call_blocked_by=supervise/Claim")
	assert.Contains(t, out.String(), "jobshop.tick.writer_holders=")
	assert.Contains(t, out.String(), "jobshop.tick.longest_hold_call=supervise/Claim")
	assert.Contains(t, out.String(), "jobshop.tick.longest_hold_seconds=20")

	// A slowest step that made no writer call falls back to the slowest call.
	r = p.report(a, b, map[string]float64{"probe_outages": 30})
	require.NotNil(t, r.SlowCall)
	assert.Equal(t, "supervise/Claim", r.SlowCall.Label())
}

// TestSlowTickLogged (JOB-212): a pass at or over the threshold logs one
// "slow tick" record with the hints, and a faster one logs nothing.
func TestSlowTickLogged(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, dir := offTickController(t, quietYAML, &log)
	c.tracer = noop.NewTracerProvider().Tracer("test")
	c.probe = newTickProbe(time.Hour, filepath.Join(dir, "db"), nil)
	c.tickSteps = map[string]float64{}
	c.tick()
	assert.NotContains(t, log.String(), "slow tick")

	c.probe.slow = time.Nanosecond
	c.tick()
	out := log.String()
	assert.Contains(t, out, "slow tick")
	assert.Contains(t, out, "hints=")
	assert.Contains(t, out, "slowest_step=")
}

// clockStore moves the fake clock when the store lists the newest ticket of
// the named trigger: a trigger that takes seconds, with no sleep.
type clockStore struct {
	store.Store
	clk     *scanClock
	trigger string
	cost    time.Duration
}

func (s clockStore) List(ctx context.Context, f store.Filter) ([]domain.Ticket, error) {
	if f.MetaValue == s.trigger && f.Order == store.OrderNewest {
		s.clk.advance(s.cost)
	}
	return s.Store.List(ctx, f)
}

const slowTriggerYAML = quietYAML + `triggers:
  quick:
    when: {cron: '0 6 1 1 *'}
    ticket: {title: quick, pool: dev}
  molasses:
    when: {cron: '0 6 1 1 *'}
    ticket: {title: molasses, pool: dev}
`

// TestSlowTickNamesTrigger (#1288): a slow tick whose slowest step is
// run_triggers names the trigger that took longest, and says nothing of
// triggers when another step was the slowest.
func TestSlowTickNamesTrigger(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, clk, dir := offTickController(t, slowTriggerYAML, &log)
	c.store = clockStore{Store: c.store, clk: clk, trigger: "molasses", cost: 3 * time.Second}
	c.probe = newTickProbe(time.Second, filepath.Join(dir, "db"), nil)
	span := noop.NewTracerProvider().Tracer("test")
	_, sp := span.Start(t.Context(), "tick")

	c.tickSteps = map[string]float64{"run_triggers": 3.2, "sweep": 0.1}
	c.testTriggerPass(t)
	c.endTick(t.Context(), sp, probeSnap{}, 4*time.Second)
	out := log.String()
	assert.Contains(t, out, "slowest_trigger=molasses")
	assert.Contains(t, out, "slowest_trigger_seconds=3")
	assert.NotContains(t, out, "slowest_trigger=quick")

	seen := len(out)
	c.tickSteps = map[string]float64{"run_triggers": 0.1, "sweep": 3}
	c.endTick(t.Context(), sp, probeSnap{}, 4*time.Second)
	next := log.String()[seen:]
	assert.Contains(t, next, "slow tick")
	assert.NotContains(t, next, "slowest_trigger")
}

// TestFirstTicksLogged (#1325): the first two ticks after a start log their
// per-step seconds even when they are quick, and the third does not.
func TestFirstTicksLogged(t *testing.T) {
	t.Parallel()
	var log lockedBuf
	c, _, dir := offTickController(t, quietYAML, &log)
	c.tracer = noop.NewTracerProvider().Tracer("test")
	c.probe = newTickProbe(time.Hour, filepath.Join(dir, "db"), nil)
	c.tickSteps = map[string]float64{}
	for range 3 {
		c.tick()
	}
	out := log.String()
	assert.NotContains(t, out, "slow tick")
	assert.Equal(t, 2, strings.Count(out, "first ticks"), out)
	assert.Contains(t, out, "tick=1 ")
	assert.Contains(t, out, "tick=2 ")
	assert.NotContains(t, out, "tick=3 ")
	assert.Contains(t, out, "step_supervise=")
	assert.Contains(t, out, "step_run_triggers=")
}

// TestTickProbeWriterCollection (#1338): holders of a collection write are
// told apart by collection, and the longest single hold is named with its
// key, so many short Puts adding up to the tick are not read as one hold.
func TestTickProbeWriterCollection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newTickProbe(time.Second, filepath.Join(dir, "db"), nil)
	p.proc = filepath.Join(dir, "proc")
	a := p.snap()
	b := a
	b.at = a.at.Add(10 * time.Second)
	var calls []sqlite.WriterCall
	for i := range 8 {
		calls = append(calls, sqlite.WriterCall{Op: "(*collection).Put", Collection: "inbox", Key: "m", Start: a.at.Add(time.Duration(i) * time.Second), Hold: time.Second})
	}
	calls = append(calls, sqlite.WriterCall{Op: "(*collection).Put", Collection: "journals", Key: "js-s-1", Start: a.at.Add(8 * time.Second), Hold: 1500 * time.Millisecond})
	p.writers = func() []sqlite.WriterCall { return calls }
	r := p.report(a, b, map[string]float64{"run_triggers": 9})
	assert.Equal(t, []string{"(*collection).Put[inbox]=8.000", "(*collection).Put[journals]=1.500"}, r.Holders)
	require.NotNil(t, r.LongestHold)
	assert.Equal(t, "(*collection).Put[journals]", r.LongestHold.Label())
	assert.Equal(t, "js-s-1", r.LongestHold.Key)
}
