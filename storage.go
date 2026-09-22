package main

import (
	"context"
	"database/sql"
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

const currentSchemaVersion = 3

const keyLastUsedWriteInterval = 5 * time.Minute

type Store struct {
	readDB  *sql.DB
	writeDB *sql.DB
	dbPath  string
}

func OpenStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data directory is required")
	}
	if err := ensureDir(dataDir); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "tracker.db")
	writeDB, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	store := &Store{writeDB: writeDB, dbPath: dbPath}
	if err := store.migrate(); err != nil {
		writeDB.Close()
		return nil, err
	}
	readDB, err := sql.Open("sqlite", sqliteDSN(dbPath, true))
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open sqlite read pool: %w", err)
	}
	readDB.SetMaxOpenConns(16)
	readDB.SetMaxIdleConns(16)
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		writeDB.Close()
		return nil, fmt.Errorf("start sqlite read pool: %w", err)
	}
	store.readDB = readDB
	return store, nil
}

func sqliteDSN(path string, readOnly bool) string {
	pragmas := "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	if readOnly {
		pragmas += "&_pragma=query_only(1)"
	}
	return path + pragmas
}

func ensureDir(path string) error {
	// Kept separate so callers and tests can create a data directory without
	// reaching into storage implementation details.
	return mkdirAll(path)
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.readDB != nil {
		first = s.readDB.Close()
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.readDB == nil || s.writeDB == nil {
		return errors.New("store is not open")
	}
	if err := s.readDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite read pool: %w", err)
	}
	if err := s.writeDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite write connection: %w", err)
	}
	return nil
}

// Backup creates a standalone SQLite backup while the store is serving. The
// VACUUM INTO operation runs from a consistent read transaction, including
// any committed changes in the WAL, and produces a database that can be
// opened without the live database's -wal/-shm sidecars.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if s == nil || s.writeDB == nil {
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

	if _, err := s.writeDB.ExecContext(ctx, "VACUUM INTO ?", temporaryPath); err != nil {
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
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
	err := s.readDB.QueryRowContext(ctx, `SELECT i.id,i.project_id,p.slug,i.number,i.title,i.body,i.state,i.created_at,i.updated_at,i.closed_at,i.creator_key_id,i.creator_actor,i.creator_session FROM issues i JOIN projects p ON p.id=i.project_id WHERE i.id=? OR (p.slug || '#' || i.number)=?`, idOrRef, idOrRef).Scan(&issue.ID, &issue.ProjectID, &issue.ProjectSlug, &issue.Number, &issue.Title, &issue.Body, &issue.State, &created, &updated, &closed, &issue.CreatorKeyID, &issue.CreatorActor, &issue.CreatorSession)
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
	rows, err := s.readDB.QueryContext(ctx, query, args...)
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
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET title=?,body=?,updated_at=? WHERE id=?", newTitle, newBody, now.Format(time.RFC3339Nano), issue.ID); err != nil {
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
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET state=?,closed_at=?,updated_at=? WHERE id=?", state, closed, now.Format(time.RFC3339Nano), issue.ID); err != nil {
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
	if _, err := s.writeDB.ExecContext(ctx, "INSERT INTO comments(id,issue_id,body,created_at,key_id,actor,session) VALUES(?,?,?,?,?,?,?)", comment.ID, issueID, body, now.Format(time.RFC3339Nano), keyID, actor, session); err != nil {
		return Comment{}, err
	}
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET updated_at=? WHERE id=?", now.Format(time.RFC3339Nano), issueID); err != nil {
		return Comment{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "comment", "issue", issueID, ""); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

func (s *Store) comments(ctx context.Context, issueID string) ([]Comment, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id,body,created_at,key_id,actor,session FROM comments WHERE issue_id=? ORDER BY created_at,id", issueID)
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
	if err := s.readDB.QueryRowContext(ctx, "SELECT parent_id,assignee,assigned_key_id,assigned_actor,assigned_at,position FROM issues WHERE id=?", issue.ID).Scan(&parent, &issue.Assignee, &issue.AssignedKeyID, &issue.AssignedActor, &assignedAt, &issue.Position); err != nil {
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
	rows, err := s.readDB.QueryContext(ctx, "SELECT label FROM issue_labels WHERE issue_id=? ORDER BY label", issue.ID)
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
	rows, err := s.readDB.QueryContext(ctx, query, issueID)
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
	_, err := s.writeDB.ExecContext(ctx, "INSERT OR IGNORE INTO issue_labels(issue_id,label,created_at) VALUES(?,?,?)", issueID, label, now.Format(time.RFC3339Nano))
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
	if _, err := s.writeDB.ExecContext(ctx, "DELETE FROM issue_labels WHERE issue_id=? AND label=?", issueID, label); err != nil {
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
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET assignee=?,assigned_key_id=?,assigned_actor=?,assigned_at=?,updated_at=? WHERE id=?", assignee, assignedKeyID, assignedActor, assignedAt, now.Format(time.RFC3339Nano), issueID); err != nil {
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
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
	if _, err := s.writeDB.ExecContext(ctx, "DELETE FROM issue_blockers WHERE issue_id=? AND blocker_id=?", issueID, blockerID); err != nil {
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
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET position=?,updated_at=? WHERE id=?", position, time.Now().UTC().Format(time.RFC3339Nano), issue.ID); err != nil {
		return Issue{}, err
	}
	if err := s.audit(ctx, keyID, actor, session, "reorder", "issue", issue.ID, fmt.Sprintf("%d", position)); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, issue.ID)
}

func (s *Store) ReorderIssues(ctx context.Context, parentID string, issueIDs []string, keyID, actor, session string) error {
	parentID = strings.TrimSpace(parentID)
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE issues SET updated_at=? WHERE id=?", now.Format(time.RFC3339Nano), issueID); err != nil {
		return err
	}
	return s.audit(ctx, keyID, actor, session, operation, "issue", issueID, details)
}

func (s *Store) audit(ctx context.Context, keyID, actor, session, operation, targetType, targetID, details string) error {
	_, err := s.writeDB.ExecContext(ctx, "INSERT INTO audit_log(key_id,actor,session,operation,target_type,target_id,created_at,details) VALUES(?,?,?,?,?,?,?,?)", keyID, actor, session, operation, targetType, targetID, time.Now().UTC().Format(time.RFC3339Nano), details)
	return err
}

func (s *Store) AuditFor(ctx context.Context, targetID string) ([]AuditEntry, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id,key_id,actor,session,operation,target_type,target_id,created_at,details FROM audit_log WHERE target_id=? ORDER BY created_at,id", targetID)
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
