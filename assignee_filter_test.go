package main

import (
	"path/filepath"
	"testing"
)

func TestQueryIssuesAssigneeFiltersActualAssigneeOnly(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, err := store.BootstrapKey(t.Context(), "assignee-filter")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), "Assignee filter", "assignee-filter")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(t.Context(), project.ID, "Assigned work", "", key.ID, "creator", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignIssue(t.Context(), issue.ID, "actual-assignee", key.ID, "attribution-actor", "assignment-session"); err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"attribution-actor", key.ID} {
		matches, err := store.QueryIssues(t.Context(), IssueFilters{ProjectID: project.ID, Assignee: value})
		if err != nil {
			t.Fatalf("query assignee %q: %v", value, err)
		}
		if len(matches) != 0 {
			t.Fatalf("assignee filter %q matched attribution metadata: %#v", value, matches)
		}
	}

	matches, err := store.QueryIssues(t.Context(), IssueFilters{ProjectID: project.ID, Assignee: "actual-assignee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != issue.ID {
		t.Fatalf("actual assignee did not match: %#v", matches)
	}
}
