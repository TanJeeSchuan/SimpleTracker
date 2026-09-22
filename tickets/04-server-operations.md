# 04 - Make the server operable

Labels: `ready-for-agent`

## What to build

Make the single-server deployment safe to run and maintain without expanding the product into distributed infrastructure. The operator can inspect health/version, apply ordered startup migrations, take consistent SQLite backups, perform validated offline restore, and upgrade through replace-and-restart. The canonical distribution remains one self-contained server binary with embedded UI assets; an OCI image may wrap the same artifact as a convenience.

## Acceptance criteria

- [ ] The running server exposes health and version information suitable for normal service monitoring.
- [ ] Schema migrations are ordered/versioned, run before serving requests, and fail closed on migration failure or an unsupported newer schema.
- [ ] An online backup operation produces a consistent restorable SQLite copy in a separate location while the server is running.
- [ ] Restore is an explicit offline flow that validates the backup before replacing the live database and preserves project/issue/auth state after restart.
- [ ] Upgrade behavior is replace binary/image and restart; no rolling, multi-node, replication, or external-database machinery is introduced.
- [ ] The canonical distribution is one self-contained server binary using one configured data directory; an optional OCI wrapper behaves against the same data model.
- [ ] Operator-supplied HTTPS/reverse proxying remains external to the product.

## Blocked by

- `01-bootstrap-secured-tracker.md`
