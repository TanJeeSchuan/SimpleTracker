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
	"strings"
	"time"
)

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
	err := s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL").Scan(&count)
	return count, err
}

func (s *Store) BootstrapKey(ctx context.Context, name string) (APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "bootstrap"
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
	_, err := s.writeDB.ExecContext(ctx, "INSERT INTO api_keys(id,name,secret_hash,created_at) VALUES(?,?,?,?)", key.ID, key.Name, secretHash(key.Secret), now.Format(time.RFC3339Nano))
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
	err := s.readDB.QueryRowContext(ctx, "SELECT id,name,created_at,last_used_at,revoked_at FROM api_keys WHERE secret_hash=?", secretHash(secret)).Scan(&key.ID, &key.Name, &created, &last, &revoked)
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
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= keyLastUsedWriteInterval {
		cutoff := now.Add(-keyLastUsedWriteInterval).Format(time.RFC3339Nano)
		result, updateErr := s.writeDB.ExecContext(ctx, "UPDATE api_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<=?)", now.Format(time.RFC3339Nano), key.ID, cutoff)
		if updateErr == nil {
			if changed, rowsErr := result.RowsAffected(); rowsErr == nil && changed > 0 {
				key.LastUsedAt = &now
			}
		}
	}
	return key, nil
}

func (s *Store) ListKeys(ctx context.Context, includeRevoked bool) ([]APIKey, error) {
	query := "SELECT id,name,created_at,last_used_at,revoked_at FROM api_keys"
	if !includeRevoked {
		query += " WHERE revoked_at IS NULL"
	}
	query += " ORDER BY created_at, id"
	rows, err := s.readDB.QueryContext(ctx, query)
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
	res, err := s.writeDB.ExecContext(ctx, "UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now, id)
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
	_, err := s.writeDB.ExecContext(ctx, "INSERT INTO projects(id,name,slug,created_at) VALUES(?,?,?,?)", project.ID, project.Name, project.Slug, now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Project{}, ErrAlreadyExists
		}
		return Project{}, err
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id,name,slug,created_at FROM projects ORDER BY created_at,id")
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
	err := s.readDB.QueryRowContext(ctx, "SELECT id,name,slug,created_at FROM projects WHERE id=? OR slug=?", idOrSlug, idOrSlug).Scan(&p.ID, &p.Name, &p.Slug, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return p, err
}
