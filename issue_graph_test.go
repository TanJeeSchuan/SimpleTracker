package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestIssueGraphAndFrontier(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "graph")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Graph", "graph")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Parent", "", key.ID, "actor", "session", IssueCreateOptions{Labels: []string{"triage"}})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := store.CreateIssue(t.Context(), project.ID, "Blocker", "", key.ID, "actor", "session")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateIssueWithOptions(t.Context(), project.ID, "Child", "", key.ID, "actor", "session", IssueCreateOptions{ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddBlocker(t.Context(), child.ID, blocker.ID, key.ID, "actor", "session"); err != nil {
		t.Fatal(err)
	}
	frontier, err := store.Frontier(t.Context(), IssueFilters{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 2 || frontier[0].Title != "Parent" || frontier[1].Title != "Blocker" {
		t.Fatalf("unexpected frontier: %#v", frontier)
	}
	if _, err := store.AddBlocker(t.Context(), blocker.ID, child.ID, key.ID, "actor", "session"); err == nil {
		t.Fatal("expected blocker cycle to be rejected")
	}
	if _, err := store.SetParent(t.Context(), parent.ID, child.ID, key.ID, "actor", "session"); err == nil {
		t.Fatal("expected parent cycle to be rejected")
	}
	if _, err := store.AssignIssue(t.Context(), blocker.ID, "worker", key.ID, "actor", "session"); err != nil {
		t.Fatal(err)
	}
	frontier, err = store.Frontier(t.Context(), IssueFilters{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 1 || frontier[0].Title != "Parent" {
		t.Fatalf("unexpected assigned frontier: %#v", frontier)
	}
	if _, err := store.SetIssueState(t.Context(), blocker.ID, "closed", key.ID, "actor", "session"); err != nil {
		t.Fatal(err)
	}
	frontier, err = store.Frontier(t.Context(), IssueFilters{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 2 {
		t.Fatalf("closed blocker should unblock child: %#v", frontier)
	}
}

func TestIssueGraphAPI(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "api")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	project := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/projects", key.Secret, map[string]string{"name": "API graph"}, http.StatusCreated)
	projectSlug := project["slug"].(string)
	parent := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/projects/"+projectSlug+"/issues", key.Secret, map[string]any{"title": "Parent", "labels": []string{"review"}}, http.StatusCreated)
	child := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/projects/"+projectSlug+"/issues", key.Secret, map[string]any{"title": "Child"}, http.StatusCreated)
	childID := child["id"].(string)
	parentID := parent["id"].(string)
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/issues/"+childID+"/parent", key.Secret, map[string]string{"parent_id": parentID}, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/issues/"+childID+"/assign", key.Secret, map[string]string{"assignee": "agent"}, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodDelete, server.URL+"/api/v1/issues/"+childID+"/assign", key.Secret, nil, http.StatusOK)
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/v1/issues/"+childID+"/labels", key.Secret, map[string]string{"label": "urgent"}, http.StatusOK)
	issue := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/issues/"+childID, key.Secret, nil, http.StatusOK)
	if issue["parent_id"] != parentID || issue["blocked"] != false {
		t.Fatalf("unexpected graph issue: %#v", issue)
	}
	if response := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/frontier", key.Secret, nil, http.StatusOK); response["frontier"] == nil {
		t.Fatalf("frontier response missing: %#v", response)
	}
}
