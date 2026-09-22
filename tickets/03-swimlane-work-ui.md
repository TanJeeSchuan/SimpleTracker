# 03 - Build the swimlane work UI

Labels: `ready-for-agent`

## What to build

Turn the issue graph into the primary human work surface: a Kanban-style swimlane board where parent issues define lanes and columns are derived from ordinary issue state. Humans can inspect and manipulate the same issues, labels, assignment, blockers, comments, and ordering that agents see through the API. Wayfinder maps use this same board model rather than a separate product area, and a secondary searchable/filterable issue list covers utility lookup.

## Acceptance criteria

- [ ] The primary project screen renders parent-based swimlanes plus a default lane for unparented issues.
- [ ] Cards are placed into derived Unclaimed, In progress, Blocked, and Done columns from issue state, assignment, and open blockers rather than a configurable workflow engine.
- [ ] Cards surface issue number/title, labels, assignment, blocker state, and useful update cues.
- [ ] Opening a card exposes Markdown detail, comments, labels, parent/children, blockers/blocked issues, assignment controls, and open/close editing against the same server state as the API.
- [ ] Manual card reordering updates the stable issue ordering used by frontier queries.
- [ ] A `wayfinder:map` parent and its child tickets appear naturally as a swimlane; no standalone Maps UI is introduced.
- [ ] A secondary issue list provides text search and the core filters exposed by the issue-query API.
- [ ] Browser acceptance tests cover board grouping, derived column placement, reordering, and issue-detail interactions without duplicating API behavior tests unnecessarily.

## Blocked by

- `02-issue-graph-and-queries.md`
