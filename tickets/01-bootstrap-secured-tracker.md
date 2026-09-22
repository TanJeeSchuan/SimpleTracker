# 01 - Bootstrap a secured issue tracker

Labels: `ready-for-agent`

## What to build

Deliver the first usable vertical slice of the tracker: a fresh installation can initialize its SQLite-backed data directory, bootstrap equivalent full-access API-key authentication, start the server, create a project, and create/read/edit/close issues through both the versioned JSON/HTTP API and thin CLI. A minimal browser issue-detail view should expose the same project and issue state. Keep concurrency requirements at ordinary database consistency; this ticket does not introduce per-ticket distributed locking, compare-and-swap workflows, or atomic queue-consumer semantics.

## Acceptance criteria

- [ ] A fresh data directory can bootstrap the first API key and start one server process without an external database.
- [ ] Authenticated API and CLI calls can create/list/read projects and create/read/edit/close/reopen issues with Markdown bodies.
- [ ] Projects provide stable identity and issues provide stable project-local numbers and canonical URLs that survive restart.
- [ ] Multiple equivalent API keys can be created, named, listed, and revoked; active keys have identical authority and there are no users, roles, scopes, or RBAC.
- [ ] Requests may carry actor/session attribution separately from the API key, and issue mutations record enough attribution/timestamp metadata for later inspection.
- [ ] The browser can authenticate through the same security boundary and display a basic issue detail page.
- [ ] Black-box tests exercise the running server with a real SQLite database through the public API rather than testing persistence internals directly.

## Blocked by

None.
