# report notes: stale "Not done" lines

2026-11-25, #1362 (part of #1359).

A note records what was done and what was left. A later PR often builds the
left part and nobody edits the note (JOB-83 and #262 were fixed by hand in
#1326/#1327). `jobshop report notes` lists the lines to check, so nobody has
to read every note.

```
jobshop report notes [--dir docs/notes] [--json]
```

It reads files only: no controller, network or store. Logic is in
`pkg/tooling/notes`.

## What it lists

Every line of a `*.md` file directly in the directory that contains
`not done` or `not built`, in any case: prose, list items and headings
(`## Not done`). Each has file, line and text, sorted by file then line.
Plain text is `file:line: open|probably resolved (...): text` and a count
line; `--json` is the `notes.Report` struct (`probably_resolved`,
`resolved_line`, `resolved_by` per finding).

## When it says probably resolved

A line after the finding's own text contains `built in`, `done in`, or a
reference `#123` / `PR 123` to a number other than the note's own. The own
number is the one in the file name (`1354-x.md`, `JOB-83-x.md`); a note named
without one has none. So a note that cites only its own issue stays open.

The finding's own text is not a later line: for a heading it is the section
up to the next heading of the same or a higher level, otherwise the lines
up to a blank line, a heading or (for a list item) the next item. This keeps
`Not done: x (see #12)` and the list under `## Not done` from resolving
themselves.

It is a heuristic. A later line that cites an unrelated PR marks a finding
resolved, and a follow-up that says "added" with no reference does not. Read
the line it names before editing the note. On 2026-11-25 it read 498 notes
and listed 22 lines, 1 probably resolved.

Test: `TestScan` and `TestScanBlock` in `pkg/tooling/notes` use a fixture
directory (open, resolved by phrase, resolved by PR, no match, own-issue
only, wrapped block, subdirectory and non-md files).
