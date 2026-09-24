package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestRoundRoutesRequireOwnerControlAndDelegatedAtomicMutation(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	status, body := h.owner(http.MethodPost, "/rounds", `{"requestKey":"start"}`)
	requireStatus(t, status, http.StatusNotFound, body)
	status, body = h.owner(http.MethodPost, "/companies", `{"name":"Bypass"}`)
	requireStatus(t, status, http.StatusServiceUnavailable, body)
	credential, token, err := h.service.CreateAgent(context.Background(), owner, "round-agent",
		[]string{"opportunities:write"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.db.CurrentPreferences(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := h.db.StartRound(context.Background(), owner.Actor(), store.StartRoundInput{
		RequestKey: "delegated", Intent: "Find work", Outcome: "process_input", ProfileVersion: p.Version,
		Scope: store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundCreateCompany, store.RoundCodexTurn},
			Delegates: []string{credential.ID}}, Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1},
		Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = h.db.ActivateRound(context.Background(), owner.Actor(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := h.db.ReserveRoundAttempt(context.Background(), store.Actor{Kind: "agent", ID: credential.ID}, r.ID,
		store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "campaign:active",
			Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(context.Background(), r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := h.db.IssueRoundToolCapability(context.Background(), r.ID, turn.ID, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner(http.MethodGet, "/rounds/active")
	requireStatus(t, status, http.StatusOK, body)
	if decodeObject(t, body)["id"] != r.ID {
		t.Fatalf("wrong active round: %s", body)
	}
	status, body = h.owner(http.MethodPost, "/rounds/"+r.ID+"/mutations", `{"requestKey":"first"}`)
	requireStatus(t, status, http.StatusForbidden, body)
	input := `{"requestKey":"first","operation":"company.create","resourceId":"campaign:active","expectedRevision":1,"company":{"name":"Synthetic Employer"}}`
	status, body = h.do(http.MethodPost, "/rounds/"+r.ID+"/mutations", input, token, "", "", nil, capability)
	requireStatus(t, status, http.StatusCreated, body)
	var first store.RoundMutationResult
	if err := json.Unmarshal(body, &first); err != nil || first.EntityID == "" || first.AuditID == "" {
		t.Fatalf("result: %+v %v", first, err)
	}
	status, replay := h.do(http.MethodPost, "/rounds/"+r.ID+"/mutations", input, token, "", "", nil, capability)
	requireStatus(t, status, http.StatusOK, replay)
	if string(replay) != string(body) {
		t.Fatalf("replay changed result: %s %s", body, replay)
	}
	status, body = h.owner(http.MethodGet, "/rounds/"+r.ID+"/results")
	requireStatus(t, status, http.StatusOK, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("missing result: %s", body)
	}
	status, body = h.owner(http.MethodPost, "/rounds/"+r.ID+"/stop")
	requireStatus(t, status, http.StatusOK, body)
	status, body = h.do(http.MethodPost, "/rounds/"+r.ID+"/mutations",
		`{"requestKey":"second","operation":"company.create","resourceId":"campaign:active","expectedRevision":1,"company":{"name":"Second"}}`, token, "", "", nil, capability)
	requireStatus(t, status, http.StatusForbidden, body)
}
