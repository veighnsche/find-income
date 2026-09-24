package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoundCandidateLeads(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := db.RoundCandidateLeads(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown round: %v", err)
	}
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "agent"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: "candidate-leads", Intent: "inspect", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{RoundCodexTurn, RoundSearchSource, RoundStageDiscovery, RoundCreateCompany, RoundCreateOpportunity}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 4, Items: 5, Tools: 8, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	leads, searches, err := db.RoundCandidateLeads(ctx, r.ID)
	if err != nil || len(leads) != 0 || len(searches) != 0 {
		t.Fatalf("empty round: %+v %+v %v", leads, searches, err)
	}
	if leads == nil || searches == nil {
		t.Fatal("empty lists must serialize as arrays, not null")
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "turn", Operation: RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "source", Operation: RoundSearchSource, ResourceID: "discovery:himalayas", Cost: RoundAllowance{Requests: 1, Tools: 1}, BoundCapability: capability})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	quote := "Lead Engineer\nhttps://himalayas.app/companies/example/jobs/lead"
	response, _ := json.Marshal(map[string]any{"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": quote}}}})
	page := DiscoveryHTTP{AttemptID: source.ID, RoundID: r.ID, Method: "search_jobs", RequestJSON: `{"method":"tools/call"}`, Endpoint: "https://mcp.himalayas.app/mcp", ObservedAt: utcNow()}
	if err = db.PrepareDiscoveryHTTP(ctx, page); err != nil {
		t.Fatal(err)
	}
	page.StatusCode = 200
	page.ResponseBody = response
	if err = db.CompleteDiscoveryHTTP(ctx, page); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRoundAttempt(ctx, agent, r.ID, source.ID, true, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	candidateURL := "https://himalayas.app/companies/example/jobs/lead"
	staged, err := db.StageDiscoveryCandidate(ctx, DiscoveryStageInput{RoundID: r.ID, Capability: capability, RequestKey: "candidate", Candidate: DiscoveryCandidate{AttemptID: source.ID, Kind: "job", Title: "Lead Engineer", URL: candidateURL, EvidenceQuote: quote}})
	if err != nil {
		t.Fatal(err)
	}
	leads, searches, err = db.RoundCandidateLeads(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 1 || leads[0].ID != staged.CandidateID || leads[0].Status != "staged" || leads[0].Title != "Lead Engineer" {
		t.Fatalf("staged lead: %+v", leads)
	}
	if len(searches) != 1 || searches[0].Source != "mcp.himalayas.app" || searches[0].Method != "search_jobs" || searches[0].Candidates != 1 || !searches[0].Succeeded {
		t.Fatalf("searches: %+v", searches)
	}
	saveCap := mutationCapability(t, db, r, agent, "campaign:active")
	company, _, err := db.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "company:direct", Operation: RoundCreateCompany, ResourceID: "campaign:active", ExpectedRevision: prefs.Version, Capability: saveCap, Company: &CompanyInput{Name: "Example", Website: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "save:direct", Operation: RoundCreateOpportunity, ResourceID: "company:" + company.EntityID, ExpectedRevision: company.Revision, Capability: saveCap, Opportunity: &OpportunityInput{CompanyID: company.EntityID, Title: "Lead Engineer", Kind: "employment", SourceURL: candidateURL, OriginalText: quote, Stage: "new"}}); err != nil {
		t.Fatal(err)
	}
	leads, _, err = db.RoundCandidateLeads(ctx, r.ID)
	if err != nil || len(leads) != 1 || leads[0].Status != "saved" {
		t.Fatalf("saved lead: %+v %v", leads, err)
	}
	if id, err := db.RoundOpportunityBySourceURL(ctx, r.ID, candidateURL); err != nil || id == "" {
		t.Fatalf("round opportunity lookup: %q %v", id, err)
	}
	if _, err := db.RoundOpportunityBySourceURL(ctx, r.ID, "https://example.com/other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source URL: %v", err)
	}
}

func TestOpportunityBySourceURLSpansRounds(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "agent"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := func(key string) Round {
		t.Helper()
		r, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: key, Intent: "inspect", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{RoundCodexTurn, RoundSearchSource, RoundStageDiscovery, RoundCreateCompany, RoundCreateOpportunity}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 4, Items: 5, Tools: 8, Turns: 2}})
		if err != nil {
			t.Fatal(err)
		}
		if r, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := start("span-first")
	savedURL := "https://himalayas.app/companies/example/jobs/lead"
	saveCap := mutationCapability(t, db, first, agent, "campaign:active")
	company, _, err := db.ApplyRoundMutation(ctx, agent, first.ID, RoundMutationInput{RequestKey: "company:span", Operation: RoundCreateCompany, ResourceID: "campaign:active", ExpectedRevision: prefs.Version, Capability: saveCap, Company: &CompanyInput{Name: "Example", Website: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.ApplyRoundMutation(ctx, agent, first.ID, RoundMutationInput{RequestKey: "save:span", Operation: RoundCreateOpportunity, ResourceID: "company:" + company.EntityID, ExpectedRevision: company.Revision, Capability: saveCap, Opportunity: &OpportunityInput{CompanyID: company.EntityID, Title: "Lead Engineer", Kind: "employment", SourceURL: savedURL, OriginalText: "Lead Engineer", Stage: "new"}}); err != nil {
		t.Fatal(err)
	}
	if id, err := db.OpportunityBySourceURL(ctx, savedURL); err != nil || id == "" {
		t.Fatalf("global lookup: %q %v", id, err)
	}
	if _, err := db.OpportunityBySourceURL(ctx, "https://example.com/other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source URL: %v", err)
	}
	if _, err := db.OpportunityBySourceURL(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty source URL: %v", err)
	}
	if _, _, err := db.StopRound(ctx, owner, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PauseStoppedRound(ctx, owner, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRound(ctx, owner, first.ID, RoundCompleted, "test", "complete", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	second := start("span-second")
	if _, err := db.RoundOpportunityBySourceURL(ctx, second.ID, savedURL); !errors.Is(err, ErrNotFound) {
		t.Fatalf("round-scoped lookup must miss another round's save: %v", err)
	}
	if id, err := db.OpportunityBySourceURL(ctx, savedURL); err != nil || id == "" {
		t.Fatalf("global lookup after second round: %q %v", id, err)
	}
}
