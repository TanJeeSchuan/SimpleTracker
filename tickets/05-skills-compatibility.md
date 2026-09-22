# 05 - Prove Matt Pocock Skills compatibility

Labels: `ready-for-agent`

## What to build

Ship the tracker-facing adapter/documentation needed for Matt Pocock's Skills and prove the generic issue model supports the intended workflows without adding GitHub/GitLab-specific features or new server-side workflow object types. The adapter should be ready to copy into a repository as its issue-tracker instructions and should describe the CLI/API operations, labels, parent/blocker conventions, assignment semantics, and frontier query clearly enough for agents to use directly.

## Acceptance criteria

- [ ] A ready-to-copy tracker adapter documents project/issue CRUD, comments, labels, assignment, parent/child and blocker relations, stable references, search/filtering, ordering, and frontier queries using the shipped CLI/API.
- [ ] A representative `to-spec` flow can create one spec issue and mark it `ready-for-agent`.
- [ ] A representative `to-tickets` flow can create ordered child issues with explicit blockers and `ready-for-agent` labels.
- [ ] A representative triage flow can read metadata-rich issues/comments, apply/remove labels, filter attention buckets, and close/reopen issues as required.
- [ ] A representative code-review flow can resolve a stable issue reference and read the originating issue/spec without Git-provider integration.
- [ ] A representative Wayfinder flow can treat a `wayfinder:map` issue and `wayfinder:<type>` child tickets as ordinary issues, query the frontier, assign work, comment with a resolution, and close completed tickets.
- [ ] Compatibility verification does not require atomic claim-next semantics; ordinary frontier lookup followed by assignment matches the product boundary agreed for v0.
- [ ] No GitHub/GitLab PR/MR synchronization or conventional PM features are added to satisfy the adapter.

## Blocked by

- `02-issue-graph-and-queries.md`
