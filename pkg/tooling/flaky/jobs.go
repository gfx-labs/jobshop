package flaky

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxErrorLen caps FailedJob.Error.
const maxErrorLen = 200

// FailedJob is one job of a CI run that did not pass (#1344): the go test
// job, but also e2e, lint, build or nix.
type FailedJob struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Step is the first failed step; empty when gh lists none.
	Step       string `json:"step,omitempty"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	// Attempt is the run attempt the job failed in; set for reruns only.
	Attempt int `json:"attempt,omitempty"`
	// Error is the first panic or error line of the job's failed log; empty
	// when the log has none or could not be read.
	Error string `json:"error,omitempty"`
}

// RerunRun is a completed run that was run more than once, on master or a
// PR branch. FailedJobs are the jobs that failed in its earlier attempts;
// Conclusion is the final one, so success means the failure passed on rerun.
type RerunRun struct {
	ID         int64       `json:"id"`
	SHA        string      `json:"sha"`
	URL        string      `json:"url"`
	Branch     string      `json:"branch"`
	Attempt    int         `json:"attempt"`
	Conclusion string      `json:"conclusion"`
	CreatedAt  time.Time   `json:"created_at"`
	FailedJobs []FailedJob `json:"failed_jobs,omitempty"`
}

// JobGroup is one job and step across the runs it failed in.
type JobGroup struct {
	Job  string `json:"job"`
	Step string `json:"step,omitempty"`
	// Count is the number of failed job instances: failed master runs plus
	// failed earlier attempts. Recovered is how many of them were in a run
	// that finally passed.
	Count     int `json:"count"`
	Recovered int `json:"recovered"`
	// Error is the newest instance's error line that has one.
	Error string    `json:"error,omitempty"`
	Runs  []int64   `json:"runs"`
	Last  time.Time `json:"last"`
}

// GroupJobs groups the failed jobs of failed runs and of rerun runs by job
// and step name, most failures first.
func GroupJobs(runs []Run, reruns []RerunRun) []JobGroup {
	type key struct{ job, step string }
	type inst struct {
		job   FailedJob
		run   int64
		at    time.Time
		saved bool
	}
	var all []inst
	for _, r := range runs {
		for _, j := range r.FailedJobs {
			all = append(all, inst{job: j, run: r.ID, at: r.CreatedAt})
		}
	}
	for _, r := range reruns {
		for _, j := range r.FailedJobs {
			all = append(all, inst{job: j, run: r.ID, at: r.CreatedAt, saved: r.Conclusion == "success"})
		}
	}
	// Newest first, so the first error seen is the latest.
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	byKey := map[key]*JobGroup{}
	for _, in := range all {
		k := key{in.job.Name, in.job.Step}
		g := byKey[k]
		if g == nil {
			g = &JobGroup{Job: k.job, Step: k.step, Last: in.at}
			byKey[k] = g
		}
		g.Count++
		if in.saved {
			g.Recovered++
		}
		if g.Error == "" {
			g.Error = in.job.Error
		}
		if !containsID(g.Runs, in.run) {
			g.Runs = append(g.Runs, in.run)
		}
	}
	out := make([]JobGroup, 0, len(byKey))
	for _, g := range byKey {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Job != out[j].Job {
			return out[i].Job < out[j].Job
		}
		return out[i].Step < out[j].Step
	})
	return out
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// failedJobs lists the jobs of run id that failed, with the first error
// line of each log. attempt 0 is the run's latest attempt.
func (g GH) failedJobs(ctx context.Context, id int64, attempt int) ([]FailedJob, []string, error) {
	args := []string{"run", "view", strconv.FormatInt(id, 10), "-R", g.Repo, "--json", "jobs"}
	if attempt > 0 {
		args = append(args, "--attempt", strconv.Itoa(attempt))
	}
	out, err := g.gh(ctx, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("gh run view %d: %w", id, err)
	}
	var v struct {
		Jobs []struct {
			DatabaseID int64  `json:"databaseId"`
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"url"`
			Steps      []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, nil, fmt.Errorf("gh run view %d: %w", id, err)
	}
	var jobs []FailedJob
	var warns []string
	for _, j := range v.Jobs {
		if !failedConclusion(j.Conclusion) {
			continue
		}
		fj := FailedJob{ID: j.DatabaseID, Name: j.Name, Conclusion: j.Conclusion, URL: j.URL, Attempt: attempt}
		for _, s := range j.Steps {
			if failedConclusion(s.Conclusion) {
				fj.Step = s.Name
				break
			}
		}
		line, err := g.firstError(ctx, id, j.DatabaseID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			warns = append(warns, err.Error())
		}
		fj.Error = line
		jobs = append(jobs, fj)
	}
	return jobs, warns, nil
}

// failedConclusion is a job or step that failed; cancelled and skipped ones
// did not fail on their own.
func failedConclusion(c string) bool { return c == "failure" || c == "timed_out" }

// firstError reads the failed log of a job. A log that cannot be read is an
// error the caller turns into a warning.
func (g GH) firstError(ctx context.Context, run, job int64) (string, error) {
	out, err := g.gh(ctx, "run", "view", strconv.FormatInt(run, 10), "-R", g.Repo,
		"--job", strconv.FormatInt(job, 10), "--log-failed")
	if err != nil {
		return "", fmt.Errorf("gh run view %d --job %d --log-failed: %w", run, job, err)
	}
	return FirstErrorLine(out), nil
}

var errorMarkers = []string{"panic:", "FAIL", "Error:", "error:"}

// FirstErrorLine returns the first line of a gh job log that has a panic or
// error marker, without the "job<TAB>step<TAB>timestamp" prefix gh adds,
// cut to 200 characters.
func FirstErrorLine(log []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := logText(sc.Text())
		for _, m := range errorMarkers {
			if strings.Contains(line, m) {
				return truncate(line, maxErrorLen)
			}
		}
	}
	return ""
}

// logText drops the job and step columns and the timestamp of a gh log line.
func logText(line string) string {
	if parts := strings.SplitN(line, "\t", 3); len(parts) == 3 {
		line = parts[2]
	}
	if ts, rest, ok := strings.Cut(line, " "); ok {
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(ts, "\ufeff")); err == nil {
			line = rest
		}
	}
	return strings.TrimSpace(line)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// rerunRuns lists completed runs of any branch that ran more than once and
// the jobs that failed in their earlier attempts.
func (g GH) rerunRuns(ctx context.Context, limit int) ([]RerunRun, []string, error) {
	args := []string{
		"run", "list", "-R", g.Repo, "--status", "completed", "--limit", strconv.Itoa(limit),
		"--json", "attempt,conclusion,headBranch,headSha,databaseId,url,createdAt",
	}
	if g.Workflow != "" {
		args = append(args, "--workflow", g.Workflow)
	}
	out, err := g.gh(ctx, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("gh run list: %w", err)
	}
	var list []struct {
		Attempt    int       `json:"attempt"`
		Conclusion string    `json:"conclusion"`
		HeadBranch string    `json:"headBranch"`
		HeadSha    string    `json:"headSha"`
		DatabaseID int64     `json:"databaseId"`
		URL        string    `json:"url"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, nil, fmt.Errorf("gh run list: %w", err)
	}
	var reruns []RerunRun
	var warns []string
	for _, l := range list {
		if l.Attempt <= 1 {
			continue
		}
		r := RerunRun{
			ID: l.DatabaseID, SHA: l.HeadSha, URL: l.URL, Branch: l.HeadBranch,
			Attempt: l.Attempt, Conclusion: l.Conclusion, CreatedAt: l.CreatedAt,
		}
		for a := 1; a < l.Attempt; a++ {
			jobs, w, err := g.failedJobs(ctx, r.ID, a)
			warns = append(warns, w...)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				warns = append(warns, err.Error())
				continue
			}
			r.FailedJobs = append(r.FailedJobs, jobs...)
		}
		reruns = append(reruns, r)
	}
	sort.SliceStable(reruns, func(i, j int) bool { return reruns[i].CreatedAt.After(reruns[j].CreatedAt) })
	return reruns, warns, nil
}
