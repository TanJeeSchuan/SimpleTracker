# 02 - Build the issue graph and work queries

Labels: `ready-for-agent`

## What to build

Extend the usable tracker so projects can represent real work graphs: issues support comments, arbitrary labels, assignment, parent/child relationships, blocker relationships, stable sibling ordering, derived blocked state, frontier queries, and the search/filter surface required by agents, triage, and review. Frontier means open + unblocked + unassigned work in stable order; selecting an issue from the frontier and assigning it are ordinary separate operations rather than an atomic distributed queue protocol.

## Acceptance criteria

- [ ] API and CLI can add/read comments with actor/key attribution and timestamps, add/remove labels, assign/unassign issues, and expose metadata-rich issue reads.
- [ ] Issues can set/clear one parent and add/remove directed blockers; parent and blocker cycles are rejected without leaving invalid graph state.
- [ ] Open blockers derive whether an issue is blocked; no independently editable Blocked lifecycle state is introduced.
- [ ] Child/sibling ordering is stable and can be updated explicitly.
- [ ] Frontier queries return open, unblocked, unassigned issues in that stable order for a parent or project scope.
- [ ] Frontier lookup and assignment are separate operations; the tracker does not promise atomic claim-next or same-ticket distributed locking semantics.
- [ ] Issue listing/search supports project, open/closed state, parent, label, assigned/unassigned or assignee, updated/age filtering, and text search sufficient for triage and review workflows.
- [ ] API, CLI JSON, and issue-detail UI expose the same graph, labels, comments, assignment, and derived blocker information.

## Blocked by

- `01-bootstrap-secured-tracker.md`
