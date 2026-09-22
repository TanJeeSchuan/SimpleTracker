package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMattPocockSkillsCompatibility exercises the tracker-facing contract in
// docs/agents/issue-tracker.md at the HTTP seam used by the CLI. It keeps the
// workflow proof provider-neutral: every map, ticket, triage action, and
// review lookup is an ordinary project or issue operation.
func TestMattPocockSkillsCompatibility(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := store.BootstrapKey(t.Context(), "skills-compatibility")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", "test").Handler())
	defer server.Close()
	client := server.Client()
	request := func(method, path string, body any, status int) any {
		t.Helper()
		return skillsRequestJSON(t, client, method, server.URL+path, key.Secret, "compat-agent", "compat-session", body, status)
	}

	project := request(http.MethodPost, "/api/v1/projects", map[string]string{
		"name": "Skills compatibility",
		"slug": "skills-compat",
	}, http.StatusCreated).(map[string]any)
	projectSlug := project["slug"].(string)

	// to-spec: one ordinary issue is the spec and ready-for-agent is its state
	// role. The returned project-number reference is stable for later flows.
	spec := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":  "Spec: searchable work queue",
		"body":   "## Problem Statement\nAgents need a deterministic frontier.",
		"labels": []string{"ready-for-agent", "enhancement"},
	}, http.StatusCreated).(map[string]any)
	specID := spec["id"].(string)
	specRef := projectSlug + "#" + strconv.FormatInt(int64(spec["number"].(float64)), 10)
	byRef := request(http.MethodGet, "/api/v1/issues/"+urlPath(specRef), nil, http.StatusOK).(map[string]any)
	if byRef["id"] != specID || byRef["body"] != spec["body"] {
		t.Fatalf("stable issue reference did not resolve the originating spec: %#v", byRef)
	}

	// to-tickets: children are ordered below the spec/map, have explicit
	// blocker direction, and are independently ready for an agent.
	mapIssue := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":  "Wayfinder map: queue rollout",
		"body":   "## Destination\nShip the queue rollout.",
		"labels": []string{"wayfinder:map"},
	}, http.StatusCreated).(map[string]any)
	mapID := mapIssue["id"].(string)
	blocker := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":  "Prepare the schema",
		"body":   "Create the storage seam.",
		"labels": []string{"ready-for-agent"},
	}, http.StatusCreated).(map[string]any)
	blockerID := blocker["id"].(string)
	child := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":     "Implement the queue",
		"body":      "Use the frontier query.",
		"parent_id": mapID,
		"labels":    []string{"wayfinder:task", "ready-for-agent"},
	}, http.StatusCreated).(map[string]any)
	childID := child["id"].(string)
	request(http.MethodPost, "/api/v1/issues/"+childID+"/blockers", map[string]string{"blocker_id": blockerID}, http.StatusOK)
	childRead := request(http.MethodGet, "/api/v1/issues/"+childID, nil, http.StatusOK).(map[string]any)
	if childRead["parent_id"] != mapID || childRead["blocked"] != true {
		t.Fatalf("ticket graph metadata missing: %#v", childRead)
	}

	frontierPath := "/api/v1/projects/" + projectSlug + "/frontier?parent=" + urlPath(mapID)
	frontier := request(http.MethodGet, frontierPath, nil, http.StatusOK).(map[string]any)
	if frontierItems(frontier).containsID(childID) {
		t.Fatal("blocked child appeared in the frontier")
	}
	request(http.MethodPost, "/api/v1/issues/"+blockerID+"/close", nil, http.StatusOK)
	frontier = request(http.MethodGet, frontierPath, nil, http.StatusOK).(map[string]any)
	if !frontierItems(frontier).containsID(childID) {
		t.Fatalf("closed blocker did not expose child in frontier: %#v", frontier)
	}

	// Wayfinder claim/resolution uses ordinary assignment, comment, and close;
	// the separate requests intentionally prove there is no claim-next API.
	claimed := request(http.MethodPost, "/api/v1/issues/"+childID+"/assign", map[string]string{"assignee": "agent/queue"}, http.StatusOK).(map[string]any)
	if claimed["assignee"] != "agent/queue" || claimed["assigned_actor"] != "compat-agent" {
		t.Fatalf("assignment attribution missing: %#v", claimed)
	}
	request(http.MethodPost, "/api/v1/issues/"+childID+"/comments", map[string]string{
		"body": "Resolution: queue implementation is ready.",
	}, http.StatusCreated)
	request(http.MethodPost, "/api/v1/issues/"+childID+"/close", nil, http.StatusOK)

	// Triage reads metadata, filters attention, mutates labels, and reopens a
	// closed issue when follow-up work arrives.
	target := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":  "Triage target",
		"body":   "Reporter supplied a reproduction.",
		"labels": []string{"needs-triage", "bug"},
	}, http.StatusCreated).(map[string]any)
	targetID := target["id"].(string)
	request(http.MethodPost, "/api/v1/issues/"+targetID+"/comments", map[string]string{
		"body": "> *This was generated by AI during triage.*\n\nVerified the reproduction.",
	}, http.StatusCreated)
	request(http.MethodPost, "/api/v1/issues/"+targetID+"/labels", map[string]string{"label": "needs-info"}, http.StatusOK)
	request(http.MethodDelete, "/api/v1/issues/"+targetID+"/labels/needs-triage", nil, http.StatusOK)
	targetRead := request(http.MethodGet, "/api/v1/issues/"+targetID, nil, http.StatusOK).(map[string]any)
	if !asIssueItems(targetRead["comments"]).containsBody("Verified the reproduction.") {
		t.Fatalf("metadata-rich issue read omitted triage comment: %#v", targetRead)
	}
	if labels := valueStrings(targetRead["labels"]); !labels.contains("needs-info") || labels.contains("needs-triage") {
		t.Fatalf("triage label add/remove was not reflected: %#v", targetRead)
	}
	attention := request(http.MethodGet, "/api/v1/issues?project="+projectSlug+"&label=needs-info&state=open", nil, http.StatusOK).(map[string]any)
	if !asIssueItems(attention["issues"]).containsID(targetID) {
		t.Fatalf("triage filter did not find target: %#v", attention)
	}
	request(http.MethodPost, "/api/v1/issues/"+targetID+"/close", nil, http.StatusOK)
	reopened := request(http.MethodPost, "/api/v1/issues/"+targetID+"/reopen", nil, http.StatusOK).(map[string]any)
	if reopened["state"] != "open" {
		t.Fatalf("triage reopen did not restore open state: %#v", reopened)
	}

	// Explicit ordering is part of the to-tickets contract and applies to
	// ordinary child issues, not a special workflow object.
	secondChild := request(http.MethodPost, "/api/v1/projects/"+projectSlug+"/issues", map[string]any{
		"title":     "Second child",
		"body":      "Follow-up.",
		"parent_id": mapID,
		"labels":    []string{"wayfinder:task", "ready-for-agent"},
	}, http.StatusCreated).(map[string]any)
	request(http.MethodPost, "/api/v1/issues/"+childID+"/position", map[string]int64{"position": 1}, http.StatusOK)
	request(http.MethodPost, "/api/v1/issues/"+secondChild["id"].(string)+"/position", map[string]int64{"position": 0}, http.StatusOK)
	children := request(http.MethodGet, "/api/v1/issues?project="+projectSlug+"&parent="+urlPath(mapID), nil, http.StatusOK).(map[string]any)
	if items := asIssueItems(children["issues"]); len(items) < 2 || items[0]["id"] != secondChild["id"] {
		t.Fatalf("explicit child order was not reflected in query: %#v", children)
	}
}

type issueList []map[string]any

func (items issueList) containsID(id string) bool {
	for _, item := range items {
		if item["id"] == id {
			return true
		}
	}
	return false
}

func (items issueList) containsBody(fragment string) bool {
	for _, item := range items {
		if body, ok := item["body"].(string); ok && strings.Contains(body, fragment) {
			return true
		}
	}
	return false
}

type stringList []string

func (items stringList) contains(value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func frontierItems(value map[string]any) issueList {
	return issueList(valueItems(value["frontier"]))
}

func asIssueItems(value any) issueList {
	return issueList(valueItems(value))
}

func valueItems(value any) []map[string]any {
	values, _ := value.([]any)
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items
}

func valueStrings(value any) stringList {
	values, _ := value.([]any)
	items := make(stringList, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			items = append(items, item)
		}
	}
	return items
}

func skillsRequestJSON(t *testing.T, client *http.Client, method, endpoint, key, actor, session string, body any, status int) map[string]any {
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
	request.Header.Set("X-Actor-ID", actor)
	request.Header.Set("X-Session-ID", session)
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
