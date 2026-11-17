package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gfx-labs/jobshop/pkg/execution/automation"
	"github.com/gfx-labs/jobshop/pkg/integrations/apicall"
	gh "github.com/gfx-labs/jobshop/pkg/integrations/github"
)

type fakePR struct {
	draft  bool
	labels []string
}

// TestNoMergeSkipsHeldPRs runs packs/github's no_merge scan script against
// a fake GitHub (JOB-223): only PRs that are real work count as open.
func TestNoMergeSkipsHeldPRs(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		commit time.Time
		prs    []fakePR
		found  bool
	}{
		{"only a release PR", now.Add(-7 * time.Hour), []fakePR{{labels: []string{"autorelease: pending"}}}, false},
		{"release label case differs", now.Add(-7 * time.Hour), []fakePR{{labels: []string{"Autorelease: Pending"}}}, false},
		{"normal PR", now.Add(-7 * time.Hour), []fakePR{{}}, true},
		{"release and normal PR", now.Add(-7 * time.Hour), []fakePR{{labels: []string{"autorelease: pending"}}, {}}, true},
		{"only a draft", now.Add(-7 * time.Hour), []fakePR{{draft: true}}, false},
		{"only a held PR", now.Add(-7 * time.Hour), []fakePR{{labels: []string{"hold"}}}, false},
		{"no PRs", now.Add(-7 * time.Hour), nil, false},
		{"recent commit", now.Add(-time.Hour), []fakePR{{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/commits/master":
					writeJSON(t, w, map[string]any{"sha": "0123456789abcdef", "commit": map[string]any{"committer": map[string]any{"date": tc.commit.Format(time.RFC3339)}}})
				case "/graphql":
					nodes := []any{}
					for i, pr := range tc.prs {
						labels := []any{}
						for _, l := range pr.labels {
							labels = append(labels, map[string]any{"name": l})
						}
						nodes = append(nodes, map[string]any{
							"number": i + 1, "isDraft": pr.draft, "updatedAt": now.Format(time.RFC3339),
							"author":  map[string]any{"login": "dev"},
							"labels":  map[string]any{"nodes": labels},
							"commits": map[string]any{"nodes": []any{}},
						})
					}
					writeJSON(t, w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"nodes": nodes}}}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			engine, err := automation.Load(t.Context(), "../../../../packs/github", []string{"triggers.star:no_merge"}, automation.Options{
				GitHub: func(context.Context, string) (*gh.Client, error) {
					return gh.New(server.URL, apicall.Static("tok")), nil
				},
				Now: func(context.Context) time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"repo": "o/r", "ci_branch": "master", "skip_labels": "hold", "release_pr_label": "autorelease: pending"}
			res, err := engine.Invoke(t.Context(), "triggers.star:no_merge", args)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.found {
				if len(res.Output) != 0 {
					t.Fatalf("want no finding, got %v", res.Output)
				}
				return
			}
			if got := res.Output["0123456789ab"]; got == nil || !strings.HasPrefix(got.(string), "last commit ") {
				t.Fatalf("want finding for the commit, got %v", res.Output)
			}
		})
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}
