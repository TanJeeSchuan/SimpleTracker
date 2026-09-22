package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

type restoreFixture struct {
	dataDir   string
	keySecret string
	projectID string
	issueID   string
}

func newRestoreFixture(t *testing.T) restoreFixture {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.BootstrapKey(t.Context(), "restore-fixture")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Restore fixture", "restore-fixture")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(t.Context(), project.ID, "Keep this issue", "restore validation", key.ID, "", "")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return restoreFixture{dataDir: dataDir, keySecret: key.Secret, projectID: project.ID, issueID: issue.ID}
}

func writeMalformedBackup(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE api_keys (id TEXT);
CREATE TABLE projects (id TEXT);
CREATE TABLE issues (id TEXT);
PRAGMA user_version = 2;
`)
	if version == 0 {
		_, err = db.Exec("PRAGMA user_version = 0")
	}
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeForgedV2Backup(t *testing.T, path, missing string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	apiKeyID := "TEXT PRIMARY KEY"
	if missing == "primary-key" {
		apiKeyID = "TEXT"
	}
	projectName := "TEXT NOT NULL"
	if missing == "not-null" {
		projectName = "TEXT"
	}
	projectSlug := "TEXT NOT NULL UNIQUE"
	if missing == "unique" {
		projectSlug = "TEXT NOT NULL"
	}
	issueProject := "TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE"
	if missing == "foreign-key" {
		issueProject = "TEXT NOT NULL"
	}
	issueState := "TEXT NOT NULL CHECK (state IN ('open', 'closed'))"
	if missing == "check" {
		issueState = "TEXT NOT NULL"
	}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE api_keys (
  id %s,
  name TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at TEXT
)`, apiKeyID),
		fmt.Sprintf(`CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name %s,
  slug %s,
  created_at TEXT NOT NULL
)`, projectName, projectSlug),
		fmt.Sprintf(`CREATE TABLE issues (
  id TEXT PRIMARY KEY,
  project_id %s,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  state %s,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT,
  creator_key_id TEXT NOT NULL,
  creator_actor TEXT NOT NULL DEFAULT '',
  creator_session TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  parent_id TEXT REFERENCES issues(id) ON DELETE SET NULL,
  assignee TEXT NOT NULL DEFAULT '',
  assigned_key_id TEXT NOT NULL DEFAULT '',
  assigned_actor TEXT NOT NULL DEFAULT '',
  assigned_at TEXT,
  UNIQUE(project_id, number)
)`, issueProject, issueState),
		`CREATE TABLE comments (
  id TEXT PRIMARY KEY,
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  details TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE issue_labels (
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(issue_id, label)
)`,
		`CREATE TABLE issue_blockers (
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  blocker_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY(issue_id, blocker_id),
  CHECK(issue_id <> blocker_id)
)`,
		`CREATE INDEX issues_project_state_idx ON issues(project_id, state, parent_id, position)`,
		`CREATE INDEX issues_parent_idx ON issues(parent_id, position)`,
		`CREATE INDEX comments_issue_idx ON comments(issue_id, created_at)`,
		`CREATE INDEX audit_target_idx ON audit_log(target_type, target_id, created_at)`,
		`CREATE INDEX issue_labels_label_idx ON issue_labels(label, issue_id)`,
		`CREATE INDEX issue_blockers_blocker_idx ON issue_blockers(blocker_id, issue_id)`,
		`PRAGMA user_version = 2`,
	}
	for _, statement := range statements {
		if missing == "index" && statement == "CREATE INDEX issue_labels_label_idx ON issue_labels(label, issue_id)" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func writeValidV1Database(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE api_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at TEXT
)`,
		`CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
)`,
		`CREATE TABLE issues (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK (state IN ('open', 'closed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT,
  creator_key_id TEXT NOT NULL,
  creator_actor TEXT NOT NULL DEFAULT '',
  creator_session TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  UNIQUE(project_id, number)
)`,
		`CREATE TABLE comments (
  id TEXT PRIMARY KEY,
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  details TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX issues_project_state_idx ON issues(project_id, state, position)`,
		`CREATE INDEX comments_issue_idx ON comments(issue_id, created_at)`,
		`CREATE INDEX audit_target_idx ON audit_log(target_type, target_id, created_at)`,
		`INSERT INTO api_keys(id, name, secret_hash, created_at) VALUES ('key-v1', 'v1', '` + secretHash("v1-secret") + `', '2026-01-01T00:00:00Z')`,
		`INSERT INTO projects(id, name, slug, created_at) VALUES ('project-v1', 'V1 project', 'v1-project', '2026-01-01T00:00:00Z')`,
		`INSERT INTO issues(id, project_id, number, title, body, state, created_at, updated_at, creator_key_id, creator_actor, creator_session, position) VALUES ('issue-v1', 'project-v1', 1, 'V1 issue', 'preserve me', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'key-v1', '', '', 0)`,
		`PRAGMA user_version = 1`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRestoreFixtureIntact(t *testing.T, fixture restoreFixture) {
	t.Helper()
	store, err := OpenStore(fixture.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Authenticate(t.Context(), fixture.keySecret); err != nil {
		t.Fatalf("auth state was replaced: %v", err)
	}
	if project, err := store.GetProject(t.Context(), fixture.projectID); err != nil || project.ID != fixture.projectID {
		t.Fatalf("project state was replaced: project=%#v err=%v", project, err)
	}
	if issue, err := store.GetIssue(t.Context(), fixture.issueID); err != nil || issue.ID != fixture.issueID {
		t.Fatalf("issue state was replaced: issue=%#v err=%v", issue, err)
	}
}

func TestRestoreRejectsMalformedSchemaBeforeReplacingLive(t *testing.T) {
	fixture := newRestoreFixture(t)
	backupPath := filepath.Join(t.TempDir(), "malformed.db")
	writeMalformedBackup(t, backupPath, 2)

	if err := RestoreStore(fixture.dataDir, backupPath); err == nil {
		t.Fatal("RestoreStore accepted a backup with missing required columns")
	}
	assertRestoreFixtureIntact(t, fixture)
}

func TestRestoreRejectsBackupWithoutSchemaVersion(t *testing.T) {
	fixture := newRestoreFixture(t)
	backupPath := filepath.Join(t.TempDir(), "unversioned.db")
	writeMalformedBackup(t, backupPath, 0)

	if err := RestoreStore(fixture.dataDir, backupPath); err == nil {
		t.Fatal("RestoreStore accepted a backup without a supported schema version")
	}
	assertRestoreFixtureIntact(t, fixture)
}

func TestRestoreRejectsForgedV2ConstraintsBeforeReplacingLive(t *testing.T) {
	for _, missing := range []string{"primary-key", "foreign-key", "unique", "not-null", "check", "index"} {
		t.Run(missing, func(t *testing.T) {
			fixture := newRestoreFixture(t)
			backupPath := filepath.Join(t.TempDir(), "forged.db")
			writeForgedV2Backup(t, backupPath, missing)

			if err := RestoreStore(fixture.dataDir, backupPath); err == nil {
				t.Fatalf("RestoreStore accepted a v2 backup missing %s", missing)
			}
			assertRestoreFixtureIntact(t, fixture)
		})
	}
}

func TestOpenStoreRejectsForgedV2Constraints(t *testing.T) {
	for _, missing := range []string{"primary-key", "foreign-key", "unique", "not-null", "check", "index"} {
		t.Run(missing, func(t *testing.T) {
			dataDir := t.TempDir()
			writeForgedV2Backup(t, filepath.Join(dataDir, "tracker.db"), missing)
			store, err := OpenStore(dataDir)
			if err == nil {
				store.Close()
				t.Fatalf("OpenStore accepted a v2 database missing %s", missing)
			}
		})
	}
}

func TestV1MigrationReplacesHistoricalIndexAndSurvivesRestartRestore(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := ensureDir(dataDir); err != nil {
		t.Fatal(err)
	}
	writeValidV1Database(t, filepath.Join(dataDir, "tracker.db"))

	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertV1State := func(store *Store) {
		t.Helper()
		if _, err := store.Authenticate(t.Context(), "v1-secret"); err != nil {
			t.Fatalf("v1 auth state was not preserved: %v", err)
		}
		project, err := store.GetProject(t.Context(), "project-v1")
		if err != nil || project.Slug != "v1-project" {
			t.Fatalf("v1 project state was not preserved: project=%#v err=%v", project, err)
		}
		issue, err := store.GetIssue(t.Context(), "issue-v1")
		if err != nil || issue.Title != "V1 issue" || issue.Body != "preserve me" {
			t.Fatalf("v1 issue state was not preserved: issue=%#v err=%v", issue, err)
		}
	}
	assertV1State(store)

	rows, err := store.db.Query("PRAGMA index_info(issues_project_state_idx)")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	var indexColumns []string
	for rows.Next() {
		var seq, cid int
		var name string
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			rows.Close()
			store.Close()
			t.Fatal(err)
		}
		indexColumns = append(indexColumns, name)
	}
	if err := rows.Close(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if !equalStringSlices(indexColumns, []string{"project_id", "state", "parent_id", "position"}) {
		store.Close()
		t.Fatalf("migration kept historical index shape: %v", indexColumns)
	}

	backupPath := filepath.Join(t.TempDir(), "v1-migrated.db")
	if err := store.Backup(t.Context(), backupPath); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertV1State(restarted)
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	if err := RestoreStore(dataDir, backupPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertV1State(restored)
}
