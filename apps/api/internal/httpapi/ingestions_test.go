package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOwnerIngestionIsDurableIdempotentAndAuthenticated(t *testing.T) {
	h := newRecordHTTP(t)
	status, body := h.do(http.MethodGet, "/ingestions", "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	h.login()
	input := `{"sourceUrl":"https://jobs.example.test/42","idempotencyKey":"owner-42"}`
	status, body = h.do(http.MethodPost, "/ingestions", input, "", "", "", h.cookie)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.owner(http.MethodPost, "/ingestions", input)
	requireStatus(t, status, http.StatusAccepted, body)
	first := decodeObject(t, body)
	id, ok := first["id"].(string)
	if !ok || id == "" || first["status"] != "pending" || first["opportunityId"] != nil {
		t.Fatalf("expected a pending durable intake without a saved opportunity: %+v", first)
	}
	status, body = h.owner(http.MethodPost, "/ingestions", input)
	requireStatus(t, status, http.StatusAccepted, body)
	if second := decodeObject(t, body); second["id"] != id || second["jobId"] != first["jobId"] {
		t.Fatalf("idempotent repeat created a second job: %+v", second)
	}
	status, body = h.owner(http.MethodPost, "/ingestions", `{"sourceUrl":"https://jobs.example.test/other","idempotencyKey":"owner-42"}`)
	requireStatus(t, status, http.StatusConflict, body)
	status, body = h.owner(http.MethodGet, "/ingestions/"+id)
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["id"]; got != id {
		t.Fatalf("got ingestion %v, want %s", got, id)
	}
	status, body = h.owner(http.MethodGet, "/ingestions")
	requireStatus(t, status, http.StatusOK, body)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0]["id"] != id {
		t.Fatalf("page: %+v", page.Items)
	}
	status, body = h.owner(http.MethodPost, "/ingestions/"+id+"/retry", `{}`)
	requireStatus(t, status, http.StatusConflict, body) // a queued submission cannot be retried
}

func TestAgentIngestionScopeIdentityAndIsolation(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	_, firstToken, err := h.service.CreateAgent(t.Context(), owner, "ingest-one", []string{"openings:ingest"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, secondToken, err := h.service.CreateAgent(t.Context(), owner, "ingest-two", []string{"openings:ingest"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, readToken, err := h.service.CreateAgent(t.Context(), owner, "reader", []string{"opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	input := `{"sourceUrl":"https://jobs.example.test/agent","idempotencyKey":"agent-1"}`
	status, body := h.do(http.MethodPost, "/ingestions", input, readToken, "", "", nil)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.do(http.MethodPost, "/ingestions", input, firstToken, "", "", nil)
	requireStatus(t, status, http.StatusAccepted, body)
	result := decodeObject(t, body)
	id := result["id"].(string)
	if result["origin"] != "agent" {
		t.Fatalf("agent origin: %+v", result)
	}
	status, body = h.do(http.MethodGet, "/ingestions/"+id, "", firstToken, "", "", nil)
	requireStatus(t, status, http.StatusOK, body)
	status, body = h.do(http.MethodGet, "/ingestions/"+id, "", secondToken, "", "", nil)
	requireStatus(t, status, http.StatusNotFound, body)
	status, body = h.do(http.MethodGet, "/ingestions", "", firstToken, "", "", nil)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.do(http.MethodPost, "/ingestions/"+id+"/retry", `{}`, secondToken, "", "", nil)
	requireStatus(t, status, http.StatusNotFound, body)
	status, body = h.do(http.MethodPost, "/ingestions/"+id+"/retry", `{}`, firstToken, "", "", nil)
	requireStatus(t, status, http.StatusConflict, body)
	status, body = h.do(http.MethodPost, "/ingestions", `{"origin":"collector","sourceUrl":"https://jobs.example.test/x","idempotencyKey":"forged"}`, firstToken, "", "", nil)
	requireStatus(t, status, http.StatusBadRequest, body)
}

func TestOwnerIngestionAcceptsFullVacancyAndRejectsUnknownFields(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	longText := strings.Repeat("Vacancy details ", 500)
	input, err := json.Marshal(map[string]string{"originalText": longText, "idempotencyKey": "paste-1"})
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.owner(http.MethodPost, "/ingestions", string(input))
	requireStatus(t, status, http.StatusAccepted, body)
	if got := decodeObject(t, body)["originalText"]; got != longText {
		t.Fatal("full pasted vacancy was not preserved")
	}
	status, body = h.owner(http.MethodPost, "/ingestions", `{"originalText":"vacancy","idempotencyKey":"paste-2","companyName":"invented"}`)
	requireStatus(t, status, http.StatusBadRequest, body)
}
