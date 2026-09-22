package main

import "time"

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Secret     string     `json:"secret,omitempty"`
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

type Issue struct {
	ID             string      `json:"id"`
	ProjectID      string      `json:"project_id"`
	ProjectSlug    string      `json:"project_slug,omitempty"`
	Number         int64       `json:"number"`
	Title          string      `json:"title"`
	Body           string      `json:"body"`
	State          string      `json:"state"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	ClosedAt       *time.Time  `json:"closed_at,omitempty"`
	CreatorKeyID   string      `json:"creator_key_id,omitempty"`
	CreatorActor   string      `json:"creator_actor,omitempty"`
	CreatorSession string      `json:"creator_session,omitempty"`
	URL            string      `json:"url,omitempty"`
	Comments       []Comment   `json:"comments,omitempty"`
	Labels         []string    `json:"labels,omitempty"`
	ParentID       string      `json:"parent_id,omitempty"`
	Parent         *IssueLink  `json:"parent,omitempty"`
	Position       int64       `json:"position"`
	Assignee       string      `json:"assignee,omitempty"`
	AssignedTo     string      `json:"assigned_to,omitempty"`
	AssignedKeyID  string      `json:"assigned_key_id,omitempty"`
	AssignedActor  string      `json:"assigned_actor,omitempty"`
	AssignedAt     *time.Time  `json:"assigned_at,omitempty"`
	Blocked        bool        `json:"blocked"`
	Blockers       []IssueLink `json:"blockers,omitempty"`
	BlockedBy      []IssueLink `json:"blocked_by,omitempty"`
	Children       []IssueLink `json:"children,omitempty"`
}

// IssueLink is the compact graph representation embedded in an issue read.
// Keeping links shallow prevents a parent/child graph from recursively
// expanding into an unbounded JSON response.
type IssueLink struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectSlug string `json:"project_slug,omitempty"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Position    int64  `json:"position"`
	Blocked     bool   `json:"blocked"`
}

type IssueCreateOptions struct {
	ParentID string
	Labels   []string
	Assignee string
}

type IssueFilters struct {
	ProjectID    string
	State        string
	Parent       string
	Label        string
	Assigned     string
	Assignee     string
	Text         string
	UpdatedSince *time.Time
	UpdatedUntil *time.Time
	Age          time.Duration
}

type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	KeyID     string    `json:"key_id,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Session   string    `json:"session,omitempty"`
}

type AuditEntry struct {
	ID         int64     `json:"id"`
	KeyID      string    `json:"key_id"`
	Actor      string    `json:"actor,omitempty"`
	Session    string    `json:"session,omitempty"`
	Operation  string    `json:"operation"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	CreatedAt  time.Time `json:"created_at"`
	Details    string    `json:"details,omitempty"`
}

type AuthContext struct {
	Key     APIKey
	Actor   string
	Session string
}
