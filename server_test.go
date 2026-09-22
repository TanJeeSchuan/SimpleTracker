package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestPublicAPIAndPersistence(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "state")
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.BootstrapKey(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()

	projects := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/projects", key.Secret, nil, http.StatusOK)
	if got := len(projects["projects"].([]any)); got != 0 {
		t.Fatalf("expected no projects, got %d", got)
	}
	project := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/projects", key.Secret, map[string]string{"name": "Demo Repository"}, http.StatusCreated)
	projectID := project["id"].(string)
	projectSlug := project["slug"].(string)
	issue := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/projects/"+projectSlug+"/issues", key.Secret, map[string]string{"title": "First issue", "body": "**Markdown**"}, http.StatusCreated)
	issueID := issue["id"].(string)
	if issue["number"].(float64) != 1 || issue["state"] != "open" {
		t.Fatalf("unexpected issue response: %#v", issue)
	}
	if issue["url"] != server.URL+"/projects/"+projectSlug+"/issues/1" {
		t.Fatalf("unexpected canonical issue URL: %#v", issue["url"])
	}
	requestJSON(t, server.Client(), http.MethodPatch, server.URL+"/api/v1/issues/"+issueID, key.Secret, map[string]string{"body": "edited"}, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/issues/"+issueID+"/comments", key.Secret, map[string]string{"body": "done soon"}, http.StatusCreated)
	closed := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/issues/"+issueID+"/close", key.Secret, nil, http.StatusOK)
	if closed["state"] != "closed" {
		t.Fatalf("expected closed issue, got %#v", closed["state"])
	}
	requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/issues/"+issueID, key.Secret, nil, http.StatusOK)

	if got := projectID; got == "" {
		t.Fatal("project id should be stable and non-empty")
	}
	store.Close()
	reopenedStore, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopenedServer := httptest.NewServer(NewServer(reopenedStore, "", "test").Handler())
	defer reopenedServer.Close()
	reopened := requestJSON(t, reopenedServer.Client(), http.MethodGet, reopenedServer.URL+"/api/v1/issues/"+issueID, key.Secret, nil, http.StatusOK)
	if reopened["id"] != issueID || reopened["state"] != "closed" {
		t.Fatalf("state did not survive restart: %#v", reopened)
	}
}

func TestKeyRevocationAndBrowserBoundary(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.BootstrapKey(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateKey(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/projects", second.Secret, nil, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/keys/"+second.ID+"/revoke", first.Secret, nil, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/projects", second.Secret, nil, http.StatusUnauthorized)
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Get(server.URL + "/projects/demo/issues/1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/login" {
		t.Fatalf("expected unauthenticated browser redirect, got %d %s", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestBrowserLoginAndIssueDetail(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Browser project", "browser")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(t.Context(), project.ID, "Visible issue", "# markdown", key.ID, "agent-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	form := bytes.NewBufferString("api_key=" + key.Secret)
	loginRequest, err := http.NewRequest(http.MethodPost, server.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse, err := client.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("login returned %d", loginResponse.StatusCode)
	}
	cookie := loginResponse.Header.Get("Set-Cookie")
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/projects/browser/issues/1", nil)
	request.Header.Set("Cookie", cookie)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("Visible issue")) || issue.Number != 1 {
		t.Fatalf("issue detail page missing content: status=%d body=%s", response.StatusCode, data)
	}
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint, key string, body any, status int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s returned %d, want %d: %s", method, endpoint, response.StatusCode, status, data)
	}
	var value map[string]any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
