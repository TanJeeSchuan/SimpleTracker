package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Store   *Store
	BaseURL string
	Version string
	Logger  *log.Logger
}

func NewServer(store *Store, baseURL, version string) *Server {
	return &Server{Store: store, BaseURL: strings.TrimRight(baseURL, "/"), Version: version, Logger: log.Default()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/api/v1/", s.api)
	mux.HandleFunc("/", s.browser)
	return requestLogger(mux, s.Logger)
}

func requestLogger(next http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if logger != nil {
			logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
		}
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	version := s.Version
	if version == "" {
		version = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "api": "v1"})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/bootstrap" {
		s.bootstrapHTTP(w, r)
		return
	}
	auth, ok := s.authenticateRequest(w, r)
	if !ok {
		return
	}
	ctx := context.WithValue(r.Context(), authContextKey{}, auth)
	r = r.WithContext(ctx)
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == "" || path == "/" {
		writeJSON(w, http.StatusOK, map[string]any{"name": "simpletracker", "api": "v1"})
		return
	}
	if path == "/keys" {
		s.keys(w, r)
		return
	}
	if strings.HasPrefix(path, "/keys/") {
		s.keyAction(w, r, strings.TrimPrefix(path, "/keys/"))
		return
	}
	if path == "/projects" {
		s.projects(w, r)
		return
	}
	if path == "/issues" {
		s.queryIssues(w, r, "")
		return
	}
	if path == "/frontier" {
		s.frontier(w, r, "")
		return
	}
	if strings.HasPrefix(path, "/projects/") {
		s.projectRoutes(w, r, strings.TrimPrefix(path, "/projects/"))
		return
	}
	if strings.HasPrefix(path, "/issues/") {
		s.issueRoutes(w, r, strings.TrimPrefix(path, "/issues/"))
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "route not found")
}

type authContextKey struct{}

func authFromRequest(r *http.Request) AuthContext {
	if auth, ok := r.Context().Value(authContextKey{}).(AuthContext); ok {
		return auth
	}
	return AuthContext{}
}

func (s *Server) authenticateRequest(w http.ResponseWriter, r *http.Request) (AuthContext, bool) {
	secret := ""
	if value := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(value), "bearer ") {
		secret = strings.TrimSpace(value[len("Bearer "):])
	}
	if secret == "" {
		secret = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}
	if secret == "" {
		if cookie, err := r.Cookie("tracker_key"); err == nil {
			secret = cookie.Value
		}
	}
	key, err := s.Store.Authenticate(r.Context(), secret)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="simpletracker"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid API key is required")
		return AuthContext{}, false
	}
	return AuthContext{Key: key, Actor: strings.TrimSpace(r.Header.Get("X-Actor-ID")), Session: strings.TrimSpace(r.Header.Get("X-Session-ID"))}, true
}

func (s *Server) bootstrapHTTP(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "forbidden", "bootstrap is available only from localhost; use the CLI for remote setup")
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r.Body, &input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	key, err := s.Store.BootstrapKey(r.Context(), input.Name)
	if errors.Is(err, ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "already_initialized", "an active API key already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := s.Store.ListKeys(r.Context(), r.URL.Query().Get("include_revoked") == "true")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	case http.MethodPost:
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		key, err := s.Store.CreateKey(r.Context(), input.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, key)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) keyAction(w http.ResponseWriter, r *http.Request, ref string) {
	parts := splitPath(ref)
	if len(parts) != 2 || parts[1] != "revoke" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "key route not found")
		return
	}
	if err := s.Store.RevokeKey(r.Context(), parts[0]); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "API key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": parts[0], "revoked": true})
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.Store.ListProjects(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
	case http.MethodPost:
		var input struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		project, err := s.Store.CreateProject(r.Context(), input.Name, input.Slug)
		if errors.Is(err, ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "already_exists", "project slug already exists")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		auth := authFromRequest(r)
		_ = s.Store.audit(r.Context(), auth.Key.ID, auth.Actor, auth.Session, "create", "project", project.ID, project.Name)
		writeJSON(w, http.StatusCreated, project)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) projectRoutes(w http.ResponseWriter, r *http.Request, ref string) {
	parts := splitPath(ref)
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	project, err := s.Store.GetProject(r.Context(), parts[0])
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, project)
		return
	}
	if len(parts) == 2 && parts[1] == "frontier" {
		s.frontier(w, r, project.Slug)
		return
	}
	if parts[1] != "issues" {
		writeError(w, http.StatusNotFound, "not_found", "project route not found")
		return
	}
	if len(parts) == 2 {
		if r.Method == http.MethodGet {
			s.queryIssues(w, r, project.Slug)
			return
		}
		if r.Method == http.MethodPost {
			var input struct {
				Title    string   `json:"title"`
				Body     string   `json:"body"`
				ParentID string   `json:"parent_id"`
				Labels   []string `json:"labels"`
				Assignee string   `json:"assignee"`
			}
			if err := decodeJSON(r.Body, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
				return
			}
			auth := authFromRequest(r)
			issue, err := s.Store.CreateIssueWithOptions(r.Context(), project.ID, input.Title, input.Body, auth.Key.ID, auth.Actor, auth.Session, IssueCreateOptions{ParentID: input.ParentID, Labels: input.Labels, Assignee: input.Assignee})
			if errors.Is(err, ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "project not found")
				return
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			s.decorateIssue(r, &issue)
			writeJSON(w, http.StatusCreated, issue)
			return
		}
	}
	if len(parts) == 3 && isNumber(parts[2]) {
		issue, err := s.Store.GetIssue(r.Context(), project.Slug+"#"+parts[2])
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "issue not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		s.issueWithMethod(w, r, issue)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "project route not found")
}

func (s *Server) issueRoutes(w http.ResponseWriter, r *http.Request, ref string) {
	parts := splitPath(ref)
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "issue not found")
		return
	}
	issue, err := s.Store.GetIssue(r.Context(), parts[0])
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "issue not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(parts) == 1 {
		s.issueWithMethod(w, r, issue)
		return
	}
	auth := authFromRequest(r)
	switch parts[1] {
	case "close", "reopen":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		state := "closed"
		if parts[1] == "reopen" {
			state = "open"
		}
		updated, err := s.Store.SetIssueState(r.Context(), issue.ID, state, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.decorateIssue(r, &updated)
		writeJSON(w, http.StatusOK, updated)
	case "comments":
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"comments": issue.Comments})
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		var input struct {
			Body string `json:"body"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		comment, err := s.Store.AddComment(r.Context(), issue.ID, input.Body, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, comment)
	case "labels":
		s.issueLabels(w, r, issue, parts[2:], auth)
	case "assign":
		if r.Method == http.MethodDelete {
			updated, err := s.Store.AssignIssue(r.Context(), issue.ID, "", auth.Key.ID, auth.Actor, auth.Session)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			s.decorateIssue(r, &updated)
			writeJSON(w, http.StatusOK, updated)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		var input struct {
			Assignee string `json:"assignee"`
			Assigned string `json:"assigned_to"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if strings.TrimSpace(input.Assignee) == "" {
			input.Assignee = input.Assigned
		}
		updated, err := s.Store.AssignIssue(r.Context(), issue.ID, input.Assignee, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.decorateIssue(r, &updated)
		writeJSON(w, http.StatusOK, updated)
	case "parent":
		if r.Method == http.MethodDelete {
			updated, err := s.Store.SetParent(r.Context(), issue.ID, "", auth.Key.ID, auth.Actor, auth.Session)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			s.decorateIssue(r, &updated)
			writeJSON(w, http.StatusOK, updated)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		var input struct {
			ParentID string `json:"parent_id"`
			Parent   string `json:"parent"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if strings.TrimSpace(input.ParentID) == "" {
			input.ParentID = input.Parent
		}
		updated, err := s.Store.SetParent(r.Context(), issue.ID, input.ParentID, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.decorateIssue(r, &updated)
		writeJSON(w, http.StatusOK, updated)
	case "blockers":
		s.issueBlockers(w, r, issue, parts[2:], auth)
	case "position", "order", "reorder":
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		var input struct {
			Position int64 `json:"position"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		updated, err := s.Store.SetIssuePosition(r.Context(), issue.ID, input.Position, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.decorateIssue(r, &updated)
		writeJSON(w, http.StatusOK, updated)
	case "audit":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		entries, err := s.Store.AuditFor(r.Context(), issue.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
	default:
		writeError(w, http.StatusNotFound, "not_found", "issue route not found")
	}
}

func (s *Server) issueWithMethod(w http.ResponseWriter, r *http.Request, issue Issue) {
	auth := authFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		s.decorateIssue(r, &issue)
		writeJSON(w, http.StatusOK, issue)
	case http.MethodPatch, http.MethodPut:
		var input struct {
			Title    *string   `json:"title"`
			Body     *string   `json:"body"`
			ParentID *string   `json:"parent_id"`
			Assignee *string   `json:"assignee"`
			Labels   *[]string `json:"labels"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		updated, err := s.Store.UpdateIssue(r.Context(), issue.ID, input.Title, input.Body, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if input.ParentID != nil {
			updated, err = s.Store.SetParent(r.Context(), updated.ID, *input.ParentID, auth.Key.ID, auth.Actor, auth.Session)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		if input.Assignee != nil {
			updated, err = s.Store.AssignIssue(r.Context(), updated.ID, *input.Assignee, auth.Key.ID, auth.Actor, auth.Session)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		if input.Labels != nil {
			desired := normalizeLabels(*input.Labels)
			keep := make(map[string]struct{}, len(desired))
			for _, label := range desired {
				keep[label] = struct{}{}
			}
			for _, label := range updated.Labels {
				if _, ok := keep[label]; !ok {
					if _, err := s.Store.RemoveLabel(r.Context(), updated.ID, label, auth.Key.ID, auth.Actor, auth.Session); err != nil {
						s.writeStoreError(w, err)
						return
					}
				}
			}
			updated, err = s.Store.GetIssue(r.Context(), updated.ID)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			have := make(map[string]struct{}, len(updated.Labels))
			for _, label := range updated.Labels {
				have[label] = struct{}{}
			}
			for _, label := range desired {
				if _, ok := have[label]; !ok {
					if _, err := s.Store.AddLabel(r.Context(), updated.ID, label, auth.Key.ID, auth.Actor, auth.Session); err != nil {
						s.writeStoreError(w, err)
						return
					}
				}
			}
			updated, err = s.Store.GetIssue(r.Context(), updated.ID)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		s.decorateIssue(r, &updated)
		writeJSON(w, http.StatusOK, updated)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) queryIssues(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	filters, err := parseIssueFilters(r, project)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	issues, err := s.Store.QueryIssues(r.Context(), filters)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	for i := range issues {
		s.decorateIssue(r, &issues[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	filters, err := parseIssueFilters(r, project)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	issues, err := s.Store.Frontier(r.Context(), filters)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	for i := range issues {
		s.decorateIssue(r, &issues[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"frontier": issues, "issues": issues})
}

func parseIssueFilters(r *http.Request, project string) (IssueFilters, error) {
	query := r.URL.Query()
	if strings.TrimSpace(project) == "" {
		project = strings.TrimSpace(query.Get("project"))
	}
	filters := IssueFilters{ProjectID: project, State: strings.TrimSpace(query.Get("state"))}
	if filters.State == "" {
		filters.State = strings.TrimSpace(query.Get("status"))
	}
	filters.Parent = strings.TrimSpace(query.Get("parent"))
	filters.Label = strings.TrimSpace(query.Get("label"))
	filters.Assigned = strings.TrimSpace(query.Get("assigned"))
	if filters.Assigned == "" {
		filters.Assigned = strings.TrimSpace(query.Get("assignment"))
	}
	filters.Assignee = strings.TrimSpace(query.Get("assignee"))
	filters.Text = strings.TrimSpace(query.Get("q"))
	if filters.Text == "" {
		filters.Text = strings.TrimSpace(query.Get("search"))
	}
	parseTime := func(name string) (*time.Time, error) {
		value := strings.TrimSpace(query.Get(name))
		if value == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", name, err)
		}
		return &parsed, nil
	}
	var err error
	filters.UpdatedSince, err = parseTime("updated_since")
	if err != nil {
		return IssueFilters{}, err
	}
	if filters.UpdatedSince == nil {
		filters.UpdatedSince, err = parseTime("updated_after")
		if err != nil {
			return IssueFilters{}, err
		}
	}
	filters.UpdatedUntil, err = parseTime("updated_until")
	if err != nil {
		return IssueFilters{}, err
	}
	if filters.UpdatedUntil == nil {
		filters.UpdatedUntil, err = parseTime("updated_before")
		if err != nil {
			return IssueFilters{}, err
		}
	}
	if age := strings.TrimSpace(query.Get("age")); age != "" {
		filters.Age, err = time.ParseDuration(age)
		if err != nil {
			if days, parseErr := strconv.Atoi(age); parseErr == nil && days >= 0 {
				filters.Age = time.Duration(days) * 24 * time.Hour
			} else {
				return IssueFilters{}, fmt.Errorf("invalid age: %w", err)
			}
		}
	}
	return filters, nil
}

func (s *Server) issueLabels(w http.ResponseWriter, r *http.Request, issue Issue, suffix []string, auth AuthContext) {
	if len(suffix) > 1 {
		writeError(w, http.StatusNotFound, "not_found", "label route not found")
		return
	}
	if r.Method == http.MethodGet && len(suffix) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"labels": issue.Labels})
		return
	}
	label := ""
	if len(suffix) == 1 {
		label = suffix[0]
	}
	if r.Method == http.MethodPost {
		var input struct {
			Label  string   `json:"label"`
			Labels []string `json:"labels"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if label == "" {
			label = input.Label
		}
		if label != "" {
			input.Labels = append(input.Labels, label)
		}
		for _, value := range input.Labels {
			if _, err := s.Store.AddLabel(r.Context(), issue.ID, value, auth.Key.ID, auth.Actor, auth.Session); err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		updated, err := s.Store.GetIssue(r.Context(), issue.ID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"labels": updated.Labels})
		return
	}
	if r.Method == http.MethodDelete && label == "" {
		var input struct {
			Label string `json:"label"`
		}
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		label = input.Label
	}
	if r.Method == http.MethodDelete && label != "" {
		labels, err := s.Store.RemoveLabel(r.Context(), issue.ID, label, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) issueBlockers(w http.ResponseWriter, r *http.Request, issue Issue, suffix []string, auth AuthContext) {
	if len(suffix) > 1 {
		writeError(w, http.StatusNotFound, "not_found", "blocker route not found")
		return
	}
	if r.Method == http.MethodGet && len(suffix) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"blockers": issue.Blockers, "blocked": issue.Blocked, "blocked_issues": issue.BlockedIssues})
		return
	}
	var input struct {
		BlockerID string `json:"blocker_id"`
		ID        string `json:"id"`
	}
	if r.Method == http.MethodPost {
		if err := decodeJSON(r.Body, &input); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if strings.TrimSpace(input.BlockerID) == "" {
			input.BlockerID = input.ID
		}
		updated, err := s.Store.AddBlocker(r.Context(), issue.ID, input.BlockerID, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}
	if r.Method == http.MethodDelete {
		if len(suffix) == 1 {
			input.BlockerID = suffix[0]
		} else if err := decodeJSON(r.Body, &input); err != nil && !errors.Is(err, io.EOF) {
			s.writeStoreError(w, err)
			return
		}
		if strings.TrimSpace(input.BlockerID) == "" {
			input.BlockerID = input.ID
		}
		updated, err := s.Store.RemoveBlocker(r.Context(), issue.ID, input.BlockerID, auth.Key.ID, auth.Actor, auth.Session)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "invalid_request"
	switch {
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrAlreadyExists):
		status, code = http.StatusConflict, "already_exists"
	case errors.Is(err, ErrInvalidTransition):
		status, code = http.StatusConflict, "invalid_transition"
	}
	writeError(w, status, code, err.Error())
}

func (s *Server) decorateIssue(r *http.Request, issue *Issue) {
	base := s.BaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	issue.URL = fmt.Sprintf("%s/projects/%s/issues/%d", base, url.PathEscape(issue.ProjectSlug), issue.Number)
}

func splitPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if decoded, err := url.PathUnescape(part); err == nil {
			part = decoded
		}
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func isNumber(value string) bool {
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func isLoopback(remote string) bool {
	if strings.TrimSpace(remote) == "" {
		// An empty address is used by in-process handlers and is local by
		// definition; network servers always provide a peer address.
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeHTML(w, http.StatusOK, loginPage)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.Store.Authenticate(r.Context(), r.FormValue("api_key")); err != nil {
		writeHTML(w, http.StatusUnauthorized, loginPageWithError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "tracker_key", Value: r.FormValue("api_key"), HttpOnly: true, SameSite: http.SameSiteLaxMode, Path: "/"})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) browser(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		if _, ok := s.browserAuth(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		projects, err := s.Store.ListProjects(r.Context())
		if err != nil {
			writeHTML(w, http.StatusInternalServerError, "<h1>Storage error</h1>")
			return
		}
		writeHTML(w, http.StatusOK, renderProjectIndex(projects))
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/"))
	if len(parts) == 2 && parts[0] == "projects" {
		if _, ok := s.browserAuth(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		project, err := s.Store.GetProject(r.Context(), parts[1])
		if errors.Is(err, ErrNotFound) {
			writeHTML(w, http.StatusNotFound, "<h1>Project not found</h1>")
			return
		}
		if err != nil {
			writeHTML(w, http.StatusInternalServerError, "<h1>Storage error</h1>")
			return
		}
		issues, err := s.Store.QueryIssues(r.Context(), IssueFilters{ProjectID: project.ID})
		if err != nil {
			writeHTML(w, http.StatusInternalServerError, "<h1>Storage error</h1>")
			return
		}
		writeHTML(w, http.StatusOK, renderProjectBoard(project, issues))
		return
	}
	if len(parts) == 4 && parts[0] == "projects" && parts[2] == "issues" && isNumber(parts[3]) {
		if _, ok := s.browserAuth(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		issue, err := s.Store.GetIssue(r.Context(), parts[1]+"#"+parts[3])
		if errors.Is(err, ErrNotFound) {
			writeHTML(w, http.StatusNotFound, "<h1>Issue not found</h1>")
			return
		}
		if err != nil {
			writeHTML(w, http.StatusInternalServerError, "<h1>Storage error</h1>")
			return
		}
		writeHTML(w, http.StatusOK, renderInteractiveIssuePage(issue))
		return
	}
	writeHTML(w, http.StatusNotFound, "<h1>Not found</h1>")
}

func (s *Server) browserAuth(r *http.Request) (APIKey, bool) {
	cookie, err := r.Cookie("tracker_key")
	if err != nil {
		return APIKey{}, false
	}
	key, err := s.Store.Authenticate(r.Context(), cookie.Value)
	return key, err == nil
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

var loginPage = `<!doctype html><meta charset="utf-8"><title>SimpleTracker sign in</title><style>body{font:16px system-ui;max-width:36rem;margin:8rem auto;padding:1rem;background:#f6f7f9}form{display:grid;gap:.75rem}input,button{font:inherit;padding:.7rem}button{cursor:pointer}</style><h1>SimpleTracker</h1><p>Use an API key to continue.</p><form method="post"><label>API key <input name="api_key" type="password" autocomplete="off" required></label><button>Sign in</button></form>`
var loginPageWithError = `<!doctype html><meta charset="utf-8"><title>SimpleTracker sign in</title><style>body{font:16px system-ui;max-width:36rem;margin:8rem auto;padding:1rem;background:#f6f7f9}form{display:grid;gap:.75rem}input,button{font:inherit;padding:.7rem}button{cursor:pointer}.error{color:#a00}</style><h1>SimpleTracker</h1><p class="error">That API key was not accepted.</p><form method="post"><label>API key <input name="api_key" type="password" autocomplete="off" required></label><button>Sign in</button></form>`

func renderProjectIndex(projects []Project) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>SimpleTracker</title><style>body{font:16px system-ui;max-width:56rem;margin:3rem auto;padding:1rem}a{color:#1769aa}.project{padding:1rem;border:1px solid #ddd;border-radius:.5rem;margin:.5rem 0}.project a{display:block;text-decoration:none;color:inherit}</style><h1>Projects</h1>`)
	if len(projects) == 0 {
		b.WriteString("<p>No projects yet.</p>")
	}
	for _, project := range projects {
		b.WriteString(`<div class="project"><a href="/projects/` + template.HTMLEscapeString(url.PathEscape(project.Slug)) + `"><strong>` + template.HTMLEscapeString(project.Name) + `</strong> <code>` + template.HTMLEscapeString(project.Slug) + `</code><small> Open swimlane board →</small></a></div>`)
	}
	return b.String()
}

func renderIssuePage(issue Issue) string {
	return renderInteractiveIssuePage(issue)
}
