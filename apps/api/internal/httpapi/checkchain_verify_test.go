package httpapi

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RW-A3 chain verification at the HTTP boundary: selected-only guards,
// cross-role isolation, Answer-path import closure, and Jev-only matching
// that fails closed without a fallback model.

func createUnselectedOpportunity(t *testing.T, h *harness) store.Opportunity {
	t.Helper()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := h.db.CreateCompany(context.Background(), owner, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(context.Background(), owner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Stage: "new", WorkPattern: "hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	return opportunity
}

// Unselected roles get honest 404s on every check/answer route; selection
// alone reads not_checked, and matching/answering before a completed check
// conflicts instead of inventing work.
func TestCheckChainGuardsHTTP(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	opportunity := createUnselectedOpportunity(t, h)

	startBody := `{"requestKey":"chain-guard-check","expectedOpportunityRevision":1,"expectedWorkflowRevision":0}`
	matchBody := `{"requestKey":"chain-guard-match","expectedCheckId":"none","expectedQuestionSetSha256":"none"}`
	for _, item := range []struct {
		method, path, body string
	}{
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/checks", startBody},
		{"GET", "/api/v1/opportunities/" + opportunity.ID + "/checks/current", ""},
		{"GET", "/api/v1/opportunities/" + opportunity.ID + "/checks/current/activity", ""},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/answers/match", matchBody},
		{"GET", "/api/v1/opportunities/" + opportunity.ID + "/answers/match/current", ""},
		{"GET", "/api/v1/opportunities/" + opportunity.ID + "/answers/current", ""},
		{"PUT", "/api/v1/opportunities/" + opportunity.ID + "/questions/q/answer",
			`{"expectedAnswerVersion":0,"text":"x"}`},
	} {
		response := h.request(item.method, item.path, item.body, cookie, "", csrf, origin)
		if response.Code != 404 {
			t.Fatalf("%s %s: got %d, want honest 404", item.method, item.path, response.Code)
		}
	}
	if _, _, err := h.db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID,
		store.OwnerDecisionInput{RequestKey: "chain-guard-select",
			ExpectedOpportunityRevision: opportunity.Revision, ExpectedDecisionRevision: 0,
			Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/checks/current", "", cookie, "", "", "")
	var current struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || current.Status != "not_checked" {
		t.Fatalf("selection without work: %d %+v", response.Code, current)
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", startBody, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("explicit check start: %d %s", response.Code, response.Body.String())
	}
	var started struct {
		Check struct {
			ID                string `json:"id"`
			QuestionSetSha256 string `json:"questionSetSha256"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	pinned := `{"requestKey":"chain-guard-match","expectedCheckId":"` + started.Check.ID +
		`","expectedQuestionSetSha256":"` + started.Check.QuestionSetSha256 + `"}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match", pinned, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("match before completion: %d %s", response.Code, response.Body.String())
	}
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/questions/q/answer",
		`{"expectedAnswerVersion":0,"text":"x"}`, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("answer before completion: %d %s", response.Code, response.Body.String())
	}
}

// One role's questions are unusable from another role, and each role's answer
// list stays pinned to its own check.
func TestCheckChainCrossRoleQuestionRejected(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	first, firstCheck := completeMatchCheck(t, h, "chain-xrole-a")
	second, secondCheck := completeMatchCheck(t, h, "chain-xrole-b")

	response := h.request("PUT", "/api/v1/opportunities/"+second.ID+"/questions/"+
		firstCheck.Questions[0].ID+"/answer", `{"expectedAnswerVersion":0,"text":"x"}`, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("cross-role answer: %d %s", response.Code, response.Body.String())
	}
	response = h.request("GET", "/api/v1/opportunities/"+second.ID+"/answers/current", "", cookie, "", "", "")
	var list struct {
		CheckID string `json:"checkId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || list.CheckID != secondCheck.ID {
		t.Fatalf("second role answers: %d %+v", response.Code, list)
	}
	response = h.request("PUT", "/api/v1/opportunities/"+first.ID+"/questions/"+
		firstCheck.Questions[0].ID+"/answer", `{"expectedAnswerVersion":0,"text":"Own role answer."}`,
		cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("own-role answer: %d %s", response.Code, response.Body.String())
	}
}

// The Answer path cannot reach Codex/LLM: its files import no model runner,
// and the only judgment touchpoint is the Jev binding in answermatch.go.
func TestCheckChainAnswerPathZeroModelImports(t *testing.T) {
	files := []string{"checkanswer.go", "answermatch.go",
		"../store/answermatch.go", "../store/answervalues.go",
		"../store/savedanswers.go", "../jev/answer_match.go"}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			lowered := strings.ToLower(path)
			if strings.Contains(lowered, "codex") || strings.Contains(lowered, "llm") ||
				strings.Contains(lowered, "openai") || strings.Contains(lowered, "anthropic") {
				t.Fatalf("%s must not import %s", file, path)
			}
		}
	}
	raw, err := os.ReadFile("answermatch.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "jevservice.Binding") {
		t.Fatal("answer matching must judge through the Jev binding")
	}
	if strings.Contains(string(raw), "codex") || strings.Contains(string(raw), "Codex") {
		t.Fatal("answer matching must not reference Codex")
	}
}

// With approved answers available but no Jev client configured, matching fails
// closed with 503 and persists nothing: no silent fallback, no invented match.
func TestCheckChainMatchFailsClosedWithoutJev(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity, check := completeMatchCheck(t, h, "chain-closed")
	if _, _, err := h.db.CreateSavedAnswer(context.Background(),
		store.Actor{Kind: "administrator", ID: "owner"}, store.SavedAnswerCreateInput{
			RequestKey: "chain-closed-answer",
			Text:       "I want this role for its platform work and on-call rotation.",
			ScopeTags:  []string{"role"}, ContextNote: "role motivation"}); err != nil {
		t.Fatal(err)
	}
	body := `{"requestKey":"chain-closed-run","expectedCheckId":"` + check.ID +
		`","expectedQuestionSetSha256":"` + check.QuestionSetSHA256 + `"}`
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match",
		body, cookie, "", csrf, origin)
	if response.Code != 503 {
		t.Fatalf("match without Jev: %d %s", response.Code, response.Body.String())
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/answers/match/current",
		"", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("failed match must persist nothing: %d", response.Code)
	}
}
