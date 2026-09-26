package httpapi

import (
	"encoding/json"
	"testing"
)

func TestArtifactReadsLive(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/opportunities/synthetic-role/artifacts", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing set: got %d, want honest 404", response.Code)
	}
	opportunity := createCheckedOpportunity(t, h, "artifact-read-1")
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts", "", cookie, "", "", "")
	var set struct {
		OpportunityID string `json:"opportunityId"`
		CheckStatus   string `json:"checkStatus"`
		Entries       []struct {
			Type     string `json:"type"`
			Required bool   `json:"required"`
			State    string `json:"state"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || set.CheckStatus != "not_checked" || len(set.Entries) != 5 {
		t.Fatalf("unchecked set: %d %+v", response.Code, set)
	}
	for _, entry := range set.Entries {
		if entry.Required || entry.State != "unresolved" {
			t.Fatalf("unchecked %s: %+v", entry.Type, entry)
		}
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv", "", cookie, "", "", "")
	var entry struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || entry.Type != "cv" || entry.State != "unresolved" {
		t.Fatalf("single entry: %d %+v", response.Code, entry)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/portfolio", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("unknown type: got %d, want 400", response.Code)
	}
}

func TestArtifactExactEditVersions(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "artifact-edit-1")
	put := func(artifactType, body string) (int, map[string]any) {
		response := h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/"+artifactType, body, cookie, "", csrf, origin)
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return response.Code, decoded
	}
	code, first := put("cv", `{"requestKey":"edit-1","expectedVersion":0,"content":"cv v1"}`)
	if code != 201 || first["version"] != float64(1) || first["content"] != "cv v1" {
		t.Fatalf("first edit: %d %+v", code, first)
	}
	code, replay := put("cv", `{"requestKey":"edit-1","expectedVersion":0,"content":"cv v1"}`)
	if code != 200 || replay["id"] != first["id"] {
		t.Fatalf("replay: %d %+v", code, replay)
	}
	code, second := put("cv", `{"requestKey":"edit-2","expectedVersion":1,"content":"cv v2"}`)
	if code != 201 || second["version"] != float64(2) {
		t.Fatalf("second edit: %d %+v", code, second)
	}
	if code, _ := put("cv", `{"requestKey":"edit-3","expectedVersion":1,"content":"fork"}`); code != 409 {
		t.Fatalf("stale fence: got %d, want 409", code)
	}
	if code, _ := put("form_values", `{"requestKey":"edit-4","expectedVersion":0,"content":"derived"}`); code != 400 {
		t.Fatalf("form values write: got %d, want 400", code)
	}
	if code, _ := put("cv", `{"requestKey":"edit-5","expectedVersion":2,"content":""}`); code != 400 {
		t.Fatalf("empty content: got %d, want 400", code)
	}
	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv", "", cookie, "", "", "")
	var entry struct {
		Current *struct {
			Version int64  `json:"version"`
			Content string `json:"content"`
		} `json:"current"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || entry.Current == nil || entry.Current.Version != 2 || entry.Current.Content != "cv v2" {
		t.Fatalf("read back: %d %+v", response.Code, entry)
	}
}
