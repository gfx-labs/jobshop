package github_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/integrations/apicall"
	"github.com/gfx-labs/jobshop/pkg/integrations/github"
)

type fakePulls struct {
	prs     map[int]github.Pull
	commits map[string][]int
}

func (f fakePulls) PullRequest(_ context.Context, _ string, n int) (github.Pull, error) {
	pr, ok := f.prs[n]
	if !ok {
		return github.Pull{}, fmt.Errorf("no PR %d", n)
	}
	return pr, nil
}

func (f fakePulls) CommitPulls(_ context.Context, _, sha string) ([]int, error) {
	return f.commits[sha], nil
}

func TestPullReads(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/7":
			_, _ = io.WriteString(w, `{"number":7,"title":"t","merged":true,"merge_commit_sha":"abc","mergeable_state":"dirty","html_url":"u"}`)
		case "/repos/o/r/commits/abc/pulls":
			_, _ = io.WriteString(w, `[{"number":7},{"number":9}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl := github.New(srv.URL, apicall.Static("tok"))

	pr, err := cl.PullRequest(t.Context(), "o/r", 7)
	require.NoError(t, err)
	assert.Equal(t, github.Pull{Number: 7, Title: "t", Merged: true, MergeCommitSHA: "abc", MergeableState: "dirty", HTMLURL: "u"}, pr)

	nums, err := cl.CommitPulls(t.Context(), "o/r", "abc")
	require.NoError(t, err)
	assert.Equal(t, []int{7, 9}, nums)

	_, err = cl.PullRequest(t.Context(), "o/r", 8)
	assert.True(t, github.IsNotFound(err))
}

func TestIsRevert(t *testing.T) {
	t.Parallel()
	assert.True(t, github.IsRevert(github.Pull{Title: `Revert "x"`}))
	assert.True(t, github.IsRevert(github.Pull{Body: "This reverts commit abcdef1."}))
	assert.True(t, github.IsRevert(github.Pull{Body: "Reverts o/r#4"}))
	assert.False(t, github.IsRevert(github.Pull{Title: "fix", Body: "plain"}))
}

func TestRevertedPRByNumber(t *testing.T) {
	t.Parallel()
	const repo = "gfx-labs/jobshop"
	src := fakePulls{prs: map[int]github.Pull{
		5: {Number: 5, Merged: true, MergeCommitSHA: "x"},
		6: {Number: 6, Merged: false},
	}}
	got, err := github.RevertedPR(t.Context(), src, repo, github.Pull{Number: 9, Body: "Reverts gfx-labs/jobshop#5"})
	require.NoError(t, err)
	assert.Equal(t, 5, got.Number)
	got, err = github.RevertedPR(t.Context(), src, repo, github.Pull{Number: 9, Body: "Reverts #6"})
	require.NoError(t, err)
	assert.Zero(t, got.Number, "an unmerged PR was not reverted")
	got, err = github.RevertedPR(t.Context(), src, repo, github.Pull{Number: 9, Body: "Reverts other/repo#5"})
	require.NoError(t, err)
	assert.Zero(t, got.Number, "another repo's PR")
}

func TestRevertedPRByCommit(t *testing.T) {
	t.Parallel()
	src := fakePulls{
		prs: map[int]github.Pull{
			3: {Number: 3, Merged: true, MergeCommitSHA: "AAAA1111bbbb"},
			9: {Number: 9, Merged: true, MergeCommitSHA: "other"},
		},
		commits: map[string][]int{"aaaa111": {9, 3}},
	}
	got, err := github.RevertedPR(t.Context(), src, "o/r", github.Pull{Number: 9, Body: "This reverts commit AAAA111."})
	require.NoError(t, err)
	assert.Equal(t, 3, got.Number, "skips the reverting PR and matches the merge commit prefix")
}

func TestNamesIssue(t *testing.T) {
	t.Parallel()
	gh := github.IssueRef{Repo: "gfx-labs/jobshop", Number: 1251}
	lin := github.IssueRef{Key: "JOB-219"}
	for _, tc := range []struct {
		name string
		p    github.PullSummary
		ref  github.IssueRef
		want bool
	}{
		{"title suffix", github.PullSummary{Number: 5, Title: "fix: x (#1251)"}, gh, true},
		{"branch", github.PullSummary{Number: 5, Head: "issue-1251"}, gh, true},
		{"branch slug", github.PullSummary{Number: 5, Head: "issue-1251-recall"}, gh, true},
		{"branch of a longer number", github.PullSummary{Number: 5, Head: "issue-12510"}, gh, false},
		{"closes", github.PullSummary{Number: 5, Body: "Closes #1251. More."}, gh, true},
		{"fixes qualified", github.PullSummary{Number: 5, Body: "fixes gfx-labs/jobshop#1251"}, gh, true},
		{"part of", github.PullSummary{Number: 5, Body: "Part of #1251"}, gh, true},
		{"longer number", github.PullSummary{Number: 5, Body: "Closes #12510"}, gh, false},
		{"plain mention", github.PullSummary{Number: 5, Body: "see #1251"}, gh, false},
		{"other repo", github.PullSummary{Number: 5, Body: "Closes other/repo#1251"}, gh, false},
		{"linear title", github.PullSummary{Number: 5, Title: "feat: x (JOB-219)"}, lin, true},
		{"linear branch lower case", github.PullSummary{Number: 5, Head: "issue-job-219"}, lin, true},
		{"linear longer key", github.PullSummary{Number: 5, Title: "x (JOB-2190)", Head: "issue-JOB-2191"}, lin, false},
		{"linear body only", github.PullSummary{Number: 5, Body: "Fixes JOB-219"}, lin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := github.NamesIssue(tc.p, tc.ref); got != tc.want {
				t.Fatalf("NamesIssue = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListPulls(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls" || r.URL.Query().Get("state") != "all" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `[
			{"number":3,"title":"a (#1)","body":"Closes #1","html_url":"u3","state":"open","merged_at":null,"head":{"ref":"issue-1"},"user":{"login":"bot"}},
			{"number":2,"title":"b","html_url":"u2","state":"closed","merged_at":"2026-11-01T00:00:00Z","head":{"ref":"x"},"user":{"login":"me"}}]`)
	}))
	defer srv.Close()
	cl := github.New(srv.URL, apicall.Static("tok"))

	got, err := cl.ListPulls(t.Context(), "o/r")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.True(t, got[0].Open)
	assert.False(t, got[0].Merged)
	assert.Equal(t, "issue-1", got[0].Head)
	assert.False(t, got[1].Open)
	assert.True(t, got[1].Merged)
}
