package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// REST reads of one PR and revert detection (JOB-159). Pull is the REST
// view of GET /repos/{r}/pulls/{n}; PRInfo is the gh-CLI view the waits
// read. The two readers stay apart on purpose: Client uses the identity
// token of the repo, CLI uses the host gh token (JOB-143).

// Pull is the part of GET /repos/{r}/pulls/{n} that revert detection and
// the rebase check read.
type Pull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	HTMLURL        string `json:"html_url"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	// MergeableState is GitHub's mergeable_state (dirty, behind, clean,
	// ...), read by the rebase check (#511); see NeedsRebase.
	MergeableState string `json:"mergeable_state"`
}

// PullSource reads PRs. Client implements it for one repo's identity;
// callers that pick a client per repo wrap it, and tests replace it.
type PullSource interface {
	PullRequest(ctx context.Context, repo string, n int) (Pull, error)
	// CommitPulls lists the PRs a commit belongs to.
	CommitPulls(ctx context.Context, repo, sha string) ([]int, error)
}

var _ PullSource = (*Client)(nil)

// pullRoute names these calls in traces. It stays the activity route the
// reads had before they moved here, so dashboards keep their series.
const pullRoute = "/repos/{repo}/activity"

// PullRequest reads PR n of repo.
func (c *Client) PullRequest(ctx context.Context, repo string, n int) (Pull, error) {
	var pr Pull
	resp, err := c.Do(ctx, Request{Path: fmt.Sprintf("/repos/%s/pulls/%d", repo, n), Route: pullRoute})
	if err != nil {
		return Pull{}, err
	}
	return pr, json.Unmarshal(resp.Body, &pr)
}

// CommitPulls lists the PRs a commit of repo is associated with.
func (c *Client) CommitPulls(ctx context.Context, repo, sha string) ([]int, error) {
	resp, err := c.Do(ctx, Request{Path: fmt.Sprintf("/repos/%s/commits/%s/pulls", repo, url.PathEscape(sha)), Route: pullRoute})
	if err != nil {
		return nil, err
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(resp.Body, &prs); err != nil {
		return nil, err
	}
	out := make([]int, len(prs))
	for i, p := range prs {
		out[i] = p.Number
	}
	return out, nil
}

var (
	revertsCommit = regexp.MustCompile(`(?i)this reverts commit ([0-9a-f]{7,40})`)
	// GitHub's revert button writes "Reverts owner/name#123".
	revertsPR = regexp.MustCompile(`(?i)reverts ([\w.-]+/[\w.-]+)?#(\d+)`)
)

// IsRevert reports whether a PR looks like a revert at all.
func IsRevert(pr Pull) bool {
	return strings.HasPrefix(pr.Title, `Revert "`) || revertsCommit.MatchString(pr.Body) || revertsPR.MatchString(pr.Body)
}

// RevertedPR finds the PR that pr reverts, checked against that PR's
// merge commit: a "Reverts #n" or the commit named in "This reverts
// commit <sha>" must be the original's merge commit. Zero means none.
func RevertedPR(ctx context.Context, src PullSource, repo string, pr Pull) (Pull, error) {
	if m := revertsPR.FindStringSubmatch(pr.Body); m != nil && (m[1] == "" || strings.EqualFold(m[1], repo)) {
		n, _ := strconv.Atoi(m[2])
		orig, err := src.PullRequest(ctx, repo, n)
		if err != nil {
			return Pull{}, err
		}
		if orig.Merged && orig.MergeCommitSHA != "" {
			return orig, nil
		}
	}
	m := revertsCommit.FindStringSubmatch(pr.Body)
	if m == nil {
		return Pull{}, nil
	}
	sha := strings.ToLower(m[1])
	nums, err := src.CommitPulls(ctx, repo, sha)
	if err != nil {
		return Pull{}, err
	}
	for _, n := range nums {
		if n == pr.Number {
			continue
		}
		orig, err := src.PullRequest(ctx, repo, n)
		if err != nil {
			return Pull{}, err
		}
		if orig.Merged && strings.HasPrefix(strings.ToLower(orig.MergeCommitSHA), sha) {
			return orig, nil
		}
	}
	return Pull{}, nil
}

// PullSummary is one entry of GET /repos/{r}/pulls: what finding the PRs
// that name an issue reads (JOB-219).
type PullSummary struct {
	Number  int
	Title   string
	Body    string
	URL     string
	Head    string // branch name
	Open    bool
	Merged  bool
	Author  string
	Updated string
}

// PullLister lists a repo's recent PRs, newest update first, open, merged
// and closed alike. Client implements it; tests replace it.
type PullLister interface {
	ListPulls(ctx context.Context, repo string) ([]PullSummary, error)
}

var _ PullLister = (*Client)(nil)

// listPullPages bounds ListPulls: 3 pages of 100 reach back several days of
// merges on a busy repo, which is the window a duplicate happens in.
const listPullPages = 3

// ListPulls reads the most recently updated PRs of repo.
func (c *Client) ListPulls(ctx context.Context, repo string) ([]PullSummary, error) {
	var out []PullSummary
	for page := 1; page <= listPullPages; page++ {
		resp, err := c.Do(ctx, Request{
			Path:  fmt.Sprintf("/repos/%s/pulls", repo),
			Query: url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}},
			Route: pullRoute,
		})
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Number    int    `json:"number"`
			Title     string `json:"title"`
			Body      string `json:"body"`
			HTMLURL   string `json:"html_url"`
			State     string `json:"state"`
			MergedAt  string `json:"merged_at"`
			UpdatedAt string `json:"updated_at"`
			Head      struct {
				Ref string `json:"ref"`
			} `json:"head"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(resp.Body, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, PullSummary{
				Number: r.Number, Title: r.Title, Body: r.Body, URL: r.HTMLURL, Head: r.Head.Ref,
				Open: r.State == "open", Merged: r.MergedAt != "", Author: r.User.Login, Updated: r.UpdatedAt,
			})
		}
		if len(rows) < 100 {
			break
		}
	}
	return out, nil
}

// IssueRef names an issue a PR can work on: a GitHub issue number in Repo,
// or a Linear key.
type IssueRef struct {
	Repo   string
	Number int    // GitHub issue; zero with a Linear key
	Key    string // Linear key such as JOB-219
}

func (r IssueRef) String() string {
	if r.Key != "" {
		return r.Key
	}
	return "#" + strconv.Itoa(r.Number)
}

// closesRef is the body wording that links a PR to an issue. The issue
// number follows, with an optional owner/name.
const closesRef = `(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?|part of)\s*:?\s+`

// NamesIssue reports whether p works on ref. GitHub: the body says
// Closes/Fixes/Resolves/Part of #n, the title ends in (#n), or the branch is
// issue-n or issue-n-slug. Linear: the key is in the title or the branch.
// Closed PRs that were not merged are the caller's to drop.
func NamesIssue(p PullSummary, ref IssueRef) bool {
	if ref.Key != "" {
		re, err := regexp.Compile(`(?i)(?:^|[^a-z0-9])` + regexp.QuoteMeta(ref.Key) + `(?:[^0-9]|$)`)
		return err == nil && (re.MatchString(p.Title) || re.MatchString(p.Head))
	}
	if ref.Number <= 0 || p.Number == ref.Number {
		return false
	}
	n := strconv.Itoa(ref.Number)
	if strings.Contains(p.Title, "(#"+n+")") {
		return true
	}
	if p.Head == "issue-"+n || strings.HasPrefix(p.Head, "issue-"+n+"-") {
		return true
	}
	repo := `(?:` + regexp.QuoteMeta(ref.Repo) + `)?`
	re, err := regexp.Compile(closesRef + repo + `#` + n + `\b`)
	return err == nil && re.MatchString(p.Body)
}
