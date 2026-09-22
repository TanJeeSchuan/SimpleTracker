# SimpleTracker

SimpleTracker is a single-process Go server backed by SQLite. The first API key is created locally and is the only credential needed for the API and browser UI.

```powershell
go run . bootstrap --data-dir .\data --name operator
go run . serve --data-dir .\data --listen 127.0.0.1:8080
```

The server runs ordered SQLite migrations before it starts accepting requests.
`GET /healthz` (also `/readyz`) checks the database connection and returns
`status`, `version`, and `schema`; `GET /version` reports the API and schema
version without authentication. A non-zero response from `/healthz` means the
process should be restarted or investigated before it receives traffic.

Back up the live database with the SQLite online backup operation. The output
must be a new file outside the live `tracker.db` path:

```powershell
go run . backup --data-dir .\data --output .\backups\tracker-2026-09-22.db
```

Restore is deliberately an offline operation. Stop the server, validate and
install the backup, then start the same binary again. The previous database
and its WAL sidecars are retained as `*.before-restore-*` rollback copies:

```powershell
go run . restore --data-dir .\data --backup .\backups\tracker-2026-09-22.db
go run . serve --data-dir .\data --listen 127.0.0.1:8080
```

Upgrades are replace-the-binary (or replace an OCI image) and restart. There
is one self-contained server binary and one configured data directory; no
rolling deployment, replication, or external database is required. HTTPS and
reverse proxying remain operator-managed at the edge.

The bootstrap command prints the secret once. Use it as `Authorization: Bearer <key>` or `X-API-Key: <key>`. Actor and session attribution can be supplied with `X-Actor-ID` and `X-Session-ID`.

The versioned API is rooted at `/api/v1`: projects support create/list/read, and issues support create/list/read/update, Markdown bodies, comments, close, and reopen. The browser login is at `/login`; issue pages use `/projects/<slug>/issues/<number>`.

The CLI mirrors the API and always emits JSON:

```powershell
go run . project create --server http://127.0.0.1:8080 --api-key $key --name Demo
go run . issue create --server http://127.0.0.1:8080 --api-key $key --project demo --title "First issue" --body "Markdown"
```

Run the black-box checks with `go test ./...`.
