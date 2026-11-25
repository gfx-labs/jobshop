package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/orchestration/control/controltest"
	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

func parse(t *testing.T, args ...string) *root {
	t.Helper()
	var r root
	p, err := kong.New(&r, kong.Exit(func(int) { t.Fatal("exit") }))
	require.NoError(t, err)
	_, err = p.Parse(args)
	require.NoError(t, err)
	return &r
}

func TestRunStartParse(t *testing.T) {
	r := parse(t, "run", "start", "pr-work", "--var", "issue=42", "-v", "x=y", "--key", "sha1")
	assert.Equal(t, rpc.RunStartParams{Workflow: "pr-work", Vars: map[string]string{"issue": "42", "x": "y"}, Key: "sha1"},
		r.Run.Start.params())
	r = parse(t, "run", "resume", "js-1", "--step", "implement")
	assert.Equal(t, rpc.RunResumeParams{ID: "js-1", Step: "implement"}, r.Run.Resume.params())
	r = parse(t, "run", "list", "--state", "needs_human")
	assert.Equal(t, "needs_human", r.Run.List.State)
}

func TestPrintRun(t *testing.T) {
	rf, _ := json.Marshal(domain.RunFields{Workflow: "pr", Vars: map[string]string{"issue": "42", "a": "b"}, Key: "k", State: domain.RunRunning})
	s0, _ := json.Marshal(domain.StepFields{Run: "js-r", Step: "implement", Index: 0, Retries: 3})
	s1, _ := json.Marshal(domain.StepFields{Run: "js-r", Step: "review", Index: 1, Retries: 2, Failures: 1})
	res := rpc.RunResult{
		Run: domain.Ticket{ID: "js-r", Kind: domain.KindRun, Status: domain.StatusOpen, Fields: rf},
		Steps: []domain.Ticket{
			{ID: "js-s0", Kind: domain.KindStep, Pool: "dev", Status: domain.StatusClosed, Resolution: "done", Fields: s0},
			{ID: "js-s1", Kind: domain.KindStep, Pool: "refinery", Status: domain.StatusInProgress, Holder: "js-sess", Fields: s1},
		},
		Existing: true,
	}
	var b bytes.Buffer
	require.NoError(t, printRun(&b, res))
	assert.Equal(t, `run js-r  workflow pr  state running  (open)  key k  [existing run for key]
vars a=b issue=42
0  implement    js-s0  pool dev  closed  done  holder -  failures 0/3
1  review       js-s1  pool refinery  in_progress  -  holder js-sess  failures 1/2
`, b.String())
}

func TestBatchStartParse(t *testing.T) {
	r := parse(t, "batch", "start", "--key", "chat", "--issues", "122,123,125", "--order", "122>123>125", "--order", "122>125")
	assert.Equal(t, rpc.BatchStartParams{Key: "chat", Issues: []string{"122", "123", "125"}, Order: []string{"122>123>125", "122>125"}},
		r.Batch.Start.params())
}

func TestPrintBatch(t *testing.T) {
	bf, _ := json.Marshal(domain.BatchFields{Key: "chat", Issues: []string{"1", "2"}, Order: [][2]string{{"1", "2"}}, State: domain.BatchOpen})
	rf := func(k string) json.RawMessage {
		b, _ := json.Marshal(domain.RunFields{Workflow: "pr-work", Key: k, State: domain.RunRunning})
		return b
	}
	res := rpc.BatchResult{
		Batch: domain.Ticket{ID: "js-b", Kind: domain.KindBatch, Status: domain.StatusOpen, Fields: bf},
		Runs: []rpc.RunResult{
			{Run: domain.Ticket{ID: "js-r1", Kind: domain.KindRun, Fields: rf("issue-1")}},
			{Run: domain.Ticket{ID: "js-r2", Kind: domain.KindRun, Fields: rf("issue-2")}},
		},
		Adopted: []string{"js-r2"},
	}
	var b bytes.Buffer
	require.NoError(t, printBatch(&b, res))
	assert.Equal(t, `batch js-b  key chat  state open  (open)
order 1>2
issue-1  run js-r1  state running
issue-2  run js-r2  state running  [adopted]
`, b.String())
}

// TestPrintBatchShow: batch show prints the current step and which issue
// blocks each member.
func TestPrintBatchShow(t *testing.T) {
	bf, _ := json.Marshal(domain.BatchFields{Key: "chat", Issues: []string{"1", "2"}, State: domain.BatchRunning})
	rf := func(k string) json.RawMessage {
		b, _ := json.Marshal(domain.RunFields{Workflow: "pr-work", Key: k, State: domain.RunRunning})
		return b
	}
	step := func(id, name string, status domain.Status, res string) domain.Ticket {
		b, _ := json.Marshal(domain.StepFields{Step: name})
		return domain.Ticket{ID: id, Kind: domain.KindStep, Status: status, Resolution: res, Fields: b}
	}
	res := rpc.BatchResult{
		Batch: domain.Ticket{ID: "js-b", Kind: domain.KindBatch, Status: domain.StatusOpen, Fields: bf},
		Runs: []rpc.RunResult{
			{Run: domain.Ticket{ID: "js-r1", Fields: rf("issue-1")}, Steps: []domain.Ticket{
				step("s1", "implement", domain.StatusClosed, "done"), step("s2", "review-merge", domain.StatusInProgress, ""),
			}},
			{Run: domain.Ticket{ID: "js-r2", Fields: rf("issue-2")}, Steps: []domain.Ticket{step("s3", "implement", domain.StatusOpen, "")}},
		},
		BlockedBy: map[string][]string{"js-r2": {"1"}},
	}
	var b bytes.Buffer
	require.NoError(t, printBatch(&b, res))
	assert.Equal(t, `batch js-b  key chat  state running  (open)
issue-1  run js-r1  state running  step review-merge
issue-2  run js-r2  state running  step implement  blocked by 1
`, b.String())
}

func TestBatchShowListParse(t *testing.T) {
	r := parse(t, "batch", "show", "chat")
	assert.Equal(t, "chat", r.Batch.Show.ID)
	r = parse(t, "batch", "list", "--project", "js")
	assert.Equal(t, "js", r.Batch.List.Project)
}

// TestPrintRunGraph renders needs, when and outcome (#385).
func TestPrintRunGraph(t *testing.T) {
	rf, _ := json.Marshal(domain.RunFields{Workflow: "g", State: domain.RunRunning})
	s0, _ := json.Marshal(domain.StepFields{Step: "review", Index: 1, Needs: []string{"build"}, Outcome: "approve"})
	when := &domain.StepWhen{Step: "review", Outcome: "approve"}
	s1, _ := json.Marshal(domain.StepFields{Step: "rollout", Index: 2, Needs: []string{"review", "device"}, When: when})
	var buf bytes.Buffer
	require.NoError(t, printRun(&buf, rpc.RunResult{
		Run:   domain.Ticket{ID: "js-r", Fields: rf},
		Steps: []domain.Ticket{{ID: "js-a", Fields: s0}, {ID: "js-b", Fields: s1}},
	}))
	assert.Contains(t, buf.String(), "needs build  outcome approve")
	assert.Contains(t, buf.String(), "needs review,device  when review=approve")
}

// TestPrintRunReviewRounds shows review rounds and an open change
// request (JOB-98).
func TestPrintRunReviewRounds(t *testing.T) {
	rf, _ := json.Marshal(domain.RunFields{Workflow: "g", State: domain.RunRunning})
	s0, _ := json.Marshal(domain.StepFields{Step: "deliver", Changes: &domain.StepChanges{Step: "review", Round: 2}})
	s1, _ := json.Marshal(domain.StepFields{Step: "review", Index: 1, ReviewRounds: 2})
	var buf bytes.Buffer
	require.NoError(t, printRun(&buf, rpc.RunResult{
		Run:   domain.Ticket{ID: "js-r", Fields: rf},
		Steps: []domain.Ticket{{ID: "js-a", Fields: s0}, {ID: "js-b", Fields: s1}},
	}))
	assert.Contains(t, buf.String(), "changes requested by review (round 2)")
	assert.Contains(t, buf.String(), "review rounds 2/3")
}

func TestRunListStateEnum(t *testing.T) {
	assert.Equal(t, "open", parse(t, "run", "list", "-s", "open").Run.List.State)
	assert.True(t, parse(t, "run", "list", "--brief").Run.List.Brief)
	var r root
	p, err := kong.New(&r, kong.Exit(func(int) { t.Fatal("exit") }))
	require.NoError(t, err)
	_, err = p.Parse([]string{"run", "list", "-s", "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

// TestRunListBrief: --brief prints only the six listed fields per run, in
// the same order; the default output keeps the full tickets; -s open drops
// done and cancelled runs (#1353).
func TestRunListBrief(t *testing.T) {
	root := controltest.NewRoot(t, `projects: {js: {}}
agents: {worker: {command: echo}}
pools: {dev: {agent: worker, max_sessions: 0, dir: $ROOT}}
workflows:
  example:
    steps:
      - {id: inspect, pool: dev, prompt: Inspect, title: Inspect}
`)
	controller := controltest.StartWith(t, root)
	var open, cancelled rpc.RunResult
	controller.Call(rpc.MethodRunStart, rpc.RunStartParams{Workflow: "example", Key: "a", Title: "first"}, &open)
	controller.Call(rpc.MethodRunStart, rpc.RunStartParams{Workflow: "example", Key: "b", Title: "second"}, &cancelled)
	controller.Call(rpc.MethodRunCancel, rpc.IDParams{ID: cancelled.Run.ID}, nil)

	old := stdout
	t.Cleanup(func() { stdout = old })
	list := func(args ...string) []map[string]any {
		t.Helper()
		var out bytes.Buffer
		stdout = &out
		require.Equal(t, 0, Main(append([]string{"--socket", root.AdminSocket(), "run", "list"}, args...), nil))
		var rows []map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
		return rows
	}

	full := list()
	require.Len(t, full, 2)
	assert.Contains(t, full[0], "fields", "default output keeps the full ticket")
	assert.Contains(t, full[0], "status")

	brief := list("--brief")
	require.Len(t, brief, 2)
	for _, row := range brief {
		keys := []string{}
		for k := range row {
			keys = append(keys, k)
		}
		assert.ElementsMatch(t, []string{"id", "state", "workflow", "key", "title", "resolution"}, keys)
	}
	assert.Equal(t, map[string]any{
		"id": cancelled.Run.ID, "state": "cancelled", "workflow": "example", "key": "b", "title": "second", "resolution": "cancelled",
	}, brief[0], "newest first")
	assert.Equal(t, map[string]any{
		"id": open.Run.ID, "state": "running", "workflow": "example", "key": "a", "title": "first", "resolution": "",
	}, brief[1])

	live := list("-s", "open", "--brief")
	require.Len(t, live, 1)
	assert.Equal(t, open.Run.ID, live[0]["id"])
	assert.Empty(t, list("-s", "done"))
}
