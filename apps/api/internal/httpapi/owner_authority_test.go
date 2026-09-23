package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestOwnerInstructionAndDirectDecisionRoutes(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	ctx := context.Background()
	p, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(`{"requestKey":"owner-context","targetKind":"profile","targetId":"current","expectedRevision":%d,"text":"I can work 32 hours and need at least €5,000 monthly."}`, p.Version)
	status, body := h.do(http.MethodPost, "/owner-instructions", request, "", "", origin, h.cookie)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.owner(http.MethodPost, "/owner-instructions", request)
	requireStatus(t, status, http.StatusCreated, body)
	instruction := decodeObject(t, body)
	if instruction["targetKind"] != "profile" || instruction["id"] == "" {
		t.Fatalf("instruction DTO: %s", body)
	}
	status, replay := h.owner(http.MethodPost, "/owner-instructions", request)
	requireStatus(t, status, http.StatusOK, replay)
	if string(replay) != string(body) {
		t.Fatalf("instruction replay changed: %s %s", body, replay)
	}
	company, _, err := h.db.CreateCompany(ctx, store.Actor{Kind: owner.Kind, ID: owner.ID}, store.CompanyInput{Name: "Synthetic Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(ctx, store.Actor{Kind: owner.Kind, ID: owner.ID}, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend engineer", Kind: "employment", Stage: "discovered", WorkPattern: "unknown",
		OriginalText: "A backend role.", SourceURL: "https://example.test/jobs/backend"})
	if err != nil {
		t.Fatal(err)
	}
	decision := fmt.Sprintf(`{"requestKey":"owner-selected","expectedOpportunityRevision":%d,"expectedDecisionRevision":0,"decision":"selected"}`, opportunity.Revision)
	status, body = h.owner(http.MethodPost, "/opportunities/"+opportunity.ID+"/decision", decision)
	requireStatus(t, status, http.StatusCreated, body)
	if decodeObject(t, body)["decision"] != "selected" {
		t.Fatalf("decision DTO: %s", body)
	}
	status, body = h.owner(http.MethodGet, "/opportunities/"+opportunity.ID+"/decision")
	requireStatus(t, status, http.StatusOK, body)
	if decodeObject(t, body)["revision"] != float64(1) {
		t.Fatalf("decision read: %s", body)
	}
}
