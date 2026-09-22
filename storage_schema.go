package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

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

var schemaColumnsV3 = func() map[string][]string {
	columns := make(map[string][]string, len(schemaColumnsV2)+1)
	for table, names := range schemaColumnsV2 {
		columns[table] = names
	}
	columns["browser_sessions"] = []string{"session_hash", "key_id", "created_at", "expires_at"}
	return columns
}()

func requiredSchemaColumns(version int) (map[string][]string, bool) {
	switch version {
	case 1:
		return schemaColumnsV1, true
	case 2:
		return schemaColumnsV2, true
	case currentSchemaVersion:
		return schemaColumnsV3, true
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

var schemaContractV3 = func() sqliteSchemaContract {
	contract := sqliteSchemaContract{
		notNull:  cloneStringSliceMap(schemaContractV2.notNull),
		defaults: cloneNestedStringMap(schemaContractV2.defaults),
		primary:  cloneStringSliceMap(schemaContractV2.primary),
		unique:   cloneNestedStringSlicesMap(schemaContractV2.unique),
		foreign:  cloneForeignKeyMap(schemaContractV2.foreign),
		checks:   cloneStringSliceMap(schemaContractV2.checks),
		indexes:  append([]sqliteIndexContract(nil), schemaContractV2.indexes...),
	}
	contract.notNull["browser_sessions"] = []string{"key_id", "created_at", "expires_at"}
	contract.primary["browser_sessions"] = []string{"session_hash"}
	contract.foreign["browser_sessions"] = []sqliteForeignKeyContract{{from: "key_id", to: "id", table: "api_keys", onDelete: "cascade"}}
	contract.indexes = append(contract.indexes, sqliteIndexContract{table: "browser_sessions", name: "browser_sessions_expires_idx", columns: []string{"expires_at"}})
	return contract
}()

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(source)+1)
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func cloneNestedStringMap(source map[string]map[string]string) map[string]map[string]string {
	clone := make(map[string]map[string]string, len(source)+1)
	for key, values := range source {
		inner := make(map[string]string, len(values))
		for name, value := range values {
			inner[name] = value
		}
		clone[key] = inner
	}
	return clone
}

func cloneNestedStringSlicesMap(source map[string][][]string) map[string][][]string {
	clone := make(map[string][][]string, len(source)+1)
	for key, groups := range source {
		for _, group := range groups {
			clone[key] = append(clone[key], append([]string(nil), group...))
		}
	}
	return clone
}

func cloneForeignKeyMap(source map[string][]sqliteForeignKeyContract) map[string][]sqliteForeignKeyContract {
	clone := make(map[string][]sqliteForeignKeyContract, len(source)+1)
	for key, values := range source {
		clone[key] = append([]sqliteForeignKeyContract(nil), values...)
	}
	return clone
}

func schemaContract(version int) (sqliteSchemaContract, bool) {
	switch version {
	case 1:
		return schemaContractV1, true
	case 2:
		return schemaContractV2, true
	case currentSchemaVersion:
		return schemaContractV3, true
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
	if err := s.writeDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary supports", version)
	}
	if version == currentSchemaVersion {
		if err := validateSchemaContract(s.writeDB, version); err != nil {
			return fmt.Errorf("validate schema: %w", err)
		}
		return nil
	}

	// Every migration runs in one transaction. SQLite supports transactional
	// DDL, so a failed migration cannot leave a database half-upgraded with a
	// user_version that still claims it is ready to serve.
	tx, err := s.writeDB.Begin()
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
CREATE TABLE IF NOT EXISTS browser_sessions (
  session_hash TEXT PRIMARY KEY,
  key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS issues_project_state_idx ON issues(project_id, state, parent_id, position);
CREATE INDEX IF NOT EXISTS issues_parent_idx ON issues(parent_id, position);
CREATE INDEX IF NOT EXISTS comments_issue_idx ON comments(issue_id, created_at);
CREATE INDEX IF NOT EXISTS audit_target_idx ON audit_log(target_type, target_id, created_at);
CREATE INDEX IF NOT EXISTS issue_labels_label_idx ON issue_labels(label, issue_id);
CREATE INDEX IF NOT EXISTS issue_blockers_blocker_idx ON issue_blockers(blocker_id, issue_id);
CREATE INDEX IF NOT EXISTS browser_sessions_expires_idx ON browser_sessions(expires_at);
PRAGMA user_version = 3;
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
			`CREATE TABLE IF NOT EXISTS browser_sessions (session_hash TEXT PRIMARY KEY, key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE, created_at TEXT NOT NULL, expires_at TEXT NOT NULL)`,
			"CREATE INDEX IF NOT EXISTS browser_sessions_expires_idx ON browser_sessions(expires_at)",
			"PRAGMA user_version = 3",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("upgrade schema: %w", err)
			}
		}
	case 2:
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS browser_sessions (session_hash TEXT PRIMARY KEY, key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE, created_at TEXT NOT NULL, expires_at TEXT NOT NULL)`,
			"CREATE INDEX IF NOT EXISTS browser_sessions_expires_idx ON browser_sessions(expires_at)",
			"PRAGMA user_version = 3",
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
	if err := validateSchemaContract(s.writeDB, currentSchemaVersion); err != nil {
		return fmt.Errorf("validate migrated schema: %w", err)
	}
	return nil
}
