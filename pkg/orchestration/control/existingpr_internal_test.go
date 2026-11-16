package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/integrations/github"
	"github.com/gfx-labs/jobshop/pkg/orchestration/activity"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
	"github.com/gfx-labs/jobshop/pkg/support/telemetry"
	"github.com/gfx-labs/jobshop/pkg/transport/rpc"
)

// fakePulls is a fake GitHub observe source: the PR list of one repo.
type fakePulls struct {
	*fakeActivity
	pulls []github.PullSummary
	err   error
	calls int
}

func (f *fakePulls) ListPulls(context.Context, string) ([]github.PullSummary, error) {
	f.calls++
	return f.pulls, f.err
}

var _ activity.Source = (*fakePulls)(nil)

// implementStep makes a pr-work run for meta and its implement step.
func implementStep(t *testing.T, c *Controller, workflow, meta string) domain.Ticket {
	t.Helper()
	run := mkTicket(t, c, store.NewTicket{
		Kind: domain.KindRun, Title: "run", Fields: json.RawMessage(`{"workflow":"` + workflow + `","state":"running"}`),
		Meta: json.RawMessage(meta),
	})
	return mkTicket(t, c, store.NewTicket{
		Kind: domain.KindStep, Title: "implement", ParentID: run.ID, Pool: "dev",
		Fields: json.RawMessage(`{"step":"implement","run":"` + run.ID + `"}`),
	})
}

// controllerComments are the controller's comments on id.
func controllerComments(t *testing.T, c *Controller, id string) []string {
	t.Helper()
	cs, err := store.Comments(t.Context(), c.store, id)
	require.NoError(t, err)
	var out []string
	for _, cm := range cs {
		if cm.Author == "controller" {
			out = append(out, cm.Body)
		}
	}
	return out
}

const ghMeta = `{"github":{"repo":"gfx-labs/jobshop","issue":1251}}`

func TestExistingPRsAtClaim(t *testing.T) {
	t.Parallel()
	other := github.PullSummary{Number: 1252, Title: "fix: the thing (#1251)", URL: "https://github.com/gfx-labs/jobshop/pull/1252", Head: "issue-1251", Open: true}
	for _, tc := range []struct {
		name     string
		pulls    []github.PullSummary
		meta     string
		workflow string
		first    string   // prefix of the first line
		contains []string // anywhere in the comment
	}{
		{
			name: "open PR from another session", pulls: []github.PullSummary{other}, meta: ghMeta, workflow: "pr-work",
			first:    "Open PR from another session already names #1251",
			contains: []string{"- open #1252: fix: the thing (#1251) https://github.com/gfx-labs/jobshop/pull/1252"},
		},
		{
			name: "none", pulls: []github.PullSummary{{Number: 9, Title: "unrelated (#12)", Open: true}}, meta: ghMeta, workflow: "pr-work",
			first: "Existing PR check: no open or merged PR in gfx-labs/jobshop names #1251.",
		},
		{
			name: "merged only", meta: ghMeta, workflow: "pr-work",
			pulls: []github.PullSummary{{Number: 1250, Title: "x", Body: "Fixes #1251.", Merged: true}},
			first: "Existing PRs name #1251 (none open from another session).", contains: []string{"- merged #1250: x"},
		},
		{
			name: "closed unmerged is skipped", meta: ghMeta, workflow: "pr-work",
			pulls: []github.PullSummary{{Number: 1249, Title: "x (#1251)"}},
			first: "Existing PR check: no open or merged PR",
		},
		{
			name: "linear key in the branch", workflow: "pr-work-linear",
			meta:  `{"github":{"repo":"gfx-labs/jobshop"},"linear":{"workspace":"gfx-labs","issue":"JOB-219"}}`,
			pulls: []github.PullSummary{{Number: 1300, Title: "other", Head: "issue-JOB-219", Open: true}, {Number: 1301, Title: "fix (JOB-2190)", Open: true}},
			first: "Open PR from another session already names JOB-219", contains: []string{"#1300"},
		},
		{
			name: "prefixed workflow", pulls: []github.PullSummary{other}, meta: ghMeta, workflow: "gfx-pr-work",
			first: "Open PR from another session already names #1251",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, fa := prController(t)
			c.activitySrc = &fakePulls{fakeActivity: fa, pulls: tc.pulls}
			step := implementStep(t, c, tc.workflow, tc.meta)

			_, err := c.claim(t.Context(), scope{actor: "s1", session: "s1", pool: "dev"}, rpc.ClaimParams{ID: step.ID})
			require.NoError(t, err)

			got := controllerComments(t, c, step.ID)
			require.Len(t, got, 1)
			assert.True(t, strings.HasPrefix(got[0], tc.first), got[0])
			for _, s := range tc.contains {
				assert.Contains(t, got[0], s)
			}
			if strings.HasPrefix(tc.first, "Open PR") {
				assert.Equal(t, 1, strings.Count(strings.Split(got[0], "\n")[0], "Open PR"), "the first line carries the warning")
			}
		})
	}
}

func TestExistingPRsIgnored(t *testing.T) {
	t.Parallel()
	c, fa := prController(t)
	src := &fakePulls{fakeActivity: fa, pulls: []github.PullSummary{{Number: 1252, Title: "x (#1251)", Open: true}}}
	c.activitySrc = src

	// Not an implement step, not a pr-work run, a read error: no comment, no failure.
	run := mkTicket(t, c, store.NewTicket{
		Kind: domain.KindRun, Title: "run", Fields: json.RawMessage(`{"workflow":"pr-work","state":"running"}`), Meta: json.RawMessage(ghMeta),
	})
	route := mkTicket(t, c, store.NewTicket{
		Kind: domain.KindStep, Title: "route", ParentID: run.ID, Pool: "dev",
		Fields: json.RawMessage(`{"step":"route","run":"` + run.ID + `"}`),
	})
	other := implementStep(t, c, "ci-repair", ghMeta)
	failing := implementStep(t, c, "pr-work", ghMeta)
	src.err = errors.New("rate limited")

	for _, id := range []string{route.ID, other.ID, failing.ID} {
		_, err := c.claim(t.Context(), scope{actor: "s1", session: "s1", pool: "dev"}, rpc.ClaimParams{ID: id})
		require.NoError(t, err, "the check never blocks a claim")
		assert.Empty(t, controllerComments(t, c, id))
	}
}

func TestExistingPRsAtPRRecord(t *testing.T) {
	t.Parallel()
	c, fa := prController(t)
	mine := github.PullSummary{Number: 1260, Title: "mine (#1251)", URL: "https://github.com/gfx-labs/jobshop/pull/1260", Open: true}
	src := &fakePulls{fakeActivity: fa, pulls: []github.PullSummary{mine}}
	c.activitySrc = src
	step := implementStep(t, c, "pr-work", ghMeta)
	sc := scope{actor: "s1", session: "s1", pool: "dev"}

	_, err := c.claim(t.Context(), sc, rpc.ClaimParams{ID: step.ID})
	require.NoError(t, err)
	got := controllerComments(t, c, step.ID)
	require.Len(t, got, 1, "the claim found a PR already: it is another session's")
	assert.True(t, strings.HasPrefix(got[0], "Open PR from another session"))

	// The step records its own PR; the only PR that names the issue is now its own.
	src.pulls = []github.PullSummary{mine}
	_, err = c.comment(t.Context(), sc, rpc.CommentParams{ID: step.ID, Body: "PR https://github.com/gfx-labs/jobshop/pull/1260"})
	require.NoError(t, err)
	assert.Len(t, controllerComments(t, c, step.ID), 1, "own PR only: nothing new at the PR record")

	// Another session's PR appears before the push is recorded.
	rival := github.PullSummary{Number: 1261, Title: "rival (#1251)", URL: "https://github.com/gfx-labs/jobshop/pull/1261", Head: "issue-1251-b", Open: true}
	src.pulls = []github.PullSummary{mine, rival}
	_, err = c.comment(t.Context(), sc, rpc.CommentParams{ID: step.ID, Body: "no PR link here"})
	require.NoError(t, err)
	assert.Len(t, controllerComments(t, c, step.ID), 1, "a comment without a PR link does not read GitHub")
	calls := src.calls
	_, err = c.comment(t.Context(), sc, rpc.CommentParams{ID: step.ID, Body: "PR https://github.com/gfx-labs/jobshop/pull/1260 head abc"})
	require.NoError(t, err)
	assert.Equal(t, calls+1, src.calls)
	got = controllerComments(t, c, step.ID)
	require.Len(t, got, 2)
	assert.True(t, strings.HasPrefix(got[1], "Open PR from another session already names #1251"), got[1])
	assert.Contains(t, got[1], "- open #1260 (this run)")
	assert.Contains(t, got[1], "- open #1261:")

	// The close with outcome pr checks again; an unchanged answer adds nothing.
	_, err = c.closeTicket(t.Context(), sc, rpc.CloseParams{Resolution: "done", Outcome: "pr", ID: step.ID})
	require.NoError(t, err)
	assert.Len(t, controllerComments(t, c, step.ID), 2)
}

// prController is an activity controller with the metrics claims and
// closes count into.
func prController(t *testing.T) (*Controller, *fakeActivity) {
	t.Helper()
	c, _, fa := activityController(t)
	m, err := telemetry.NewMetricsFor(nil)
	require.NoError(t, err)
	c.metrics = m
	return c, fa
}
