package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestApplicationPackToolUsesRecordedJevAndGuardedMutation(t *testing.T) {
	ctx := context.Background()
	typst, err := exec.LookPath("typst")
	if err != nil {
		t.Skip("Typst not installed")
	}
	s, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "pack-owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.invalid/job", OriginalText: "Build Go services and Linux developer tools.", Stage: "new", WorkPattern: "remote", LocationText: "Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{RequestKey: "select-pack-role", ExpectedOpportunityRevision: opportunity.Revision, ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "pack-round", Intent: "Prepare a truthful application", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundJevRequest, store.RoundPrepareApplicationPack}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 3, Items: 1, Tools: 4, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "bound-pack-turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"relevance":{"type":"choice","choice":"relevant","probabilities":{"relevant":0.8,"uncertain":0.1,"unrelated":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer provider.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	jevConfig := jev.DefaultConfig()
	jevConfig.Enabled = true
	jevConfig.Endpoint = provider.URL + "/v1/systemone"
	jevConfig.MaxAttempts = 1
	client, err := jev.NewFromEnvironment(jevConfig, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../../../../")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureApplicationPacks(ApplicationPackRuntimeConfig{ProjectRoot: root, TypstPath: typst, PrivateTempDir: t.TempDir(), RenderTimeout: 10 * time.Second, Relevance: jevservice.Service{Store: db, Client: client}}); err != nil {
		t.Fatal(err)
	}
	citation := applicationpacks.Citation{SourceID: "cv-vince-liem.md", Excerpt: "Recent work spans Go services, a compiler targeting TypeScript/Bun, Rust systems utilities and Python MCP tooling."}
	args := applicationPackPrepareArgs{RoundID: round.ID, Capability: capability, RequestKey: "pack-one", OpportunityID: opportunity.ID, RequirementQuote: "Build Go services",
		Focus:            applicationpacks.Line{Text: "Go service and platform work.", Citations: []applicationpacks.Citation{citation}},
		Cover:            []applicationpacks.Line{{Text: "I have recent personal Go service work.", Citations: []applicationpacks.Citation{citation}}},
		MaterialUnknowns: []string{"The role's application questions are not known from this saved text."}}
	bad := args
	bad.Focus.Citations = []applicationpacks.Citation{{SourceID: "cv-vince-liem.md", Excerpt: "Invented commercial backend tenure"}}
	if _, err := s.applicationPackPrepareTool(ctx, bad); err == nil || calls != 0 {
		t.Fatalf("invalid draft charged Jev: err=%v calls=%d", err, calls)
	}
	result, err := s.applicationPackPrepareTool(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result["packId"].(string)
	if !ok || id == "" || calls != 1 {
		t.Fatalf("result=%v calls=%d", result, calls)
	}
	pack, err := db.ApplicationPack(ctx, id)
	if err != nil || pack.Version != 1 || len(pack.PDF) < 1000 {
		t.Fatalf("pack=%+v err=%v", pack, err)
	}
	var manifest struct {
		Draft applicationpacks.Draft `json:"draft"`
		Role  applicationpacks.Role  `json:"role"`
	}
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Draft.Relevance) != 1 || manifest.Draft.Relevance[0].Model != "jev-1.13.0" || manifest.Role.Destination != "" || len(manifest.Draft.MaterialUnknowns) != 3 {
		t.Fatalf("manifest=%+v", manifest)
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "succeeded" {
		t.Fatalf("Jev attempts=%+v err=%v", attempts, err)
	}
	after, err := db.Round(ctx, round.ID)
	if err != nil || after.Used.Requests != 2 || after.Used.Items != 1 {
		t.Fatalf("round=%+v err=%v", after.Used, err)
	}
	replayed, err := s.applicationPackPrepareTool(ctx, args)
	if err != nil || replayed["packId"] != id || replayed["created"] != false || calls != 1 {
		t.Fatalf("replay=%v err=%v calls=%d", replayed, err, calls)
	}
	changed := args
	changed.Focus.Text = "Changed role focus"
	if _, err := s.applicationPackPrepareTool(ctx, changed); !errors.Is(err, store.ErrRoundIdempotencyConflict) || calls != 1 {
		t.Fatalf("changed replay err=%v calls=%d", err, calls)
	}
	sources, template, err := applicationpacks.LoadApprovedCareerSources(root, []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.packCorrection(ctx, round, applicationPackCorrectionArgs{PriorPackID: "wrong-pack", OwnerInstructionID: "wrong-instruction"}, opportunity, company, profile, sources, template); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("wrong prior pack not fenced: %v", err)
	}
	wrongOpportunity := opportunity
	wrongOpportunity.ID = "different-opportunity"
	if _, err := s.packCorrection(ctx, round, applicationPackCorrectionArgs{PriorPackID: id, OwnerInstructionID: "wrong-instruction"}, wrongOpportunity, company, profile, sources, template); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong opportunity not fenced: %v", err)
	}
	staleProfile := profile
	staleProfile.Version++
	if _, err := s.packCorrection(ctx, round, applicationPackCorrectionArgs{PriorPackID: id, OwnerInstructionID: "wrong-instruction"}, opportunity, company, staleProfile, sources, template); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed profile not fenced: %v", err)
	}
	if _, err := s.packCorrection(ctx, round, applicationPackCorrectionArgs{PriorPackID: id, OwnerInstructionID: "wrong-instruction"}, opportunity, company, profile, sources, template); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("wrong instruction not fenced: %v", err)
	}
	wrongKind, _, err := db.AddOwnerInstruction(ctx, owner, store.OwnerInstructionInput{RequestKey: "wrong-pack-kind", TargetKind: "opportunity", TargetID: opportunity.ID, ExpectedRevision: opportunity.Revision, Text: "Correct the selected opportunity."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.packCorrection(ctx, round, applicationPackCorrectionArgs{PriorPackID: id, OwnerInstructionID: wrongKind.ID}, opportunity, company, profile, sources, template); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("opportunity-wide instruction authorised pack correction: %v", err)
	}
}

func TestApplicationPackExpiredBeforePreparation(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "pack-agent"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.invalid/role", OriginalText: "Build Go services.", Stage: "new", WorkPattern: "remote", LocationText: "Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "expiring-pack", Intent: "Prepare", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundJevRequest, store.RoundPrepareApplicationPack}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 2, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(80 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	_, err = s.applicationPackPrepareTool(ctx, applicationPackPrepareArgs{RoundID: round.ID, Capability: capability, RequestKey: "expired", OpportunityID: opportunity.ID, RequirementQuote: "Build Go services"})
	if !errors.Is(err, store.ErrExpired) && !errors.Is(err, store.ErrFenced) {
		t.Fatalf("expired preparation err=%v", err)
	}
	items, err := db.ListApplicationPacks(ctx, opportunity.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("expired preparation saved pack: %+v %v", items, err)
	}
}
