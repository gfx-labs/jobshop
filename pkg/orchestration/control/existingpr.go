package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/integrations/github"
	"github.com/gfx-labs/jobshop/pkg/orchestration/threads"
	"github.com/gfx-labs/jobshop/pkg/storage/store"
)

// Existing PR check (JOB-219): two sessions built the same fix because
// nothing showed the second one that a PR for the issue was open or had
// just merged. When an implement step of pr-work (or a prefixed copy, or
// pr-work-linear) is claimed, and again when the step records a PR (a
// "PR <url>" comment, or the close with outcome pr), the controller
// comments the open and merged PRs that name the issue. It only shows:
// nothing here refuses a claim, a comment or a close, and a GitHub
// failure is logged and skipped.

// existingPRTimeout bounds the PR list read, which runs inside the claim,
// comment or close call.
const existingPRTimeout = 20 * time.Second

// maxExistingPRLines bounds the PRs one comment lists.
const maxExistingPRLines = 8

// implementIssue returns the repo and issue of an implement step of a
// pr-work run, and the run. ok is false for any other ticket.
func (c *Controller) implementIssue(ctx context.Context, step domain.Ticket) (run domain.Ticket, ref github.IssueRef, ok bool) {
	if step.Kind != domain.KindStep || step.ParentID == "" {
		return run, ref, false
	}
	sf, err := domain.DecodeFields[domain.StepFields](step)
	if err != nil || sf.Step != "implement" {
		return run, ref, false
	}
	run, err = c.store.Get(ctx, step.ParentID)
	if err != nil {
		return run, ref, false
	}
	rf, err := domain.DecodeFields[domain.RunFields](run)
	if err != nil || (!strings.HasSuffix(rf.Workflow, "pr-work") && !strings.HasSuffix(rf.Workflow, "pr-work-linear")) {
		return run, ref, false
	}
	ref.Repo = metaString(run.Meta, "github", "repo")
	if ref.Repo == "" {
		return run, ref, false
	}
	for _, th := range threads.Implicit(run, nil) {
		switch {
		case th.Linear != "":
			ref.Key = th.Linear
		case !th.PR && ref.Key == "":
			ref.Number = th.Number
		}
	}
	return run, ref, ref.Key != "" || ref.Number > 0
}

// runOwnPRs is the set of "repo#n" PRs the run's tickets link in their
// comments: the PRs its own sessions opened. The controller's comments are
// skipped: they quote the PRs of other sessions.
func (c *Controller) runOwnPRs(ctx context.Context, run domain.Ticket) map[string]bool {
	own := map[string]bool{}
	steps, err := store.Children(ctx, c.store, run.ID)
	if err != nil {
		return own
	}
	for _, t := range append([]domain.Ticket{run}, steps...) {
		cs, err := store.Comments(ctx, c.store, t.ID)
		if err != nil {
			continue
		}
		for _, cm := range cs {
			if cm.Author == "controller" || strings.HasPrefix(cm.Author, threads.GitHubAuthorPrefix) || strings.HasPrefix(cm.Author, threads.LinearAuthorPrefix) {
				continue
			}
			for _, th := range github.PRRefs(cm.Body) {
				own[prKey(th.Repo, th.Number)] = true
			}
		}
	}
	return own
}

// noteExistingPRs comments on implement step the PRs that name its issue.
// atClaim is true at the claim, which comments even when there are none (so
// the agent can tell the check ran); at a PR record it comments only when
// a PR from another session exists.
func (c *Controller) noteExistingPRs(ctx context.Context, step domain.Ticket, atClaim bool) {
	run, ref, ok := c.implementIssue(ctx, step)
	if !ok {
		return
	}
	lister, ok := c.activitySource().(github.PullLister)
	if !ok {
		c.log.DebugContext(ctx, "existing PR check: no PR source", "step", step.ID)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, existingPRTimeout)
	defer cancel()
	pulls, err := lister.ListPulls(ctx, ref.Repo)
	if err != nil {
		c.log.WarnContext(ctx, "existing PR check", "step", step.ID, "repo", ref.Repo, "err", err)
		return
	}
	own := c.runOwnPRs(ctx, run)
	var open, merged []github.PullSummary
	foreignOpen, foreignAny := false, false
	for _, p := range pulls {
		if (!p.Open && !p.Merged) || !github.NamesIssue(p, ref) {
			continue
		}
		if p.Open {
			open = append(open, p)
		} else {
			merged = append(merged, p)
		}
		if !own[prKey(ref.Repo, p.Number)] {
			foreignAny = true
			foreignOpen = foreignOpen || p.Open
		}
	}
	if len(open)+len(merged) == 0 {
		if atClaim {
			c.commentOnce(ctx, step.ID, fmt.Sprintf("Existing PR check: no open or merged PR in %s names %s.", ref.Repo, ref))
		}
		return
	}
	if !atClaim && !foreignAny {
		return
	}
	c.commentOnce(ctx, step.ID, existingPRText(ref, own, open, merged, foreignOpen))
}

// prKey is the key of runOwnPRs.
func prKey(repo string, n int) string { return fmt.Sprintf("%s#%d", strings.ToLower(repo), n) }

// existingPRText is the comment: the first line says whether an open PR
// from another session exists, then one line per PR.
func existingPRText(ref github.IssueRef, own map[string]bool, open, merged []github.PullSummary, foreign bool) string {
	var b strings.Builder
	if foreign {
		fmt.Fprintf(&b, "Open PR from another session already names %s. Read it before you build or push: this may be duplicate work.", ref)
	} else {
		fmt.Fprintf(&b, "Existing PRs name %s (none open from another session).", ref)
	}
	n := 0
	line := func(state string, p github.PullSummary) {
		if n++; n > maxExistingPRLines {
			return
		}
		mine := ""
		if own[prKey(ref.Repo, p.Number)] {
			mine = " (this run)"
		}
		fmt.Fprintf(&b, "\n- %s #%d%s: %s %s", state, p.Number, mine, oneLine(p.Title), p.URL)
	}
	for _, p := range open {
		line("open", p)
	}
	for _, p := range merged {
		line("merged", p)
	}
	if n > maxExistingPRLines {
		fmt.Fprintf(&b, "\n- and %d more", n-maxExistingPRLines)
	}
	return b.String()
}

// oneLine keeps a PR title on one line and short.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		return string(r[:100]) + "..."
	}
	return s
}

// notePRRecorded runs the check after body, a comment on step, linked a PR
// in the run's repo.
func (c *Controller) notePRRecorded(ctx context.Context, step domain.Ticket, body string) {
	if len(github.PRRefs(body)) == 0 {
		return
	}
	c.noteExistingPRs(ctx, step, false)
}
