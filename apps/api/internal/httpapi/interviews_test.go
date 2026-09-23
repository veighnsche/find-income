package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type interviewNoopWorker struct{}

func (interviewNoopWorker) CheckRound(context.Context, string) error { return nil }
func (interviewNoopWorker) LaunchRound(_ context.Context, r store.Round) error {
	if (r.Outcome != "interview_prepare" && r.Outcome != "interview_debrief") || r.State != store.RoundRunning {
		return store.ErrFenced
	}
	return nil
}
func (interviewNoopWorker) CancelRound(string) {}

func TestInterviewCommissionToPrivateBriefAndReplay(t *testing.T) {
	for _, change := range []string{"opportunity", "profile"} {
		t.Run(change, func(t *testing.T) {
			h := newHarness(t)
			h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: interviewNoopWorker{}, Worker: interviewNoopWorker{}}})
			owner := store.Actor{Kind: "administrator", ID: "owner"}
			company, _, err := h.db.CreateCompany(context.Background(), owner, store.CompanyInput{Name: "Fixture Employer"})
			if err != nil {
				t.Fatal(err)
			}
			roleText := "Maintain Go services with a small engineering team."
			opportunity, _, err := h.db.CreateOpportunity(context.Background(), owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", Stage: "new", SourceURL: "https://example.test/role", OriginalText: roleText})
			if err != nil {
				t.Fatal(err)
			}
			cookie, csrf := h.login()
			contextText := "The owner supplied a discussion invitation about Go services. No date or venue was given."
			body, _ := json.Marshal(map[string]string{"requestKey": "invite-one", "opportunityId": opportunity.ID, "context": contextText})
			unauth := h.request(http.MethodPost, "/api/v1/interviews/prepare", string(body), nil, "", "", origin)
			if unauth.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated commission: %d", unauth.Code)
			}
			started := h.request(http.MethodPost, "/api/v1/interviews/prepare", string(body), cookie, "", csrf, origin)
			if started.Code != http.StatusCreated {
				t.Fatalf("start: %d %s", started.Code, started.Body.String())
			}
			var response struct {
				InterviewID string `json:"interviewId"`
				Round       struct {
					ID string `json:"id"`
				} `json:"round"`
			}
			if err := json.Unmarshal(started.Body.Bytes(), &response); err != nil || response.InterviewID == "" || response.Round.ID == "" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			commission, err := h.db.Interview(context.Background(), response.InterviewID)
			if err != nil || commission.RoundID != response.Round.ID || !commission.Current {
				t.Fatalf("round not bound before launch: %+v %v", commission, err)
			}
			replay := h.request(http.MethodPost, "/api/v1/interviews/prepare", string(body), cookie, "", csrf, origin)
			if replay.Code != http.StatusOK {
				t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
			}
			var again struct {
				InterviewID string `json:"interviewId"`
				Round       struct {
					ID string `json:"id"`
				} `json:"round"`
			}
			_ = json.Unmarshal(replay.Body.Bytes(), &again)
			if again.InterviewID != response.InterviewID || again.Round.ID != response.Round.ID {
				t.Fatalf("retry made new work: %+v", again)
			}
			agent := store.Actor{Kind: "agent", ID: "codex-runner"}
			turn, _, err := h.db.ReserveRoundAttempt(context.Background(), agent, response.Round.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "opportunity:" + opportunity.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.MarkRoundDispatched(context.Background(), response.Round.ID, turn.ID); err != nil {
				t.Fatal(err)
			}
			capability, err := h.db.IssueRoundToolCapability(context.Background(), response.Round.ID, turn.ID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			digest := func(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
			cite := applicationpacks.Citation{SourceID: "role", Excerpt: "Maintain Go services"}
			input := interviewprep.Input{InterviewID: commission.ID, OpportunityID: opportunity.ID, RoleTitle: opportunity.Title, EmployerName: company.Name, Context: []interviewprep.ContextSource{{ID: "owner-input", Kind: "owner_input", Revision: commission.ContextSHA256, SHA256: commission.ContextSHA256, Body: contextText}, {ID: "role", Kind: "role", Revision: "1", SHA256: digest(roleText), Body: roleText}}, Draft: interviewprep.Draft{Focus: []interviewprep.FocusAlternative{{ID: "go", Why: interviewprep.CitedText{Text: "Discuss Go service work.", Citations: []applicationpacks.Citation{cite}}}}, Questions: []interviewprep.Question{{Text: "How are Go changes reviewed?", Why: interviewprep.CitedText{Text: "The saved role mentions Go services.", Citations: []applicationpacks.Citation{cite}}}}, Unknowns: []string{"No approved career example or interview time was supplied."}}}
			brief, err := interviewprep.Prepare(input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(brief)
			_, created, err := h.db.ApplyRoundMutation(context.Background(), agent, response.Round.ID, store.RoundMutationInput{RequestKey: "brief", Operation: store.RoundInterviewBriefSave, ResourceID: "opportunity:" + opportunity.ID, ExpectedRevision: opportunity.Revision, InterviewBrief: &store.InterviewBriefMutation{InterviewID: commission.ID, OpportunityID: opportunity.ID, InputSHA256: brief.InputSHA256, BriefJSON: encoded}, Capability: capability})
			if err != nil || !created {
				t.Fatalf("brief save: created=%v err=%v", created, err)
			}
			read := h.request(http.MethodGet, "/api/v1/interviews/"+commission.ID, "", cookie, "", "", "")
			if read.Code != http.StatusOK || !json.Valid(read.Body.Bytes()) {
				t.Fatalf("read: %d %s", read.Code, read.Body.String())
			}
			var detail struct {
				Interview struct {
					Current bool `json:"current"`
					Brief   struct {
						Input struct {
							Draft struct {
								Unknowns []string `json:"unknowns"`
							} `json:"draft"`
						} `json:"input"`
					} `json:"brief"`
				} `json:"interview"`
			}
			if err := json.Unmarshal(read.Body.Bytes(), &detail); err != nil || !detail.Interview.Current || len(detail.Interview.Brief.Input.Draft.Unknowns) != 1 {
				t.Fatalf("saved brief view: %+v %v", detail, err)
			}
			if _, err := h.db.OwnerInterview(context.Background(), "someone-else", commission.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("cross-owner interview read: %v", err)
			}
			for _, url := range []string{"/api/v1/interviews", "/api/v1/interviews/" + commission.ID} {
				if reply := h.request(http.MethodGet, url, "", nil, "", "", ""); reply.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous read: %d", reply.Code)
				}
			}
			if reply := h.request(http.MethodPost, "/api/v1/interviews/prepare", string(body), cookie, "", "", origin); reply.Code != http.StatusForbidden {
				t.Fatalf("missing CSRF: %d", reply.Code)
			}
			otherList, err := h.db.OwnerInterviews(context.Background(), "someone-else")
			if err != nil || len(otherList) != 0 {
				t.Fatalf("cross-owner list: %+v %v", otherList, err)
			}
			if _, err := h.db.OwnerInterviewDebriefs(context.Background(), "someone-else", commission.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("cross-owner debrief read: %v", err)
			}
			changedBody, _ := json.Marshal(map[string]string{"requestKey": "invite-one", "opportunityId": opportunity.ID, "context": "changed"})
			if reply := h.request(http.MethodPost, "/api/v1/interviews/prepare", string(changedBody), cookie, "", csrf, origin); reply.Code != http.StatusConflict {
				t.Fatalf("changed commission: %d", reply.Code)
			}
			ctx := context.Background()
			if err := h.db.BindRoundThread(ctx, response.Round.ID, turn.ID, turn.Generation, "review-thread"); err != nil {
				t.Fatal(err)
			}
			if err := h.db.BindRoundTurn(ctx, response.Round.ID, turn.ID, turn.Generation, "review-thread", "review-turn"); err != nil {
				t.Fatal(err)
			}
			if err := h.db.ObserveRoundTurn(ctx, response.Round.ID, turn.ID, turn.Generation, "review-thread", "review-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.FinishRoundAttempt(ctx, agent, response.Round.ID, turn.ID, true, json.RawMessage(`{}`), ""); err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.FinishRound(ctx, owner, response.Round.ID, store.RoundCompleted, "saved", "complete", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			debriefBody := `{"requestKey":"review-debrief","notes":"We discussed Go testing."}`
			debriefURL := "/api/v1/interviews/" + commission.ID + "/debrief"
			createdDebrief := h.request(http.MethodPost, debriefURL, debriefBody, cookie, "", csrf, origin)
			if createdDebrief.Code != http.StatusCreated {
				t.Fatalf("debrief create: %d %s", createdDebrief.Code, createdDebrief.Body.String())
			}
			var returned struct {
				Round struct {
					RequestKey string `json:"requestKey"`
				} `json:"round"`
			}
			if json.Unmarshal(started.Body.Bytes(), &returned) != nil || returned.Round.RequestKey != "invite-one" {
				t.Fatalf("prepare request identity differs from submitted key: %s", started.Body.String())
			}
			if json.Unmarshal(createdDebrief.Body.Bytes(), &returned) != nil || returned.Round.RequestKey != "review-debrief" {
				t.Fatalf("debrief request identity differs from submitted key: %s", createdDebrief.Body.String())
			}
			beforeChange := h.request(http.MethodPost, debriefURL, debriefBody, cookie, "", csrf, origin)
			if beforeChange.Code != http.StatusOK {
				t.Fatalf("debrief replay before change: %d %s", beforeChange.Code, beforeChange.Body.String())
			}
			if change == "opportunity" {
				changed := "Different role duties."
				if _, _, err := h.db.PatchOpportunity(ctx, owner, opportunity.ID, store.OpportunityPatch{ExpectedRevision: 1, OriginalText: &changed}); err != nil {
					t.Fatal(err)
				}
			} else {
				profile, err := h.db.CurrentPreferences(ctx)
				if err != nil {
					t.Fatal(err)
				}
				profile.MinMonthlyBaseCents++
				if _, _, err := h.db.UpdatePreferences(ctx, profile.Version, profile, owner); err != nil {
					t.Fatal(err)
				}
			}
			read = h.request(http.MethodGet, "/api/v1/interviews/"+commission.ID, "", cookie, "", "", "")
			if read.Code != http.StatusOK || !json.Valid(read.Body.Bytes()) {
				t.Fatalf("stale read: %d %s", read.Code, read.Body.String())
			}
			_ = json.Unmarshal(read.Body.Bytes(), &detail)
			afterChange := h.request(http.MethodPost, debriefURL, debriefBody, cookie, "", csrf, origin)
			t.Logf("debrief create=%d replay-before-change=%d replay-after-change=%d body=%s", createdDebrief.Code, beforeChange.Code, afterChange.Code, afterChange.Body.String())
			if afterChange.Code != http.StatusOK || !bytes.Equal(afterChange.Body.Bytes(), beforeChange.Body.Bytes()) {
				t.Errorf("exact debrief replay must return the same existing debrief and round after context changes")
			}
			for _, input := range []string{`{"requestKey":"review-debrief","notes":"Different notes."}`, `{"requestKey":"new-stale-debrief","notes":"We discussed Go testing."}`} {
				reply := h.request(http.MethodPost, debriefURL, input, cookie, "", csrf, origin)
				if reply.Code != http.StatusConflict {
					t.Fatalf("stale new work or changed payload accepted: %d %s", reply.Code, reply.Body.String())
				}
			}
			debriefs, err := h.db.OwnerInterviewDebriefs(ctx, owner.ID, commission.ID)
			if err != nil || len(debriefs) != 1 {
				t.Fatalf("replay or stale request created debrief rows: %+v %v", debriefs, err)
			}
			if detail.Interview.Current {
				t.Fatal("stale brief shown current")
			}
		})
	}
}
