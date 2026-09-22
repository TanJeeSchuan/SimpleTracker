# SimpleTracker

SimpleTracker is a single-process Go server backed by SQLite. The first API key is created locally and is the only credential needed for the API and browser UI.

```powershell
go run . bootstrap --data-dir .\data --name operator
go run . serve --data-dir .\data --listen 127.0.0.1:8080
```

The bootstrap command prints the secret once. Use it as `Authorization: Bearer <key>` or `X-API-Key: <key>`. Actor and session attribution can be supplied with `X-Actor-ID` and `X-Session-ID`.

The versioned API is rooted at `/api/v1`: projects support create/list/read, and issues support create/list/read/update, Markdown bodies, comments, close, and reopen. The browser login is at `/login`; issue pages use `/projects/<slug>/issues/<number>`.

The CLI mirrors the API and always emits JSON:

```powershell
go run . project create --server http://127.0.0.1:8080 --api-key $key --name Demo
go run . issue create --server http://127.0.0.1:8080 --api-key $key --project demo --title "First issue" --body "Markdown"
```

Run the black-box checks with `go test ./...`.
