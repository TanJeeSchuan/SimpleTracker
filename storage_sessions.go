package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const browserSessionLifetime = 30 * 24 * time.Hour

func (s *Store) CreateBrowserSession(ctx context.Context, keyID string) (string, time.Time, error) {
	now := time.Now().UTC()
	expires := now.Add(browserSessionLifetime)
	token := "sts_" + strings.TrimPrefix(generateSecret(), "st_")
	result, err := s.writeDB.ExecContext(ctx, `
INSERT INTO browser_sessions(session_hash,key_id,created_at,expires_at)
SELECT ?,id,?,? FROM api_keys WHERE id=? AND revoked_at IS NULL`, secretHash(token), now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), keyID)
	if err != nil {
		return "", time.Time{}, err
	}
	created, err := result.RowsAffected()
	if err != nil || created != 1 {
		return "", time.Time{}, ErrUnauthorized
	}
	_, _ = s.writeDB.ExecContext(ctx, "DELETE FROM browser_sessions WHERE expires_at<=?", now.Format(time.RFC3339Nano))
	return token, expires, nil
}

func (s *Store) AuthenticateBrowserSession(ctx context.Context, token string) (APIKey, error) {
	if token == "" {
		return APIKey{}, ErrUnauthorized
	}
	var key APIKey
	var created, last, revoked sql.NullString
	err := s.readDB.QueryRowContext(ctx, `
SELECT k.id,k.name,k.created_at,k.last_used_at,k.revoked_at
FROM browser_sessions s
JOIN api_keys k ON k.id=s.key_id
WHERE s.session_hash=? AND s.expires_at>? AND k.revoked_at IS NULL`, secretHash(token), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&key.ID, &key.Name, &created, &last, &revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid {
		return APIKey{}, ErrUnauthorized
	}
	if err != nil {
		return APIKey{}, err
	}
	key.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	if last.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, last.String)
		if parseErr == nil {
			key.LastUsedAt = &parsed
		}
	}
	return key, nil
}
