package notes

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		// An open line, and a later line with no follow-up.
		"100-open.md": "# Open\n\n- Not done: the retry.\n\nSome closing words.\n",
		// A "Not built" line and a dated follow-up citing another PR.
		"101-resolved.md": "# Resolved\n\nNot built yet: the cache.\n\n2026-11-02: the cache was built in the next change.\n",
		// A reference to another issue after the line.
		"102-ref.md": "# Ref\n\nNot done: the index.\n\n2026-11-03, PR 555 adds it.\n",
		// No such line.
		"103-clean.md": "# Clean\n\nEverything here was finished, see #103.\n",
		// Cites only its own number, before and after: still open.
		"104-own.md": "# Own\n\nSee #104. Not done: the sweep (#104).\n\nTracked in PR #104.\n",
		// A wrapped paragraph and a "Not done" section: lines inside the
		// block do not resolve it; a later section does.
		"105-block.md": "# Block\n\nNot done: the walk, which a\nfuture change (#999) could do.\n\n## Not done\n\n- the prune (#998)\n\n## Later\n\nDone in #997.\n",
		// A file name with no issue number and a non-note file.
		"readme.md":   "NOT BUILT: a thing.\n",
		"skip.txt":    "Not done: ignored.\n",
		"JOB-83-x.md": "## Not done\n\n- profiles\n\nTouched again in #83 only.\n",
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "x.md"), []byte("Not done: nested.\n"), 0o600))
	return dir
}

func TestScan(t *testing.T) {
	t.Parallel()
	rep, err := Scan(fixture(t))
	require.NoError(t, err)
	assert.Equal(t, 8, rep.Files, "the .md files of the directory only")

	type row struct {
		file     string
		line     int
		resolved bool
	}
	var got []row
	for _, f := range rep.Findings {
		got = append(got, row{f.File, f.Line, f.Resolved})
	}
	assert.Equal(t, []row{
		{"100-open.md", 3, false},
		{"101-resolved.md", 3, true},
		{"102-ref.md", 3, true},
		{"104-own.md", 3, false},
		{"105-block.md", 3, true},
		{"105-block.md", 6, true},
		{"JOB-83-x.md", 1, false},
		{"readme.md", 1, false},
	}, got)

	by := map[string]Finding{}
	for _, f := range rep.Findings {
		by[f.File] = f
	}
	assert.Equal(t, 5, by["101-resolved.md"].ResolvedLine)
	assert.Equal(t, "built in", by["101-resolved.md"].ResolvedBy)
	assert.Equal(t, "PR 555", by["102-ref.md"].ResolvedBy)
	assert.Equal(t, "See #104. Not done: the sweep (#104).", by["104-own.md"].Text)
}

// TestScanBlock: the wrapped rest of a paragraph and the list under a
// "Not done" heading are the finding's own text, not a later line.
func TestScanBlock(t *testing.T) {
	t.Parallel()
	rep, err := Scan(fixture(t))
	require.NoError(t, err)
	var para, section Finding
	for _, f := range rep.Findings {
		if f.File != "105-block.md" {
			continue
		}
		if f.Line == 3 {
			para = f
		} else {
			section = f
		}
	}
	assert.Equal(t, 8, para.ResolvedLine, "the paragraph's own #999 does not count; the later section's #998 does")
	assert.Equal(t, 12, section.ResolvedLine, "the section's own list item #998 does not count")
	assert.Equal(t, "Done in", section.ResolvedBy)
}

func TestScanEmptyAndMissing(t *testing.T) {
	t.Parallel()
	rep, err := Scan(t.TempDir())
	require.NoError(t, err)
	b, err := json.Marshal(rep)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"findings":[]`)

	_, err = Scan(filepath.Join(t.TempDir(), "none"))
	require.Error(t, err)
}

func TestReportText(t *testing.T) {
	t.Parallel()
	rep, err := Scan(fixture(t))
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, rep.Text(&buf))
	out := buf.String()
	assert.Contains(t, out, "100-open.md:3: open: - Not done: the retry.\n")
	assert.Contains(t, out, "101-resolved.md:3: probably resolved (line 5: built in): Not built yet: the cache.\n")
	assert.Contains(t, out, "8 notes read, 8 lines, 4 open, 4 probably resolved\n")
}

// TestScanDatedFollowUp: a dated line written straight under an item (a
// list continuation, or the next line of a paragraph) is a later line, and
// a wrapped line that is not dated stays the item's own text (#1386).
func TestScanDatedFollowUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"200-list.md": "- Not built yet (#316): the install dir,\n  wrapped (#317).\n  2026-11-04: built in #316.\n- Not done: other.\n",
		"201-para.md": "Not done: a state.\n2026-11-19: built in #1294.\n",
		"202-open.md": "- Not done: the walk.\n  2026-11-20: measured, not changing it.\n",
		"203-skip.md": "Not done: a report line. <!-- notes:ignore -->\n",
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	rep, err := Scan(dir)
	require.NoError(t, err)
	got := map[string]Finding{}
	for _, f := range rep.Findings {
		got[f.File+":"+string(rune('0'+f.Line))] = f
	}
	require.Len(t, rep.Findings, 4, "the ignored line is not reported")
	assert.True(t, got["200-list.md:1"].Resolved)
	assert.Equal(t, 3, got["200-list.md:1"].ResolvedLine, "the wrapped #317 is own text, the dated line resolves")
	assert.False(t, got["200-list.md:4"].Resolved)
	assert.True(t, got["201-para.md:1"].Resolved)
	assert.False(t, got["202-open.md:1"].Resolved, "a dated line that cites nothing built leaves it open")
}
