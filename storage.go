package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")
var ErrInvalidTransition = errors.New("invalid transition")
var ErrUnauthorized = errors.New("unauthorized")

const currentSchemaVersion = 2

type Store struct {
	db     *sql.DB
	dbPath string
}

func OpenStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data directory is required")
	}
	if err := ensureDir(dataDir); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "tracker.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	store := &Store{db: db, dbPath: dbPath}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func ensureDir(path string) error {
	// Kept separate so callers and tests can create a data directory without
	// reaching into storage implementation details.
	return mkdirAll(path)
}

func (s *Store) Close() error { return s.db.Close() }

// Backup creates a standalone SQLite backup while the store is serving. The
// VACUUM INTO operation runs from a consistent read transaction, including
// any committed changes in the WAL, and produces a database that can be
// opened without the live database's -wal/-shm sidecars.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store is not open")
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return fmt.Errorf("backup destination is required")
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve backup destination: %w", err)
	}
	if sameFilePath(destinationPath, s.dbPath) {
		return fmt.Errorf("backup destination must differ from the live database")
	}
	parent := filepath.Dir(destinationPath)
	if err := ensureDir(parent); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return fmt.Errorf("backup destination already exists: %s", destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".simpletracker-backup-*.db")
	if err != nil {
		return fmt.Errorf("create backup temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close backup temporary file: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("prepare backup temporary file: %w", err)
	}
	defer os.Remove(temporaryPath)

	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", temporaryPath); err != nil {
		return fmt.Errorf("create sqlite backup: %w", err)
	}
	if _, err := validateSQLiteBackup(temporaryPath); err != nil {
		return fmt.Errorf("validate sqlite backup: %w", err)
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		return fmt.Errorf("publish sqlite backup: %w", err)
	}
	return nil
}

// RestoreStore validates a backup and installs it as dataDir/tracker.db.
// Callers must stop the server first: this is intentionally an offline
// operation. The previous database and any WAL sidecars are retained beside
// the new database as a rollback copy until the operator removes them.
func RestoreStore(dataDir, backupPath string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("data directory is required")
	}
	backupPath = strings.TrimSpace(backupPath)
	if backupPath == "" {
		return fmt.Errorf("restore source is required")
	}
	backupPath, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("resolve restore source: %w", err)
	}
	if _, err := validateSQLiteBackup(backupPath); err != nil {
		return fmt.Errorf("validate restore source: %w", err)
	}
	if err := ensureDir(dataDir); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	livePath, err := filepath.Abs(filepath.Join(dataDir, "tracker.db"))
	if err != nil {
		return fmt.Errorf("resolve live database: %w", err)
	}
	if sameFilePath(livePath, backupPath) {
		return fmt.Errorf("restore source must differ from the live database")
	}

	temporary, err := os.CreateTemp(filepath.Dir(livePath), ".simpletracker-restore-*.db")
	if err != nil {
		return fmt.Errorf("create restore temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	copyErr := func() error {
		input, err := os.Open(backupPath)
		if err != nil {
			return err
		}
		defer input.Close()
		if _, err := io.Copy(temporary, input); err != nil {
			return err
		}
		return temporary.Sync()
	}()
	if err := temporary.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("copy restore source: %w", copyErr)
	}
	defer os.Remove(temporaryPath)
	if _, err := validateSQLiteBackup(temporaryPath); err != nil {
		return fmt.Errorf("validate restore copy: %w", err)
	}

	rollbackBase := livePath + ".before-restore-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	type movedFile struct{ from, to string }
	moved := make([]movedFile, 0, 3)
	moveAside := func(path, suffix string) error {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		target := rollbackBase + suffix
		if err := os.Rename(path, target); err != nil {
			return err
		}
		moved = append(moved, movedFile{from: path, to: target})
		return nil
	}
	rollback := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i].to, moved[i].from)
		}
	}
	if err := moveAside(livePath, ""); err != nil {
		return fmt.Errorf("move live database aside: %w", err)
	}
	if err := moveAside(livePath+"-wal", "-wal"); err != nil {
		rollback()
		return fmt.Errorf("move live database WAL aside: %w", err)
	}
	if err := moveAside(livePath+"-shm", "-shm"); err != nil {
		rollback()
		return fmt.Errorf("move live database shared memory aside: %w", err)
	}
	if err := os.Rename(temporaryPath, livePath); err != nil {
		rollback()
		return fmt.Errorf("install restored database: %w", err)
	}
	return nil
}

func validateSQLiteBackup(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("backup is not a regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		return 0, err
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return 0, err
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return 0, fmt.Errorf("integrity check failed: %s", integrity)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	requiredTables, ok := requiredSchemaColumns(version)
	if !ok {
		if version > currentSchemaVersion {
			return 0, fmt.Errorf("schema version %d is newer than this binary supports", version)
		}
		return 0, fmt.Errorf("backup has unsupported schema version %d", version)
	}
	for _, table := range sortedSchemaTableNames(requiredTables) {
		var tableType string
		if err := db.QueryRow("SELECT type FROM sqlite_master WHERE name=? LIMIT 1", table).Scan(&tableType); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("backup is missing required table %q", table)
			}
			return 0, err
		}
		if tableType != "table" {
			return 0, fmt.Errorf("backup object %q is %s, want table", table, tableType)
		}
		columns, err := sqliteTableColumns(db, table)
		if err != nil {
			return 0, fmt.Errorf("inspect table %q: %w", table, err)
		}
		for _, column := range requiredTables[table] {
			if _, ok := columns[column]; !ok {
				return 0, fmt.Errorf("backup table %q is missing required column %q", table, column)
			}
		}
	}
	if err := validateSchemaContract(db, version); err != nil {
		return 0, fmt.Errorf("validate schema contract: %w", err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return 0, fmt.Errorf("check backup foreign keys: %w", err)
	}
	if rows.Next() {
		rows.Close()
		return 0, fmt.Errorf("backup contains a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read backup foreign-key check: %w", err)
	}
	rows.Close()
	return version, nil
}

var schemaColumnsV1 = map[string][]string{
	"api_keys":  {"id", "name", "secret_hash", "created_at", "last_used_at", "revoked_at"},
	"projects":  {"id", "name", "slug", "created_at"},
	"issues":    {"id", "project_id", "number", "title", "body", "state", "created_at", "updated_at", "closed_at", "creator_key_id", "creator_actor", "creator_session", "position"},
	"comments":  {"id", "issue_id", "body", "created_at", "key_id", "actor", "session"},
	"audit_log": {"id", "key_id", "actor", "session", "operation", "target_type", "target_id", "created_at", "details"},
}

var schemaColumnsV2 = map[string][]string{
	"api_keys":       {"id", "name", "secret_hash", "created_at", "last_used_at", "revoked_at"},
	"projects":       {"id", "name", "slug", "created_at"},
	"issues":         {"id", "project_id", "number", "title", "body", "state", "created_at", "updated_at", "closed_at", "creator_key_id", "creator_actor", "creator_session", "position", "parent_id", "assignee", "assigned_key_id", "assigned_actor", "assigned_at"},
	"comments":       {"id", "issue_id", "body", "created_at", "key_id", "actor", "session"},
	"audit_log":      {"id", "key_id", "actor", "session", "operation", "target_type", "target_id", "created_at", "details"},
	"issue_labels":   {"issue_id", "label", "created_at"},
	"issue_blockers": {"issue_id", "blocker_id", "created_at"},
}

func requiredSchemaColumns(version int) (map[string][]string, bool) {
	switch version {
	case 1:
		return schemaColumnsV1, true
	case currentSchemaVersion:
		return schemaColumnsV2, true
	default:
		return nil, false
	}
}

type sqliteForeignKeyContract struct {
	from     string
	to       string
	table    string
	onDelete string
}

type sqliteIndexContract struct {
	table   string
	name    string
	columns []string
}

type sqliteSchemaContract struct {
	notNull  map[string][]string
	defaults map[string]map[string]string
	primary  map[string][]string
	unique   map[string][][]string
	foreign  map[string][]sqliteForeignKeyContract
	checks   map[string][]string
	indexes  []sqliteIndexContract
}

var schemaContractV1 = sqliteSchemaContract{
	notNull: map[string][]string{
		"api_keys":  {"name", "secret_hash", "created_at"},
		"projects":  {"name", "slug", "created_at"},
		"issues":    {"project_id", "number", "title", "body", "state", "created_at", "updated_at", "creator_key_id", "creator_actor", "creator_session", "position"},
		"comments":  {"issue_id", "body", "created_at", "key_id", "actor", "session"},
		"audit_log": {"key_id", "actor", "session", "operation", "target_type", "target_id", "created_at", "details"},
	},
	defaults: map[string]map[string]string{
		"issues":    {"body": "''", "creator_actor": "''", "creator_session": "''", "position": "0"},
		"comments":  {"actor": "''", "session": "''"},
		"audit_log": {"actor": "''", "session": "''", "details": "''"},
	},
	primary: map[string][]string{
		"api_keys":  {"id"},
		"projects":  {"id"},
		"issues":    {"id"},
		"comments":  {"id"},
		"audit_log": {"id"},
	},
	unique: map[string][][]string{
		"api_keys": {{"secret_hash"}},
		"projects": {{"slug"}},
		"issues":   {{"project_id", "number"}},
	},
	foreign: map[string][]sqliteForeignKeyContract{
		"issues":   {{from: "project_id", to: "id", table: "projects", onDelete: "cascade"}},
		"comments": {{from: "issue_id", to: "id", table: "issues", onDelete: "cascade"}},
	},
	checks: map[string][]string{
		"issues": {"check(statein('open','closed'))"},
	},
	indexes: []sqliteIndexContract{
		{table: "issues", name: "issues_project_state_idx", columns: []string{"project_id", "state", "position"}},
		{table: "comments", name: "comments_issue_idx", columns: []string{"issue_id", "created_at"}},
		{table: "audit_log", name: "audit_target_idx", columns: []string{"target_type", "target_id", "created_at"}},
	},
}

var schemaContractV2 = sqliteSchemaContract{
	notNull: map[string][]string{
		"api_keys":       {"name", "secret_hash", "created_at"},
		"projects":       {"name", "slug", "created_at"},
		"issues":         {"project_id", "number", "title", "body", "state", "created_at", "updated_at", "creator_key_id", "creator_actor", "creator_session", "position", "assignee", "assigned_key_id", "assigned_actor"},
		"comments":       {"issue_id", "body", "created_at", "key_id", "actor", "session"},
		"audit_log":      {"key_id", "actor", "session", "operation", "target_type", "target_id", "created_at", "details"},
		"issue_labels":   {"issue_id", "label", "created_at"},
		"issue_blockers": {"issue_id", "blocker_id", "created_at"},
	},
	defaults: map[string]map[string]string{
		"issues":    {"body": "''", "creator_actor": "''", "creator_session": "''", "position": "0", "assignee": "''", "assigned_key_id": "''", "assigned_actor": "''"},
		"comments":  {"actor": "''", "session": "''"},
		"audit_log": {"actor": "''", "session": "''", "details": "''"},
	},
	primary: map[string][]string{
		"api_keys":       {"id"},
		"projects":       {"id"},
		"issues":         {"id"},
		"comments":       {"id"},
		"audit_log":      {"id"},
		"issue_labels":   {"issue_id", "label"},
		"issue_blockers": {"issue_id", "blocker_id"},
	},
	unique: map[string][][]string{
		"api_keys": {{"secret_hash"}},
		"projects": {{"slug"}},
		"issues":   {{"project_id", "number"}},
	},
	foreign: map[string][]sqliteForeignKeyContract{
		"issues": {
			{from: "project_id", to: "id", table: "projects", onDelete: "cascade"},
			{from: "parent_id", to: "id", table: "issues", onDelete: "set null"},
		},
		"comments":     {{from: "issue_id", to: "id", table: "issues", onDelete: "cascade"}},
		"issue_labels": {{from: "issue_id", to: "id", table: "issues", onDelete: "cascade"}},
		"issue_blockers": {
			{from: "issue_id", to: "id", table: "issues", onDelete: "cascade"},
			{from: "blocker_id", to: "id", table: "issues", onDelete: "cascade"},
		},
	},
	checks: map[string][]string{
		"issues":         {"check(statein('open','closed'))"},
		"issue_blockers": {"check(issue_id<>blocker_id)"},
	},
	indexes: []sqliteIndexContract{
		{table: "issues", name: "issues_project_state_idx", columns: []string{"project_id", "state", "parent_id", "position"}},
		{table: "issues", name: "issues_parent_idx", columns: []string{"parent_id", "position"}},
		{table: "comments", name: "comments_issue_idx", columns: []string{"issue_id", "created_at"}},
		{table: "audit_log", name: "audit_target_idx", columns: []string{"target_type", "target_id", "created_at"}},
		{table: "issue_labels", name: "issue_labels_label_idx", columns: []string{"label", "issue_id"}},
		{table: "issue_blockers", name: "issue_blockers_blocker_idx", columns: []string{"blocker_id", "issue_id"}},
	},
}

func schemaContract(version int) (sqliteSchemaContract, bool) {
	switch version {
	case 1:
		return schemaContractV1, true
	case currentSchemaVersion:
		return schemaContractV2, true
	default:
		return sqliteSchemaContract{}, false
	}
}

func validateSchemaContract(db *sql.DB, version int) error {
	contract, ok := schemaContract(version)
	if !ok {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	for _, table := range sortedSchemaTableNames(contract.notNull) {
		columns, err := sqliteTableColumns(db, table)
		if err != nil {
			return fmt.Errorf("inspect table %q: %w", table, err)
		}
		for _, column := range contract.notNull[table] {
			info, ok := columns[column]
			if !ok {
				return fmt.Errorf("table %q is missing required column %q", table, column)
			}
			if !info.notNull {
				return fmt.Errorf("table %q column %q must be NOT NULL", table, column)
			}
		}
		for column, expectedDefault := range contract.defaults[table] {
			info, ok := columns[column]
			if !ok {
				return fmt.Errorf("table %q is missing defaulted column %q", table, column)
			}
			if !info.defaultValue.Valid || strings.TrimSpace(info.defaultValue.String) != expectedDefault {
				return fmt.Errorf("table %q column %q has unexpected default", table, column)
			}
		}
		if err := validatePrimaryKey(columns, table, contract.primary[table]); err != nil {
			return err
		}
		if err := validateUniqueConstraints(db, table, contract.unique[table]); err != nil {
			return err
		}
		if err := validateForeignKeys(db, table, contract.foreign[table]); err != nil {
			return err
		}
		if err := validateChecks(db, table, contract.checks[table]); err != nil {
			return err
		}
	}
	for _, index := range contract.indexes {
		if err := validateIndex(db, index); err != nil {
			return err
		}
	}
	return nil
}

func validatePrimaryKey(columns map[string]sqliteColumnInfo, table string, expected []string) error {
	for position, column := range expected {
		info, ok := columns[column]
		if !ok || info.primaryKey != position+1 {
			return fmt.Errorf("table %q is missing required primary key", table)
		}
	}
	for column, info := range columns {
		if info.primaryKey == 0 {
			continue
		}
		found := false
		for _, expectedColumn := range expected {
			if expectedColumn == column {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("table %q has an unexpected primary-key column %q", table, column)
		}
	}
	return nil
}

func validateUniqueConstraints(db *sql.DB, table string, expected [][]string) error {
	indexes, err := sqliteIndexes(db, table)
	if err != nil {
		return fmt.Errorf("inspect unique constraints on %q: %w", table, err)
	}
	for _, want := range expected {
		found := false
		for _, index := range indexes {
			if index.unique && equalStringSlices(index.columns, want) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("table %q is missing required UNIQUE constraint on %v", table, want)
		}
	}
	return nil
}

func validateForeignKeys(db *sql.DB, table string, expected []sqliteForeignKeyContract) error {
	rows, err := db.Query("PRAGMA foreign_key_list(" + quoteSQLiteIdentifier(table) + ")")
	if err != nil {
		return fmt.Errorf("inspect foreign keys on %q: %w", table, err)
	}
	actual := make([]sqliteForeignKeyContract, 0)
	for rows.Next() {
		var id, seq int
		var referenceTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &referenceTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		actual = append(actual, sqliteForeignKeyContract{from: from, to: to, table: referenceTable, onDelete: strings.ToLower(onDelete)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(actual) != len(expected) {
		return fmt.Errorf("table %q has %d foreign keys, want %d", table, len(actual), len(expected))
	}
	for _, want := range expected {
		found := false
		for _, got := range actual {
			if strings.EqualFold(got.from, want.from) && strings.EqualFold(got.to, want.to) && strings.EqualFold(got.table, want.table) && strings.EqualFold(got.onDelete, want.onDelete) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("table %q is missing required foreign key %s -> %s.%s", table, want.from, want.table, want.to)
		}
	}
	return nil
}

func validateChecks(db *sql.DB, table string, expected []string) error {
	if len(expected) == 0 {
		return nil
	}
	var createSQL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&createSQL); err != nil {
		return fmt.Errorf("read table definition %q: %w", table, err)
	}
	normalized := compactSQL(createSQL)
	for _, check := range expected {
		if !strings.Contains(normalized, compactSQL(check)) {
			return fmt.Errorf("table %q is missing required CHECK constraint %q", table, check)
		}
	}
	return nil
}

func validateIndex(db *sql.DB, expected sqliteIndexContract) error {
	indexes, err := sqliteIndexes(db, expected.table)
	if err != nil {
		return fmt.Errorf("inspect index %q: %w", expected.name, err)
	}
	index, ok := indexes[expected.name]
	if !ok {
		return fmt.Errorf("table %q is missing required index %q", expected.table, expected.name)
	}
	if index.unique || !equalStringSlices(index.columns, expected.columns) {
		return fmt.Errorf("index %q has unexpected definition", expected.name)
	}
	return nil
}

type sqliteIndexInfo struct {
	unique  bool
	columns []string
}

func sqliteIndexes(db *sql.DB, table string) (map[string]sqliteIndexInfo, error) {
	rows, err := db.Query("PRAGMA index_list(" + quoteSQLiteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	type listedIndex struct {
		name   string
		unique bool
	}
	listed := make([]listedIndex, 0)
	for rows.Next() {
		var seq, unique, partial int
		var origin string
		var name string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return nil, err
		}
		listed = append(listed, listedIndex{name: name, unique: unique != 0})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	result := make(map[string]sqliteIndexInfo, len(listed))
	for _, index := range listed {
		indexRows, err := db.Query("PRAGMA index_info(" + quoteSQLiteIdentifier(index.name) + ")")
		if err != nil {
			return nil, err
		}
		columns := make([]string, 0)
		for indexRows.Next() {
			var seqno, cid int
			var name sql.NullString
			if err := indexRows.Scan(&seqno, &cid, &name); err != nil {
				indexRows.Close()
				return nil, err
			}
			if !name.Valid {
				indexRows.Close()
				return nil, fmt.Errorf("index %q contains an expression", index.name)
			}
			columns = append(columns, name.String)
		}
		if err := indexRows.Err(); err != nil {
			indexRows.Close()
			return nil, err
		}
		indexRows.Close()
		result[index.name] = sqliteIndexInfo{unique: index.unique, columns: columns}
	}
	return result, nil
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

func compactSQL(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), "")
}

func sortedSchemaTableNames(columns map[string][]string) []string {
	tables := make([]string, 0, len(columns))
	for table := range columns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

type sqliteColumnInfo struct {
	notNull      bool
	defaultValue sql.NullString
	primaryKey   int
}

func sqliteTableColumns(db *sql.DB, table string) (map[string]sqliteColumnInfo, error) {
	rows, err := db.Query("PRAGMA table_info(" + quoteSQLiteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]sqliteColumnInfo)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = sqliteColumnInfo{notNull: notNull != 0, defaultValue: defaultValue, primaryKey: primaryKey}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sameFilePath(left, right string) bool {
	if strings.TrimSpace(right) == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbs = filepath.Clean(leftAbs)
	rightAbs = filepath.Clean(rightAbs)
	if strings.EqualFold(leftAbs, rightAbs) {
		return true
	}
	leftResolved, leftErr := filepath.EvalSymlinks(leftAbs)
	rightResolved, rightErr := filepath.EvalSymlinks(rightAbs)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftResolved), filepath.Clean(rightResolved))
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary supports", version)
	}
	if version == currentSchemaVersion {
		if err := validateSchemaContract(s.db, version); err != nil {
			return fmt.Errorf("validate schema: %w", err)
		}
		return nil
	}

	// Every migration runs in one transaction. SQLite supports transactional
	// DDL, so a failed migration cannot leave a database half-upgraded with a
	// user_version that still claims it is ready to serve.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	switch version {
	case 0:
		_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS issues (
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
  parent_id TEXT REFERENCES issues(id) ON DELETE SET NULL,
  assignee TEXT NOT NULL DEFAULT '',
  assigned_key_id TEXT NOT NULL DEFAULT '',
  assigned_actor TEXT NOT NULL DEFAULT '',
  assigned_at TEXT,
  UNIQUE(project_id, number)
);
CREATE TABLE IF NOT EXISTS comments (
  id TEXT PRIMARY KEY,
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  details TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS issue_labels (
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(issue_id, label)
);
CREATE TABLE IF NOT EXISTS issue_blockers (
  issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  blocker_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY(issue_id, blocker_id),
  CHECK(issue_id <> blocker_id)
);
CREATE INDEX IF NOT EXISTS issues_project_state_idx ON issues(project_id, state, parent_id, position);
CREATE INDEX IF NOT EXISTS issues_parent_idx ON issues(parent_id, position);
CREATE INDEX IF NOT EXISTS comments_issue_idx ON comments(issue_id, created_at);
CREATE INDEX IF NOT EXISTS audit_target_idx ON audit_log(target_type, target_id, created_at);
CREATE INDEX IF NOT EXISTS issue_labels_label_idx ON issue_labels(label, issue_id);
CREATE INDEX IF NOT EXISTS issue_blockers_blocker_idx ON issue_blockers(blocker_id, issue_id);
PRAGMA user_version = 2;
`)
		if err != nil {
			return fmt.Errorf("apply schema migration: %w", err)
		}
	case 1:
		// Version 1 databases are from the first vertical slice. Keep them
		// upgradeable in place instead of requiring users to discard issue data.
		for _, statement := range []string{
			"ALTER TABLE issues ADD COLUMN parent_id TEXT REFERENCES issues(id) ON DELETE SET NULL",
			"ALTER TABLE issues ADD COLUMN assignee TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE issues ADD COLUMN assigned_key_id TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE issues ADD COLUMN assigned_actor TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE issues ADD COLUMN assigned_at TEXT",
			`CREATE TABLE IF NOT EXISTS issue_labels (issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE, label TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(issue_id, label))`,
			`CREATE TABLE IF NOT EXISTS issue_blockers (issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE, blocker_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE, created_at TEXT NOT NULL, PRIMARY KEY(issue_id, blocker_id), CHECK(issue_id <> blocker_id))`,
			"DROP INDEX IF EXISTS issues_project_state_idx",
			"CREATE INDEX issues_project_state_idx ON issues(project_id, state, parent_id, position)",
			"CREATE INDEX IF NOT EXISTS issues_parent_idx ON issues(parent_id, position)",
			"CREATE INDEX IF NOT EXISTS issue_labels_label_idx ON issue_labels(label, issue_id)",
			"CREATE INDEX IF NOT EXISTS issue_blockers_blocker_idx ON issue_blockers(blocker_id, issue_id)",
			"PRAGMA user_version = 2",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("upgrade schema: %w", err)
			}
		}
	default:
		return fmt.Errorf("database schema version %d cannot be migrated", version)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	if err := validateSchemaContract(s.db, currentSchemaVersion); err != nil {
		return fmt.Errorf("validate migrated schema: %w", err)
	}
	return nil
}

func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:])
}

func generateSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "st_" + base64.RawURLEncoding.EncodeToString(buf)
}

func secretHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func (s *Store) KeyCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL").Scan(&count)
	return count, err
}

func (s *Store) BootstrapKey(ctx context.Context, name string) (APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "bootstrap"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL").Scan(&count); err != nil {
		return APIKey{}, err
	}
	if count != 0 {
		return APIKey{}, ErrAlreadyExists
	}
	now := time.Now().UTC()
	key := APIKey{ID: newID(), Name: name, CreatedAt: now, Secret: generateSecret()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO api_keys(id,name,secret_hash,created_at) VALUES(?,?,?,?)", key.ID, key.Name, secretHash(key.Secret), now.Format(time.RFC3339Nano)); err != nil {
		return APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, err
	}
	return key, nil
}

func (s *Store) CreateKey(ctx context.Context, name string) (APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, fmt.Errorf("key name is required")
	}
	now := time.Now().UTC()
	key := APIKey{ID: newID(), Name: name, CreatedAt: now, Secret: generateSecret()}
	_, err := s.db.ExecContext(ctx, "INSERT INTO api_keys(id,name,secret_hash,created_at) VALUES(?,?,?,?)", key.ID, key.Name, secretHash(key.Secret), now.Format(time.RFC3339Nano))
	return key, err
}

func (s *Store) Authenticate(ctx context.Context, secret string) (APIKey, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return APIKey{}, ErrUnauthorized
	}
	var key APIKey
	var created string
	var last, revoked sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT id,name,created_at,last_used_at,revoked_at FROM api_keys WHERE secret_hash=?", secretHash(secret)).Scan(&key.ID, &key.Name, &created, &last, &revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid {
		return APIKey{}, ErrUnauthorized
	}
	if err != nil {
		return APIKey{}, err
	}
	key.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return APIKey{}, err
	}
	if last.Valid {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, last.String); parseErr == nil {
			key.LastUsedAt = &parsed
		}
	}
	now := time.Now().UTC()
	key.LastUsedAt = &now
	_, _ = s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at=? WHERE id=?", now.Format(time.RFC3339Nano), key.ID)
	return key, nil
}

func (s *Store) ListKeys(ctx context.Context, includeRevoked bool) ([]APIKey, error) {
	query := "SELECT id,name,created_at,last_used_at,revoked_at FROM api_keys"
	if !includeRevoked {
		query += " WHERE revoked_at IS NULL"
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []APIKey
	for rows.Next() {
		var key APIKey
		var created string
		var last, revoked sql.NullString
		if err := rows.Scan(&key.ID, &key.Name, &created, &last, &revoked); err != nil {
			return nil, err
		}
		key.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		if last.Valid {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, last.String); parseErr == nil {
				key.LastUsedAt = &parsed
			}
		}
		if revoked.Valid {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, revoked.String); parseErr == nil {
				key.RevokedAt = &parsed
			}
		}
		result = append(result, key)
	}
	return result, rows.Err()
}

func (s *Store) RevokeKey(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, "UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now, id)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateProject(ctx context.Context, name, slug string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	if slug == "" {
		slug = slugify(name)
	} else {
		slug = slugify(slug)
	}
	if slug == "" {
		return Project{}, fmt.Errorf("project slug is required")
	}
	now := time.Now().UTC()
	project := Project{ID: newID(), Name: name, Slug: slug, CreatedAt: now}
	_, err := s.db.ExecContext(ctx, "INSERT INTO projects(id,name,slug,created_at) VALUES(?,?,?,?)", project.ID, project.Name, project.Slug, now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Project{}, ErrAlreadyExists
		}
		return Project{}, err
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,slug,created_at FROM projects ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		var p Project
		var created string
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &created); err != nil {
			return nil, err
		}
		p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, idOrSlug string) (Project, error) {
	var p Project
	var created string
	err := s.db.QueryRowContext(ctx, "SELECT id,name,slug,created_at FROM projects WHERE id=? OR slug=?", idOrSlug, idOrSlug).Scan(&p.ID, &p.Name, &p.Slug, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return p, err
}

func (s *Store) CreateIssue(ctx context.Context, projectID, title, body, keyID, actor, session string) (Issue, error) {
	return s.CreateIssueWithOptions(ctx, projectID, title, body, keyID, actor, session, IssueCreateOptions{})
}

func (s *Store) CreateIssueWithOptions(ctx context.Context, projectID, title, body, keyID, actor, session string, options IssueCreateOptions) (Issue, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Issue{}, fmt.Errorf("issue title is required")
	}
	options.Assignee = strings.TrimSpace(options.Assignee)
	labels := normalizeLabels(options.Labels)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback()
	var next int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM issues WHERE project_id=?", projectID).Scan(&next); err != nil {
		return Issue{}, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position),-1)+1 FROM issues WHERE project_id=?", projectID).Scan(&position); err != nil {
		return Issue{}, err
	}
	var projectSlug string
	if err := tx.QueryRowContext(ctx, "SELECT slug FROM projects WHERE id=?", projectID).Scan(&projectSlug); errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	} else if err != nil {
		return Issue{}, err
	}
	now := time.Now().UTC()
	issue := Issue{ID: newID(), ProjectID: projectID, ProjectSlug: projectSlug, Number: next, Title: title, Body: body, State: "open", CreatedAt: now, UpdatedAt: now, CreatorKeyID: keyID, CreatorActor: actor, CreatorSession: session, ParentID: strings.TrimSpace(options.ParentID), Position: position, Assignee: options.Assignee}
	if issue.ParentID != "" {
		if err := validateParentTx(ctx, tx, issue.ID, projectID, issue.ParentID); err != nil {
			return Issue{}, err
		}
		// A child starts at the end of its sibling list, not the end of the
		// project-wide list used by older databases.
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position),-1)+1 FROM issues WHERE project_id=? AND parent_id=?", projectID, issue.ParentID).Scan(&position); err != nil {
			return Issue{}, err
		}
		issue.Position = position
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO issues(id,project_id,number,title,body,state,created_at,updated_at,creator_key_id,creator_actor,creator_session,position,parent_id,assignee) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", issue.ID, projectID, next, title, body, "open", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), keyID, actor, session, position, nullableString(issue.ParentID), issue.Assignee); err != nil {
		return Issue{}, err
	}
	for _, label := range labels {
		if _, err := tx.ExecContext(ctx, "INSERT INTO issue_labels(issue_id,label,created_at) VALUES(?,?,?)", issue.ID, label, now.Format(time.RFC3339Nano)); err != nil {
			return Issue{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO audit_log(key_id,actor,session,operation,target_type,target_id,created_at,details) VALUES(?,?,?,?,?,?,?,?)", keyID, actor, session, "create", "issue", issue.ID, now.Format(time.RFC3339Nano), title); err != nil {
		return Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) GetIssue(ctx context.Context, idOrRef string) (Issue, error) {
	var issue Issue
	var created, updated, closed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT i.id,i.project_id,p.slug,i.number,i.title,i.body,i.state,i.created_at,i.updated_at,i.closed_at,i.creator_key_id,i.creator_actor,i.creator_session FROM issues i JOIN projects p ON p.id=i.project_id WHERE i.id=? OR (p.slug || '#' || i.number)=?`, idOrRef, idOrRef).Scan(&issue.ID, &issue.ProjectID, &issue.ProjectSlug, &issue.Number, &issue.Title, &issue.Body, &issue.State, &created, &updated, &closed, &issue.CreatorKeyID, &issue.CreatorActor, &issue.CreatorSession)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, err
	}
	issue.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String)
	if err != nil {
		return Issue{}, err
	}
	issue.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated.String)
	if err != nil {
		return Issue{}, err
	}
	if closed.Valid {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, closed.String); parseErr == nil {
			issue.ClosedAt = &parsed
		}
	}
	comments, err := s.comments(ctx, issue.ID)
	if err != nil {
		return Issue{}, err
	}
	issue.Comments = comments
	if err := s.hydrateIssue(ctx, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (s *Store) ListIssues(ctx context.Context, projectID string) ([]Issue, error) {
	return s.QueryIssues(ctx, IssueFilters{ProjectID: projectID})
}

func (s *Store) QueryIssues(ctx context.Context, filters IssueFilters) ([]Issue, error) {
	query := `SELECT i.id,i.project_id,p.slug,i.number,i.title,i.body,i.state,i.created_at,i.updated_at,i.closed_at,i.creator_key_id,i.creator_actor,i.creator_session
		FROM issues i JOIN projects p ON p.id=i.project_id`
	where := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if strings.TrimSpace(filters.ProjectID) != "" {
		where = append(where, "(i.project_id=? OR p.slug=?)")
		args = append(args, filters.ProjectID, filters.ProjectID)
	}
	if state := strings.TrimSpace(filters.State); state != "" {
		if state != "open" && state != "closed" {
			return nil, fmt.Errorf("invalid issue state %q", state)
		}
		where = append(where, "i.state=?")
		args = append(args, state)
	}
	if parent := strings.TrimSpace(filters.Parent); parent != "" {
		if parent == "none" || parent == "root" {
			where = append(where, "i.parent_id IS NULL")
		} else {
			parentID, err := s.resolveIssueID(ctx, parent)
			if err != nil {
				return nil, err
			}
			where = append(where, "i.parent_id=?")
			args = append(args, parentID)
		}
	}
	if label := strings.TrimSpace(filters.Label); label != "" {
		where = append(where, "EXISTS (SELECT 1 FROM issue_labels il WHERE il.issue_id=i.id AND il.label=?)")
		args = append(args, label)
	}
	switch strings.ToLower(strings.TrimSpace(filters.Assigned)) {
	case "assigned":
		where = append(where, "i.assignee <> ''")
	case "unassigned":
		where = append(where, "i.assignee = ''")
	case "":
	default:
		return nil, fmt.Errorf("invalid assigned filter %q", filters.Assigned)
	}
	if assignee := strings.TrimSpace(filters.Assignee); assignee != "" {
		where = append(where, "i.assignee=?")
		args = append(args, assignee)
	}
	if text := strings.TrimSpace(filters.Text); text != "" {
		like := "%" + strings.ToLower(text) + "%"
		where = append(where, "(LOWER(i.title) LIKE ? OR LOWER(i.body) LIKE ?)")
		args = append(args, like, like)
	}
	if filters.UpdatedSince != nil {
		where = append(where, "i.updated_at >= ?")
		args = append(args, filters.UpdatedSince.UTC().Format(time.RFC3339Nano))
	}
	if filters.UpdatedUntil != nil {
		where = append(where, "i.updated_at <= ?")
		args = append(args, filters.UpdatedUntil.UTC().Format(time.RFC3339Nano))
	}
	if filters.Age > 0 {
		cutoff := time.Now().UTC().Add(-filters.Age)
		where = append(where, "i.updated_at <= ?")
		args = append(args, cutoff.Format(time.RFC3339Nano))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY i.position,i.number,i.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Issue, 0)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range result {
		if err := s.hydrateIssue(ctx, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type scanner interface{ Scan(...any) error }

func scanIssue(row scanner) (Issue, error) {
	var issue Issue
	var created, updated, closed sql.NullString
	err := row.Scan(&issue.ID, &issue.ProjectID, &issue.ProjectSlug, &issue.Number, &issue.Title, &issue.Body, &issue.State, &created, &updated, &closed, &issue.CreatorKeyID, &issue.CreatorActor, &issue.CreatorSession)
	if err != nil {
		return Issue{}, err
	}
	var parseErr error
	issue.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, created.String)
	if parseErr != nil {
		return Issue{}, parseErr
	}
	issue.UpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updated.String)
	if parseErr != nil {
		return Issue{}, parseErr
	}
	if closed.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, closed.String); err == nil {
			issue.ClosedAt = &parsed
		}
	}
	return issue, nil
}

func (s *Store) UpdateIssue(ctx context.Context, id string, title, body *string, keyID, actor, session string) (Issue, error) {
	issue, err := s.GetIssue(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	if title == nil && body == nil {
		return issue, nil
	}
	newTitle, newBody := issue.Title, issue.Body
	if title != nil {
		newTitle = strings.TrimSpace(*title)
		if newTitle == "" {
			return Issue{}, fmt.Errorf("issue title is required")
		}
	}
	if body != nil {
		newBody = *body
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET title=?,body=?,updated_at=? WHERE id=?", newTitle, newBody, now.Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "update", "issue", issue.ID, ""); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) SetIssueState(ctx context.Context, id, state, keyID, actor, session string) (Issue, error) {
	if state != "open" && state != "closed" {
		return Issue{}, ErrInvalidTransition
	}
	issue, err := s.GetIssue(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	if issue.State == state {
		return issue, nil
	}
	now := time.Now().UTC()
	var closed any
	if state == "closed" {
		closed = now.Format(time.RFC3339Nano)
	} else {
		closed = nil
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET state=?,closed_at=?,updated_at=? WHERE id=?", state, closed, now.Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, state, "issue", issue.ID, ""); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) AddComment(ctx context.Context, issueID, body, keyID, actor, session string) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, fmt.Errorf("comment body is required")
	}
	if _, err := s.GetIssue(ctx, issueID); err != nil {
		return Comment{}, err
	}
	now := time.Now().UTC()
	comment := Comment{ID: newID(), Body: body, CreatedAt: now, KeyID: keyID, Actor: actor, Session: session}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO comments(id,issue_id,body,created_at,key_id,actor,session) VALUES(?,?,?,?,?,?,?)", comment.ID, issueID, body, now.Format(time.RFC3339Nano), keyID, actor, session); err != nil {
		return Comment{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET updated_at=? WHERE id=?", now.Format(time.RFC3339Nano), issueID); err != nil {
		return Comment{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "comment", "issue", issueID, ""); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

func (s *Store) comments(ctx context.Context, issueID string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,body,created_at,key_id,actor,session FROM comments WHERE issue_id=? ORDER BY created_at,id", issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Comment, 0)
	for rows.Next() {
		var c Comment
		var created string
		if err := rows.Scan(&c.ID, &c.Body, &created, &c.KeyID, &c.Actor, &c.Session); err != nil {
			return nil, err
		}
		c.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func normalizeLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	result := make([]string, 0, len(labels))
	for _, raw := range labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		result = append(result, label)
	}
	sort.Strings(result)
	return result
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Store) resolveIssueID(ctx context.Context, idOrRef string) (string, error) {
	issue, err := s.GetIssue(ctx, idOrRef)
	if err != nil {
		return "", err
	}
	return issue.ID, nil
}

func validateParentTx(ctx context.Context, tx *sql.Tx, issueID, projectID, parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	if parentID == issueID {
		return fmt.Errorf("an issue cannot be its own parent")
	}
	var parentProject string
	if err := tx.QueryRowContext(ctx, "SELECT project_id FROM issues WHERE id=?", parentID).Scan(&parentProject); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if parentProject != projectID {
		return fmt.Errorf("parent issue must belong to the same project")
	}
	current := parentID
	seen := map[string]struct{}{issueID: {}}
	for current != "" {
		if _, ok := seen[current]; ok {
			return fmt.Errorf("parent relationship would create a cycle")
		}
		seen[current] = struct{}{}
		var next sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT parent_id FROM issues WHERE id=?", current).Scan(&next); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		current = ""
		if next.Valid {
			current = next.String
		}
	}
	return nil
}

func (s *Store) hydrateIssue(ctx context.Context, issue *Issue) error {
	var parent, assignedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT parent_id,assignee,assigned_key_id,assigned_actor,assigned_at,position FROM issues WHERE id=?", issue.ID).Scan(&parent, &issue.Assignee, &issue.AssignedKeyID, &issue.AssignedActor, &assignedAt, &issue.Position); err != nil {
		return err
	}
	if parent.Valid {
		issue.ParentID = parent.String
		parentLinks, err := s.issueLinks(ctx, `SELECT p.id,p.project_id,pr.slug,p.number,p.title,p.state,p.position
			FROM issues p JOIN projects pr ON pr.id=p.project_id WHERE p.id=?`, parent.String)
		if err != nil {
			return err
		}
		if len(parentLinks) > 0 {
			issue.Parent = &parentLinks[0]
		}
	}
	issue.AssignedTo = issue.Assignee
	if assignedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, assignedAt.String)
		if err != nil {
			return err
		}
		issue.AssignedAt = &parsed
	}
	rows, err := s.db.QueryContext(ctx, "SELECT label FROM issue_labels WHERE issue_id=? ORDER BY label", issue.ID)
	if err != nil {
		return err
	}
	issue.Labels = make([]string, 0)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			rows.Close()
			return err
		}
		issue.Labels = append(issue.Labels, label)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	issue.Blockers, err = s.issueLinks(ctx, `SELECT b.id,b.project_id,p.slug,b.number,b.title,b.state,b.position
		FROM issue_blockers ib JOIN issues b ON b.id=ib.blocker_id JOIN projects p ON p.id=b.project_id
		WHERE ib.issue_id=? ORDER BY b.position,b.number,b.id`, issue.ID)
	if err != nil {
		return err
	}
	issue.Blocked = false
	for _, blocker := range issue.Blockers {
		if blocker.State == "open" {
			issue.Blocked = true
			break
		}
	}
	issue.BlockedBy = issue.Blockers
	issue.BlockedIssues, err = s.issueLinks(ctx, `SELECT i.id,i.project_id,p.slug,i.number,i.title,i.state,i.position
			FROM issue_blockers ib JOIN issues i ON i.id=ib.issue_id JOIN projects p ON p.id=i.project_id
			WHERE ib.blocker_id=? ORDER BY i.position,i.number,i.id`, issue.ID)
	if err != nil {
		return err
	}
	issue.Children, err = s.issueLinks(ctx, `SELECT c.id,c.project_id,p.slug,c.number,c.title,c.state,c.position
		FROM issues c JOIN projects p ON p.id=c.project_id WHERE c.parent_id=? ORDER BY c.position,c.number,c.id`, issue.ID)
	return err
}

func (s *Store) issueLinks(ctx context.Context, query string, issueID string) ([]IssueLink, error) {
	rows, err := s.db.QueryContext(ctx, query, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]IssueLink, 0)
	for rows.Next() {
		var link IssueLink
		if err := rows.Scan(&link.ID, &link.ProjectID, &link.ProjectSlug, &link.Number, &link.Title, &link.State, &link.Position); err != nil {
			return nil, err
		}
		result = append(result, link)
	}
	return result, rows.Err()
}

func (s *Store) AddLabel(ctx context.Context, issueID, label, keyID, actor, session string) ([]string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, fmt.Errorf("label is required")
	}
	if _, err := s.GetIssue(ctx, issueID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO issue_labels(issue_id,label,created_at) VALUES(?,?,?)", issueID, label, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err := s.touchIssue(ctx, issueID, keyID, actor, session, "label_add", label); err != nil {
		return nil, err
	}
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return issue.Labels, nil
}

func (s *Store) RemoveLabel(ctx context.Context, issueID, label, keyID, actor, session string) ([]string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, fmt.Errorf("label is required")
	}
	if _, err := s.GetIssue(ctx, issueID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM issue_labels WHERE issue_id=? AND label=?", issueID, label); err != nil {
		return nil, err
	}
	if err := s.touchIssue(ctx, issueID, keyID, actor, session, "label_remove", label); err != nil {
		return nil, err
	}
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return issue.Labels, nil
}

func (s *Store) AssignIssue(ctx context.Context, issueID, assignee, keyID, actor, session string) (Issue, error) {
	if _, err := s.GetIssue(ctx, issueID); err != nil {
		return Issue{}, err
	}
	now := time.Now().UTC()
	assignee = strings.TrimSpace(assignee)
	var assignedAt any
	assignedKeyID, assignedActor := keyID, actor
	if assignee != "" {
		assignedAt = now.Format(time.RFC3339Nano)
	} else {
		assignedKeyID, assignedActor = "", ""
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET assignee=?,assigned_key_id=?,assigned_actor=?,assigned_at=?,updated_at=? WHERE id=?", assignee, assignedKeyID, assignedActor, assignedAt, now.Format(time.RFC3339Nano), issueID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "assign", "issue", issueID, assignee); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issueID)
}

func (s *Store) SetParent(ctx context.Context, issueID, parentID, keyID, actor, session string) (Issue, error) {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return Issue{}, err
	}
	parentID = strings.TrimSpace(parentID)
	if parentID != "" {
		parentID, err = s.resolveIssueID(ctx, parentID)
		if err != nil {
			return Issue{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback()
	if err := validateParentTx(ctx, tx, issue.ID, issue.ProjectID, parentID); err != nil {
		return Issue{}, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position),-1)+1 FROM issues WHERE project_id=? AND parent_id IS ?", issue.ProjectID, nullableString(parentID)).Scan(&position); err != nil {
		return Issue{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET parent_id=?,position=?,updated_at=? WHERE id=?", nullableString(parentID), position, now.Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "parent", "issue", issue.ID, parentID); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) AddBlocker(ctx context.Context, issueID, blockerID, keyID, actor, session string) (Issue, error) {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return Issue{}, err
	}
	blockerID, err = s.resolveIssueID(ctx, blockerID)
	if err != nil {
		return Issue{}, err
	}
	if issue.ID == blockerID {
		return Issue{}, fmt.Errorf("an issue cannot block itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback()
	var cycle int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE reach(id) AS (
		SELECT blocker_id FROM issue_blockers WHERE issue_id=?
		UNION
		SELECT ib.blocker_id FROM issue_blockers ib JOIN reach r ON ib.issue_id=r.id
	) SELECT 1 FROM reach WHERE id=? LIMIT 1`, blockerID, issue.ID).Scan(&cycle)
	if err == nil {
		return Issue{}, fmt.Errorf("blocker relationship would create a cycle")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Issue{}, err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, "INSERT INTO issue_blockers(issue_id,blocker_id,created_at) VALUES(?,?,?)", issue.ID, blockerID, now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Issue{}, ErrAlreadyExists
		}
		return Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET updated_at=? WHERE id=?", now.Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "blocker_add", "issue", issue.ID, blockerID); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) RemoveBlocker(ctx context.Context, issueID, blockerID, keyID, actor, session string) (Issue, error) {
	if _, err := s.GetIssue(ctx, issueID); err != nil {
		return Issue{}, err
	}
	blockerID, err := s.resolveIssueID(ctx, blockerID)
	if err != nil {
		return Issue{}, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM issue_blockers WHERE issue_id=? AND blocker_id=?", issueID, blockerID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "blocker_remove", "issue", issueID, blockerID); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issueID)
}

func (s *Store) SetIssuePosition(ctx context.Context, issueID string, position int64, keyID, actor, session string) (Issue, error) {
	if position < 0 {
		return Issue{}, fmt.Errorf("position must be non-negative")
	}
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return Issue{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET position=?,updated_at=? WHERE id=?", position, time.Now().UTC().Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "reorder", "issue", issue.ID, fmt.Sprintf("%d", position)); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) ReorderIssues(ctx context.Context, parentID string, issueIDs []string, keyID, actor, session string) error {
	parentID = strings.TrimSpace(parentID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for position, id := range issueIDs {
		var actualParent sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT parent_id FROM issues WHERE id=?", id).Scan(&actualParent); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if actualParent.String != parentID || (!actualParent.Valid && parentID != "") {
			return fmt.Errorf("all issues must be siblings")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE issues SET position=?,updated_at=? WHERE id=?", position, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, id := range issueIDs {
		if err := s.audit(ctx, keyID, actor, session, "reorder", "issue", id, ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Frontier(ctx context.Context, filters IssueFilters) ([]Issue, error) {
	filters.State = "open"
	filters.Assigned = "unassigned"
	issues, err := s.QueryIssues(ctx, filters)
	if err != nil {
		return nil, err
	}
	result := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if !issue.Blocked {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (s *Store) touchIssue(ctx context.Context, issueID, keyID, actor, session, operation, details string) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET updated_at=? WHERE id=?", now.Format(time.RFC3339Nano), issueID); err != nil {
		return err
	}
	return s.audit(ctx, keyID, actor, session, operation, "issue", issueID, details)
}

func (s *Store) audit(ctx context.Context, keyID, actor, session, operation, targetType, targetID, details string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO audit_log(key_id,actor,session,operation,target_type,target_id,created_at,details) VALUES(?,?,?,?,?,?,?,?)", keyID, actor, session, operation, targetType, targetID, time.Now().UTC().Format(time.RFC3339Nano), details)
	return err
}

func (s *Store) AuditFor(ctx context.Context, targetID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,key_id,actor,session,operation,target_type,target_id,created_at,details FROM audit_log WHERE target_id=? ORDER BY created_at,id", targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var created string
		if err := rows.Scan(&a.ID, &a.KeyID, &a.Actor, &a.Session, &a.Operation, &a.TargetType, &a.TargetID, &created, &a.Details); err != nil {
			return nil, err
		}
		a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
