// Package notes finds "Not done" and "Not built" lines in docs/notes that a
// later line of the same note may have resolved (#1362). A note is dated and
// rarely edited, so a "Not done" line goes stale when a later PR builds the
// thing; this lists them so a person need not read every note.
package notes

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding is one line that says something was not done or not built.
type Finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Resolved is true when a later line of the note reads like a
	// follow-up that built it.
	Resolved bool `json:"probably_resolved"`
	// ResolvedLine and ResolvedBy name that later line and what matched.
	ResolvedLine int    `json:"resolved_line,omitempty"`
	ResolvedBy   string `json:"resolved_by,omitempty"`
}

// Report is the output of `jobshop report notes`.
type Report struct {
	Dir      string    `json:"dir"`
	Files    int       `json:"files"`
	Findings []Finding `json:"findings"`
}

var (
	notDone = regexp.MustCompile(`(?i)not (?:done|built)`)
	// done is a follow-up phrase; ref is a PR or issue reference.
	done     = regexp.MustCompile(`(?i)\b(?:built|done) in\b`)
	ref      = regexp.MustCompile(`(?i)(?:#|\bPR\s+#?)(\d+)`)
	ownIssue = regexp.MustCompile(`^(?:JOB-)?(\d+)-`)
	heading  = regexp.MustCompile(`^(#+)\s`)
	listItem = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s`)
	// dated starts a follow-up line ("2026-11-04: built in ..."), which the
	// notes write straight under the item they update.
	dated = regexp.MustCompile(`^\s*\d{4}-\d{2}-\d{2}\b`)
)

// ignoreMark on a finding's own line drops it from the report, for a line
// that only talks about "not done" (a note describing this report).
const ignoreMark = "notes:ignore"

// Scan reads every .md file directly in dir.
func Scan(dir string) (Report, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Dir: dir, Findings: []Finding{}}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		lines, err := readLines(filepath.Join(dir, e.Name()))
		if err != nil {
			return Report{}, err
		}
		rep.Files++
		rep.Findings = append(rep.Findings, scanFile(e.Name(), lines)...)
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return rep, nil
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// scanFile reports the matching lines of one note. A note's own issue
// number comes from its file name (1354-x.md, JOB-83-x.md): a reference to
// it does not show that anything was built.
func scanFile(name string, lines []string) []Finding {
	own := ""
	if m := ownIssue.FindStringSubmatch(name); m != nil {
		own = m[1]
	}
	var out []Finding
	for i := 0; i < len(lines); i++ {
		if !notDone.MatchString(lines[i]) || strings.Contains(lines[i], ignoreMark) {
			continue
		}
		f := Finding{File: name, Line: i + 1, Text: strings.TrimSpace(lines[i])}
		end := blockEnd(lines, i)
		for j := end; j < len(lines); j++ {
			if by := resolves(lines[j], own); by != "" {
				f.Resolved, f.ResolvedLine, f.ResolvedBy = true, j+1, by
				break
			}
		}
		out = append(out, f)
	}
	return out
}

// blockEnd is the index of the first line after the text a finding
// belongs to, so the wrapped rest of a "Not done" paragraph or list item,
// or the list under a "Not done" heading, does not count as a later line.
// A heading's section runs to the next heading of the same or a higher
// level; any other line's block runs to a blank line, a heading, a dated
// follow-up line or, for a list item, the next item.
func blockEnd(lines []string, i int) int {
	if m := heading.FindStringSubmatch(lines[i]); m != nil {
		level := len(m[1])
		for j := i + 1; j < len(lines); j++ {
			if h := heading.FindStringSubmatch(lines[j]); h != nil && len(h[1]) <= level {
				return j
			}
		}
		return len(lines)
	}
	item := listItem.MatchString(lines[i])
	for j := i + 1; j < len(lines); j++ {
		l := lines[j]
		if strings.TrimSpace(l) == "" || heading.MatchString(l) || dated.MatchString(l) || (item && listItem.MatchString(l)) {
			return j
		}
	}
	return len(lines)
}

// resolves returns what in line reads like a follow-up, or "".
func resolves(line, own string) string {
	if m := done.FindString(line); m != "" {
		return m
	}
	for _, m := range ref.FindAllStringSubmatch(line, -1) {
		if m[1] != own {
			return m[0]
		}
	}
	return ""
}

// Text writes the report as plain text, one line per finding.
func (r Report) Text(w io.Writer) error {
	open := 0
	for _, f := range r.Findings {
		state := "open"
		if f.Resolved {
			state = fmt.Sprintf("probably resolved (line %d: %s)", f.ResolvedLine, f.ResolvedBy)
		} else {
			open++
		}
		if _, err := fmt.Fprintf(w, "%s:%d: %s: %s\n", f.File, f.Line, state, f.Text); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%d notes read, %d lines, %d open, %d probably resolved\n",
		r.Files, len(r.Findings), open, len(r.Findings)-open)
	return err
}
