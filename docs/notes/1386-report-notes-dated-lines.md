# report notes: dated follow-ups and an opt-out (#1386)

2026-11-26. Follows `1362-report-notes.md`.

- A line that starts with a date (`2026-11-04: built in #316`) ends the
  finding's own text, even with no blank line before it or when it is an
  indented continuation of a list item. It is then a later line and resolves
  the finding the way any other later line does (`built in`, `done in`, or a
  reference to another number). A wrapped line that is not dated stays part
  of the item. A dated line that cites nothing built leaves it open.
- `notes:ignore` anywhere on a line drops it from the report, for a line that
  only talks about "not done" (a note describing this report, a cross <!-- notes:ignore -->
  reference). Write it as an HTML comment: `<!-- notes:ignore -->`.
- Not changed: under a `## Not done` heading, dated lines inside the section <!-- notes:ignore -->
  are still its own text, so such a heading stays open until a later section
  cites something.

On this directory the report went from 29 lines, 26 open and 3 probably
resolved to 23 lines, 17 open and 6 probably resolved. `TestScanDatedFollowUp` covers both rules.
