package main

import (
	"database/sql"
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
