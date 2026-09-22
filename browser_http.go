package main

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
	key, err := s.Store.Authenticate(r.Context(), r.FormValue("api_key"))
	if err != nil {
		writeHTML(w, http.StatusUnauthorized, loginPageWithError)
		return
	}
	token, expires, err := s.Store.CreateBrowserSession(r.Context(), key.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create browser session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     browserSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) secureCookies(r *http.Request) bool {
	return r.TLS != nil || strings.HasPrefix(strings.ToLower(s.BaseURL), "https://")
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
	cookie, err := r.Cookie(browserSessionCookie)
	if err != nil {
		return APIKey{}, false
	}
	key, err := s.Store.AuthenticateBrowserSession(r.Context(), cookie.Value)
	return key, err == nil
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

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
