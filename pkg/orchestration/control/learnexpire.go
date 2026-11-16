package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gfx-labs/jobshop/pkg/config"
	"github.com/gfx-labs/jobshop/pkg/domain"
	"github.com/gfx-labs/jobshop/pkg/integrations/github"
	"github.com/gfx-labs/jobshop/pkg/knowledge"
)

// Automatic learning expiry (JOB-220). Housekeeping expires open learnings
// older than learnings.expire_after that have no PR, the same change
// `jobshop learn expire` makes by hand (JOB-198), except those an open
// GitHub issue or PR still cites: the curator names learning ids in the
// issue and PR of a prompt fix, so a cited learning is being worked on.

// learnExpireEvery spaces expiry runs; the age is days.
const learnExpireEvery = time.Hour

// learnRefPages bounds the open issues and PRs read per repo (100 a page).
const learnRefPages = 10

// learnCite finds "learning 12", "learnings #12, #14 and 15", "learning ids
// 12 13". Group 1 is the list of numbers.
var learnCite = regexp.MustCompile(`(?i)\blearnings?(?:\s+ids?)?\s*[:#]?\s*#?(\d+(?:(?:\s*(?:,|&|\band\b)\s*|\s+)#?\d+)*)`)

var digits = regexp.MustCompile(`\d+`)

// citedLearnings returns the learning ids that texts name.
func citedLearnings(texts []string) map[int64]bool {
	out := map[int64]bool{}
	for _, text := range texts {
		for _, m := range learnCite.FindAllStringSubmatch(text, -1) {
			for _, d := range digits.FindAllString(m[1], -1) {
				if id, err := strconv.ParseInt(d, 10, 64); err == nil {
					out[id] = true
				}
			}
		}
	}
	return out
}

// expireLearnings is the housekeeping step. A failed lookup of open issues
// and PRs expires nothing: a learning must not expire because GitHub was
// unreachable. Each run logs the count it expired.
func (c *Controller) expireLearnings(ctx context.Context, cfg *config.Config) {
	after := cfg.LearningExpireAfter()
	if after <= 0 {
		return
	}
	now := c.now()
	if now.Sub(c.learnExpiredAt) < learnExpireEvery {
		return
	}
	c.learnExpiredAt = now
	ls, err := knowledge.ExpireLearnings(ctx, c.store, now.Add(-after), "", "", func(ctx context.Context, cands []domain.Learning) (map[int64]bool, error) {
		texts, err := c.openRefTexts(ctx, cfg, learnRepos(cfg, cands))
		if err != nil {
			return nil, err
		}
		return citedLearnings(texts), nil
	})
	if err != nil {
		if ctx.Err() == nil {
			c.log.WarnContext(ctx, "learning expiry", "err", err)
		}
		return
	}
	if len(ls) > 0 {
		c.log.InfoContext(ctx, "learnings expired", "count", len(ls), "older_than", after, "by", "housekeeping")
	}
}

// learnRepos are the repos whose open issues and PRs may cite a candidate:
// the configured workflow and activity repos, and any repo a candidate is
// scoped to.
func learnRepos(cfg *config.Config, cands []domain.Learning) []string {
	repos := cfg.ActivityRepos()
	for _, l := range cands {
		if r, ok := strings.CutPrefix(l.Scope, "repo:"); ok && config.RepoPattern.MatchString(r) {
			repos = append(repos, r)
		}
	}
	slices.Sort(repos)
	return slices.Compact(repos)
}

func (c *Controller) openRefTexts(ctx context.Context, cfg *config.Config, repos []string) ([]string, error) {
	if c.learnRefs != nil {
		return c.learnRefs(ctx, cfg, repos)
	}
	var out []string
	for _, repo := range repos {
		cl := c.githubOrHost(cfg, "", repo)
		// The issues endpoint lists PRs too.
		items, _, err := cl.List(ctx, github.Request{
			Path:  "/repos/" + repo + "/issues",
			Query: url.Values{"state": {"open"}, "per_page": {"100"}},
			Route: "learn_refs",
		}, learnRefPages)
		if err != nil {
			return nil, fmt.Errorf("open issues of %s: %w", repo, err)
		}
		for _, raw := range items {
			var it struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			if err := json.Unmarshal(raw, &it); err != nil {
				return nil, fmt.Errorf("open issues of %s: %w", repo, err)
			}
			out = append(out, it.Title+"\n"+it.Body)
		}
	}
	return out, nil
}
