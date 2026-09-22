# Issue tracker: SimpleTracker

This repository uses the self-hosted SimpleTracker API as its issue tracker.
Use the checked-out CLI (`go run .`) or an HTTP client against the configured
server. Do not use GitHub, GitLab, pull requests, merge requests, or a second
workflow system for these operations.

## Connection and attribution

Set `SIMPLETRACKER_API_KEY` for CLI calls, or pass `--api-key` on every call.
The server defaults to `http://127.0.0.1:8080`; pass `--server` on each call
when it is different. Every CLI command emits JSON.

```bash
export SIMPLETRACKER_API_KEY='...'

go run . project list
```

The equivalent HTTP requests use `Authorization: Bearer <key>` (or
`X-API-Key: <key>`). Add `X-Actor-ID` and `X-Session-ID` to attribute a write
to an agent and run. The resulting issue and comment metadata exposes the key,
actor, session, and timestamps used for the write.

## Identity and lifecycle

Projects have a stable opaque `id` and a unique human-readable `slug`.
Issues have a stable opaque `id`, a project-local monotonically assigned
`number`, and the stable reference `<project-slug>#<number>`. The API also
returns a canonical browser `url`. Resolve a reference with either the issue
ID or the project-number reference before reading or writing it; do not infer
identity from title or position.

The CRUD-like lifecycle is deliberately small and stable:

- Project create/list/read: `project create`, `project list`, `project get`.
- Issue create/list/read/update: `issue create`, `issue list`, `issue get`,
  `issue update`.
- Issue state transitions: `issue close` and `issue reopen`.
- Destructive delete is not part of the tracker contract. Close an issue to
  retire it while preserving its stable reference, comments, graph edges, and
  audit history. Project identity is likewise retained.

The API equivalents are `POST /api/v1/projects`, `GET /api/v1/projects`,
`GET /api/v1/projects/<id-or-slug>`, `POST /api/v1/projects/<slug>/issues`,
`GET /api/v1/projects/<slug>/issues`, `GET/PATCH /api/v1/issues/<id-or-ref>`,
and `POST /api/v1/issues/<id-or-ref>/{close,reopen}`.

## Issue operations

Use these CLI operations for the complete agent-facing surface:

```bash
# create, read, update, and list
go run . issue create --project demo --title 'Title' --body 'Markdown' \
  --labels 'ready-for-agent,enhancement'
go run . issue get --id 'demo#12'
go run . issue update --id 'demo#12' --body 'Revised Markdown'
go run . issue list --project demo --state open --label ready-for-agent

# comments, labels, assignment, lifecycle
go run . issue comment --id 'demo#12' --body 'Progress note'
go run . issue label --id 'demo#12' --label needs-info
go run . issue label --id 'demo#12' --label needs-info --remove
go run . issue assign --id 'demo#12' --assignee 'agent/backend'
go run . issue unassign --id 'demo#12'
go run . issue close --id 'demo#12'
go run . issue reopen --id 'demo#12'

# graph and ordering
go run . issue parent --id 'demo#13' --parent 'demo#12'
go run . issue parent --id 'demo#13'                 # clear the parent
go run . issue block --id 'demo#13' --parent 'demo#9'
go run . issue unblock --id 'demo#13' --parent 'demo#9'
go run . issue order --id 'demo#13' --position 0
```

The HTTP equivalents are:

| Operation | Request |
| --- | --- |
| comments | `GET/POST /api/v1/issues/<ref>/comments`, body `{"body":"..."}` |
| labels | `GET/POST /api/v1/issues/<ref>/labels`, or `DELETE /api/v1/issues/<ref>/labels/<label>` |
| assignment | `POST/DELETE /api/v1/issues/<ref>/assign`, body `{"assignee":"..."}` |
| parent | `POST/DELETE /api/v1/issues/<ref>/parent`, body `{"parent_id":"<ref>"}` |
| blockers | `GET/POST/DELETE /api/v1/issues/<ref>/blockers`; a POST body is `{"blocker_id":"<ref>"}` |
| ordering | `POST /api/v1/issues/<ref>/position`, body `{"position":0}` |
| state | `POST /api/v1/issues/<ref>/close` or `/reopen` |

Issue reads are metadata-rich. They include the Markdown body, comments,
labels, creator attribution, assignment attribution, `parent` and `children`,
`blockers`/`blocked_by`, reverse `blocked_issues`, `position`, state, and the
derived `blocked` boolean.

## Graph conventions

Use one parent for hierarchy and directed blockers for prerequisites. Both
endpoints accept an issue ID or `<project>#<number>` reference.

- `parent` points from child to parent. A child and parent must be in the same
  project; an issue has at most one parent; self-parenting and parent cycles
  are rejected.
- `blockers` points from blocked issue to blocker. An open blocker makes the
  blocked issue's derived `blocked` value true; closing the blocker makes it
  eligible again. Self-blockers and blocker cycles are rejected.
- `position` is a stable non-negative sibling order. New children append to
  their parent's sibling list. Query results are ordered by position, number,
  then ID, so ties remain deterministic. Use `issue order` after creating a
  batch when an explicit order matters.

Labels are arbitrary trimmed strings, deduplicated on an issue and returned in
sorted order. The standard state labels are `needs-triage`, `needs-info`,
`ready-for-agent`, `ready-for-human`, and `wontfix`; category labels such as
`bug` and `enhancement` are independent and may be combined with one state
label. Workflow-specific labels use a prefix, for example `wayfinder:map` and
`wayfinder:task`.

Assignment is an explicit string chosen by the caller. `issue assign` records
the assignee and the key/actor/time that performed the assignment;
`issue unassign` clears all assignment fields. Assignment is not a lease and
does not claim work atomically.

## Search, filtering, and frontier

List issues with `issue list` or `GET /api/v1/issues` (the project-scoped
endpoint is also available). Supported filters are:

| Intent | CLI | HTTP query |
| --- | --- | --- |
| project | `--project demo` | `project=demo` or `/projects/demo/issues` |
| state | `--state open` | `state=open` (also `status`) |
| parent | `--parent demo#12` | `parent=demo%2312`; `none`/`root` means top-level |
| label | `--label ready-for-agent` | `label=ready-for-agent` |
| assignment bucket | `--assigned assigned` | `assigned=assigned` or `unassigned` |
| assignee | `--assignee agent/backend` | `assignee=agent/backend` |
| text | `--q database` | `q=database` (also `search`) |
| age | `--age 7` | `age=7` (integer days; duration values such as `168h` are also accepted) |
| update window | `--updated-since <RFC3339>` | `updated_since`/`updated_until` |

`issue frontier` or `GET /api/v1/frontier` returns `frontier` (and the
backwards-compatible `issues` key) in stable order. A frontier item is open,
unblocked, and unassigned. Scope it to a project with `--project` or
`/projects/<slug>/frontier`, and to a parent with `--parent`:

```bash
go run . issue frontier --project demo --parent 'demo#12'
```

Frontier lookup and assignment are intentionally separate ordinary requests:
read the frontier, choose an item, then call `issue assign`. The v0 contract
does not provide atomic `claim-next`, distributed locking, leases, or a
same-ticket queue guarantee. Agents must tolerate another worker assigning an
item between the two requests and query again when needed.

## Skill workflows

### `to-spec`

Create one issue containing the synthesized spec and mark it ready in the same
request. Use `--labels ready-for-agent` and retain the returned stable
reference. The spec body is ordinary Markdown; no provider-specific issue or
PR object is needed.

```bash
go run . issue create --project demo \
  --title 'Spec: searchable work queue' \
  --body '<Problem Statement>\n\n<Solution>\n\n<User Stories>...' \
  --labels ready-for-agent --actor to-spec --session "$SESSION"
```

### `to-tickets`

Create the spec/map issue first, then create ordered child issues with
`--parent`. Add an explicit blocker edge from each blocked child to the issue
that must finish first. Every executable child gets `ready-for-agent` (and any
category label); use `issue order` when the child order is part of the plan.

```bash
go run . issue create --project demo --title 'Spec: queue' \
  --body '...' --labels ready-for-agent
# Extract .id from JSON, then:
go run . issue create --project demo --title 'Ticket 1' --body '...' \
  --parent 'demo#20' --labels 'ready-for-agent,enhancement'
go run . issue create --project demo --title 'Ticket 2' --body '...' \
  --parent 'demo#20' --labels ready-for-agent
go run . issue block --id 'demo#22' --parent 'demo#21'
```

The blocker direction is significant: `--id` is the blocked ticket and
`--parent` is the blocker. Close the blocker when its work is complete; the
child then becomes frontier-eligible if it is still open and unassigned.

### `triage`

For attention buckets, list with the deterministic server order plus
`state`/`label`/`assigned` filters, then sort the returned `created_at` values
ascending when presenting the skill's oldest-first view. Read each issue by
stable reference to get the complete body, comments, labels, graph, and
attribution. Apply or remove labels with `issue label`; assign only when a
person or agent explicitly owns the next action; close or reopen with the state
commands.

Every triage comment starts with this exact disclaimer, followed by the useful
triage note:

```text
> *This was generated by AI during triage.*
```

The usual buckets are unlabeled issues, `needs-triage`, and `needs-info` with
reporter activity since the last triage note. Category (`bug` or
`enhancement`) and state labels are independent; preserve exactly one category
and one state role after triage.

### `code-review`

Resolve the issue reference from the commit/spec context with `issue get` (or
`GET /api/v1/issues/<id-or-ref>`), then read its body and embedded comments.
The stable ID and `<project>#<number>` reference remain valid after updates,
reordering, close/reopen, and server restarts. Code review needs no GitHub or
GitLab integration and no pull-request synchronization.

### `wayfinder`

Represent the map and its decisions as ordinary issues:

1. Create one open issue labelled `wayfinder:map` with the map template body.
2. Create each decision child with `--parent <map-ref>` and exactly one
   `wayfinder:<type>` label (`research`, `prototype`, `grilling`, or `task`).
3. Add blocker edges to express prerequisites; assign a child before working
   it, using the assignment as the claim.
4. Query `issue frontier --project <slug> --parent <map-ref>` to find open,
   unblocked, unassigned decision tickets.
5. Comment the resolution on the child, then close it. Update the map's
   Decisions-so-far body or comment with a short pointer to that child.

The tracker has no special map, decision, claim, or resolution object type:
labels, parent links, blockers, assignment, comments, and close/reopen are the
whole protocol. A child that another agent assigned is no longer frontier
work; query again rather than attempting an atomic claim.

## Publishing and errors

When a skill says “publish to the issue tracker”, create or update a
SimpleTracker issue through the commands above and apply `ready-for-agent` to
work that is fully specified. Read response JSON and retain the returned `id`
and stable reference. A 4xx response contains `error.code` and
`error.message`; fix the request or relation rather than inventing an
alternative workflow.
