package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	sessionToken := mustBrowserSession(t, store, key.ID)
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
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: sessionToken})
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
		"data-filter-updated-since",
		"data-filter-updated-until",
		"data-filter-age",
		"data-board-message",
		"/assets/browser.css",
		"/assets/board.js",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("board response missing %q: %s", want, body)
		}
	}
}

func TestBrowserInteractionAcceptance(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "browser acceptance")
	if err != nil {
		t.Fatal(err)
	}
	sessionToken := mustBrowserSession(t, store, key.ID)
	project, err := store.CreateProject(t.Context(), "Acceptance", "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := store.CreateIssue(t.Context(), project.ID, "Parent lane", "", key.ID, "browser", "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateIssueWithOptions(t.Context(), project.ID, "First child", "first", key.ID, "browser", "acceptance", IssueCreateOptions{ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Second child", "second", key.ID, "browser", "acceptance", IssueCreateOptions{ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := store.CreateIssue(t.Context(), project.ID, "Loose issue", "", key.ID, "browser", "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	if _, err := store.writeDB.ExecContext(t.Context(), "UPDATE issues SET updated_at=? WHERE id=?", old.Format(time.RFC3339Nano), loose.ID); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	client := server.Client()

	// The board is the browser's grouping surface. Confirm the initial lane and
	// card order before driving the same position endpoint used by drag/drop.
	board := browserPage(t, client, server.URL+"/projects/acceptance", sessionToken, http.StatusOK)
	if !strings.Contains(board, "Parent lane") || !strings.Contains(board, "Unparented issues") {
		t.Fatalf("board did not render graph lanes: %s", board)
	}
	if firstIndex, secondIndex := strings.Index(board, "First child"), strings.Index(board, "Second child"); firstIndex == -1 || secondIndex == -1 || firstIndex > secondIndex {
		t.Fatalf("unexpected initial card order: first=%d second=%d", firstIndex, secondIndex)
	}

	// A drag reorder persists sibling positions, not a transient DOM order.
	browserJSON(t, client, http.MethodPost, server.URL+"/api/v1/issues/"+second.ID+"/position", sessionToken, `{"position":0}`, http.StatusOK)
	browserJSON(t, client, http.MethodPost, server.URL+"/api/v1/issues/"+first.ID+"/position", sessionToken, `{"position":1}`, http.StatusOK)
	var reordered struct {
		Issues []Issue `json:"issues"`
	}
	decodeBrowserJSON(t, client, server.URL+"/api/v1/projects/acceptance/issues?parent="+parent.ID, sessionToken, http.StatusOK, &reordered)
	if len(reordered.Issues) != 2 || reordered.Issues[0].ID != second.ID || reordered.Issues[1].ID != first.ID {
		t.Fatalf("reorder did not persist through issue query: %#v", reordered.Issues)
	}
	board = browserPage(t, client, server.URL+"/projects/acceptance", sessionToken, http.StatusOK)
	if secondIndex, firstIndex := strings.Index(board, "Second child"), strings.Index(board, "First child"); secondIndex == -1 || firstIndex == -1 || secondIndex > firstIndex {
		t.Fatalf("board did not reflect persisted reorder: second=%d first=%d", secondIndex, firstIndex)
	}

	// The secondary list sends the API's updated/age filters, including the
	// RFC3339 values produced from its datetime-local controls.
	now := time.Now().UTC()
	filterURL := server.URL + "/api/v1/projects/acceptance/issues?state=open&updated_since=" + now.Add(-24*time.Hour).Format(time.RFC3339) + "&updated_until=" + now.Add(24*time.Hour).Format(time.RFC3339)
	var filtered struct {
		Issues []Issue `json:"issues"`
	}
	decodeBrowserJSON(t, client, filterURL, sessionToken, http.StatusOK, &filtered)
	if len(filtered.Issues) != 3 {
		t.Fatalf("updated filter interaction returned %#v", filtered.Issues)
	}
	var aged struct {
		Issues []Issue `json:"issues"`
	}
	decodeBrowserJSON(t, client, server.URL+"/api/v1/projects/acceptance/issues?state=open&age=168h", sessionToken, http.StatusOK, &aged)
	if len(aged.Issues) != 1 || aged.Issues[0].ID != loose.ID {
		t.Fatalf("age filter interaction returned %#v", aged.Issues)
	}

	// Drive the issue detail's edit flow through the same authenticated API
	// calls its forms use, then verify the rendered detail reflects server state.
	browserJSON(t, client, http.MethodPatch, server.URL+"/api/v1/issues/"+first.ID, sessionToken, `{"title":"Edited child","body":"## Updated","assignee":"operator","labels":["review"],"parent_id":"`+parent.ID+`"}`, http.StatusOK)
	browserJSON(t, client, http.MethodPost, server.URL+"/api/v1/issues/"+first.ID+"/comments", sessionToken, `{"body":"Browser update"}`, http.StatusCreated)
	browserJSON(t, client, http.MethodPost, server.URL+"/api/v1/issues/"+first.ID+"/close", sessionToken, "", http.StatusOK)
	detail := browserPage(t, client, server.URL+"/projects/acceptance/issues/2", sessionToken, http.StatusOK)
	for _, want := range []string{"Edited child", "## Updated", "Browser update", "review", "operator", "status-done"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail interaction missing %q: %s", want, detail)
		}
	}
}

func browserPage(t *testing.T, client *http.Client, endpoint, token string, expectedStatus int) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: token})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s returned %d, want %d: %s", endpoint, response.StatusCode, expectedStatus, data)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func browserJSON(t *testing.T, client *http.Client, method, endpoint, token, body string, expectedStatus int) map[string]any {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: token})
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s returned %d, want %d: %s", method, endpoint, response.StatusCode, expectedStatus, data)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func decodeBrowserJSON(t *testing.T, client *http.Client, endpoint, token string, expectedStatus int, target any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: token})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s returned %d, want %d: %s", endpoint, response.StatusCode, expectedStatus, data)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
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
	sessionToken := mustBrowserSession(t, store, key.ID)
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
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: sessionToken})
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
		"/assets/detail.js",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail response missing %q: %s", want, body)
		}
	}
}

func mustBrowserSession(t *testing.T, store *Store, keyID string) string {
	t.Helper()
	token, _, err := store.CreateBrowserSession(t.Context(), keyID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
