package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserProjectBoardGroupsGraphAndDerivedColumns(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Wayfinder", "wayfinder")
	if err != nil {
		t.Fatal(err)
	}
	mapIssue, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Wayfinder map", "map body", key.ID, "agent", "session", IssueCreateOptions{Labels: []string{"wayfinder:map"}})
	if err != nil {
		t.Fatal(err)
	}
	unclaimed, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Unclaimed child", "", key.ID, "agent", "session", IssueCreateOptions{ParentID: mapIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Assigned child", "", key.ID, "agent", "session", IssueCreateOptions{ParentID: mapIssue.ID, Assignee: "worker-1"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := store.CreateIssue(t.Context(), project.ID, "Open blocker", "", key.ID, "agent", "session")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Blocked child", "", key.ID, "agent", "session", IssueCreateOptions{ParentID: mapIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddBlocker(t.Context(), blocked.ID, blocker.ID, key.ID, "agent", "session"); err != nil {
		t.Fatal(err)
	}
	done, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Done child", "", key.ID, "agent", "session", IssueCreateOptions{ParentID: mapIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetIssueState(t.Context(), done.ID, "closed", key.ID, "agent", "session"); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateIssue(t.Context(), project.ID, "Loose issue", "", key.ID, "agent", "session")
	if err != nil {
		t.Fatal(err)
	}
	_ = unclaimed
	_ = assigned
	_ = blocked
	_ = done
	_ = root

	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/projects/wayfinder", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "tracker_key", Value: key.Secret})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"Swimlane board",
		"Wayfinder map",
		"Unparented issues",
		`data-column="unclaimed"`,
		`data-column="in-progress"`,
		`data-column="blocked"`,
		`data-column="done"`,
		"Issue list",
		"data-filter-search",
		"/position",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("board response missing %q: %s", want, body)
		}
	}
}

func TestBrowserIssueDetailExposesEditingAndRelationships(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Detail", "detail")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(t.Context(), project.ID, "Editable", "# Markdown detail", key.ID, "agent", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(t.Context(), issue.ID, "A useful update", key.ID, "agent", "session"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/projects/detail/issues/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "tracker_key", Value: key.Secret})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"Markdown description",
		"# Markdown detail",
		"Comments",
		"A useful update",
		"Assignment & labels",
		"Parent & children",
		"Blockers",
		"data-issue-form",
		"data-toggle-state",
		"/comments",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail response missing %q: %s", want, body)
		}
	}
}
