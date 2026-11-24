package flaky

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// flagVal returns the argument after flag, or "".
func flagVal(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// fakeGh serves the gh calls of Report from testdata:
//   - run list --status failure: runs.json
//   - run list --status completed: run-list-completed.json
//   - run download: failed-tests-<id>.json as the artifact; a missing file
//     is a run without the artifact
//   - run view <id> --json jobs [--attempt n]: run-view-<id>[-<n>].json
//   - run view <id> --job <job> --log-failed: log-<job>.txt
//
// A missing file is a gh error.
func fakeGh(_ context.Context, args ...string) ([]byte, error) {
	switch args[1] {
	case "list":
		if flagVal(args, "--status") == "completed" {
			return os.ReadFile("testdata/run-list-completed.json")
		}
		return os.ReadFile("testdata/runs.json")
	case "view":
		id := args[2]
		if job := flagVal(args, "--job"); job != "" {
			return os.ReadFile(filepath.Join("testdata", "log-"+job+".txt"))
		}
		name := "run-view-" + id
		if a := flagVal(args, "--attempt"); a != "" {
			name += "-" + a
		}
		return os.ReadFile(filepath.Join("testdata", name+".json"))
	}
	id, dir := args[2], args[len(args)-1]
	b, err := os.ReadFile(filepath.Join("testdata", "failed-tests-"+id+".json"))
	if err != nil {
		return nil, errors.New("no valid artifacts found to download")
	}
	return nil, os.WriteFile(filepath.Join(dir, "failed-tests.json"), b, 0o600)
}

func report(t *testing.T) Report {
	t.Helper()
	rep, err := GH{Repo: "o/r", Branch: "master", Gh: fakeGh}.Report(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestReportFromFixture(t *testing.T) {
	rep := report(t)
	if !reflect.DeepEqual(rep.NoArtifact, []int64{3}) {
		t.Fatalf("no_artifact %v", rep.NoArtifact)
	}
	type row struct {
		test, verdict string
		flaky, broken int
		runs          []int64
	}
	var got []row
	for _, g := range rep.Tests {
		got = append(got, row{g.Test, g.Verdict, g.Flaky, g.Broken, g.Runs})
	}
	want := []row{
		{"TestA", Broken, 1, 1, []int64{2, 1}},
		{"TestB", Flaky, 1, 0, []int64{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// A failure in a job other than go test shows its job, step and panic line;
// a cancelled job is not a failure.
func TestReportNonTestJob(t *testing.T) {
	rep := report(t)
	var run Run
	for _, r := range rep.Runs {
		if r.ID == 3 {
			run = r
		}
	}
	if len(run.FailedJobs) != 1 {
		t.Fatalf("failed jobs %+v", run.FailedJobs)
	}
	j := run.FailedJobs[0]
	if j.ID != 301 || j.Name != "e2e" || j.Step != "Run Desk e2e" || j.Conclusion != "failure" || j.URL != "https://example.com/3/job/301" {
		t.Fatalf("job %+v", j)
	}
	if want := "panic: runtime error: invalid memory address or nil pointer dereference [recovered]"; j.Error != want {
		t.Fatalf("error %q want %q", j.Error, want)
	}
	// A go test failure has its first FAIL line.
	for _, r := range rep.Runs {
		if r.ID == 1 && r.FailedJobs[0].Error != "--- FAIL: TestA (0.01s)" {
			t.Fatalf("run 1 error %q", r.FailedJobs[0].Error)
		}
	}
}

// A run that passed on rerun, one on a PR branch, one still failing, and a
// single-attempt run that is skipped.
func TestReportReruns(t *testing.T) {
	rep := report(t)
	type row struct {
		id         int64
		branch     string
		attempt    int
		conclusion string
		jobs       []string
	}
	var got []row
	for _, r := range rep.Reruns {
		var jobs []string
		for _, j := range r.FailedJobs {
			jobs = append(jobs, j.Name+"/"+j.Step)
			if j.Attempt < 1 || j.Attempt >= r.Attempt {
				t.Errorf("run %d job %d attempt %d of %d", r.ID, j.ID, j.Attempt, r.Attempt)
			}
		}
		got = append(got, row{r.ID, r.Branch, r.Attempt, r.Conclusion, jobs})
	}
	want := []row{
		{13, "feat/y", 3, "failure", []string{"lint/golangci-lint", "e2e/Run Desk e2e"}},
		{11, "feat/x", 2, "success", []string{"e2e/Run Desk e2e"}},
		{10, "master", 2, "success", []string{"e2e/Run Desk e2e"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestReportJobGroups(t *testing.T) {
	rep := report(t)
	if len(rep.Jobs) != 3 {
		t.Fatalf("groups %+v", rep.Jobs)
	}
	e2e := rep.Jobs[0]
	if e2e.Job != "e2e" || e2e.Step != "Run Desk e2e" || e2e.Count != 4 || e2e.Recovered != 2 ||
		!reflect.DeepEqual(e2e.Runs, []int64{13, 11, 10, 3}) || e2e.Error != "error: page crashed" {
		t.Fatalf("e2e group %+v", e2e)
	}
	test := rep.Jobs[1]
	if test.Job != "test" || test.Step != "Test" || test.Count != 2 || test.Recovered != 0 ||
		!reflect.DeepEqual(test.Runs, []int64{2, 1}) || test.Error != "FAIL\texample.com/a\t0.5s" {
		t.Fatalf("test group %+v", test)
	}
	lint := rep.Jobs[2]
	if lint.Job != "lint" || lint.Count != 1 || lint.Error != "Error: issues found" {
		t.Fatalf("lint group %+v", lint)
	}
}

// A log that cannot be read leaves the error empty and does not fail the
// report; the rest of the run's jobs are still listed.
func TestReportLogReadError(t *testing.T) {
	rep := report(t)
	for _, r := range rep.Reruns {
		if r.ID != 11 {
			continue
		}
		if len(r.FailedJobs) != 1 || r.FailedJobs[0].ID != 1101 || r.FailedJobs[0].Error != "" {
			t.Fatalf("run 11 jobs %+v", r.FailedJobs)
		}
		if !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "--job 1101") }) {
			t.Fatalf("warnings %v", rep.Warnings)
		}
		return
	}
	t.Fatal("run 11 missing")
}

// A run whose jobs gh cannot list keeps its tests and gets a warning.
func TestReportViewError(t *testing.T) {
	gh := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "view" && args[2] == "2" {
			return nil, errors.New("HTTP 502")
		}
		return fakeGh(ctx, args...)
	}
	rep, err := GH{Repo: "o/r", Branch: "master", Gh: gh}.Report(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tests) != 2 || len(rep.Reruns) != 3 {
		t.Fatalf("tests %d reruns %d", len(rep.Tests), len(rep.Reruns))
	}
	if !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "gh run view 2") }) {
		t.Fatalf("warnings %v", rep.Warnings)
	}
}

func TestReportRerunLimit(t *testing.T) {
	var limits []string
	gh := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "list" {
			limits = append(limits, flagVal(args, "--status")+"="+flagVal(args, "--limit"))
		}
		return fakeGh(ctx, args...)
	}
	if _, err := (GH{Repo: "o/r", Branch: "master", RerunLimit: 20, Gh: gh}).Report(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if want := []string{"failure=5", "completed=20"}; !reflect.DeepEqual(limits, want) {
		t.Fatalf("limits %v want %v", limits, want)
	}
}

func TestFirstErrorLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct{ name, log, want string }{
		{"go panic", "a\tb\t2026-01-02T03:04:05.5Z ok\na\tb\t2026-01-02T03:04:05.6Z panic: boom\n", "panic: boom"},
		{"no prefix", "starting\nError: bad\nerror: later\n", "Error: bad"},
		{"truncated", "a\tb\t2026-01-02T03:04:05Z error: " + long + "\n", "error: " + long[:193]},
		{"none", "a\tb\t2026-01-02T03:04:05Z all good\n", ""},
		{"empty", "", ""},
	} {
		if got := FirstErrorLine([]byte(tc.log)); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
