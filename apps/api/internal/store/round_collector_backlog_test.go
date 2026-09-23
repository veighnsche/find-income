package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPriorCollectorSourceSaveRequiresOwnerBoardAndCurrentUnprocessedText(t *testing.T) {
	for _, scenario := range []string{"same_owner_board", "other_owner", "other_board", "stale_source"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner := Actor{Kind: "administrator", ID: "owner"}
			agent := Actor{Kind: "agent", ID: "codex-runner"}
			profile, err := db.CurrentPreferences(ctx)
			if err != nil {
				t.Fatal(err)
			}
			boards, err := db.ListCollectorBoards(ctx)
			if err != nil || len(boards) == 0 {
				t.Fatalf("boards: %v", err)
			}
			board := boards[0]
			host := "jobs.lever.co"
			if board.Region == "eu" {
				host = "jobs.eu.lever.co"
			}
			url := fmt.Sprintf("https://%s/%s/role-a", host, board.Site)
			raw := []byte(fmt.Sprintf(`{"id":"role-a","text":"Engineer","descriptionPlain":"Build services.","hostedUrl":%q}`, url))
			hash := sha256.Sum256(raw)
			company, _, err := db.CreateCompany(ctx, owner, CompanyInput{Name: board.DisplayName, Website: board.OfficialCareersURL})
			if err != nil {
				t.Fatal(err)
			}
			prior, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: "source-batch", Intent: "Collect source", Outcome: "discover", ProfileVersion: profile.Version,
				Scope:  RoundScope{Resources: []string{"board:" + board.ID}, Operations: []string{RoundCollectorPage}, Delegates: []string{agent.ID}},
				Limits: RoundAllowance{Requests: 1, Items: 1, Tools: 1}, Deadline: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			prior, err = db.ActivateRound(ctx, owner, prior.ID)
			if err != nil {
				t.Fatal(err)
			}
			page, _, err := db.ReserveCollectorAcquisition(ctx, agent, prior.ID, RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.MarkRoundDispatched(ctx, prior.ID, page.ID); err != nil {
				t.Fatal(err)
			}
			prior, err = db.Round(ctx, prior.ID)
			if err != nil {
				t.Fatal(err)
			}
			observed := time.Now().UTC().Format(time.RFC3339Nano)
			payload, _ := json.Marshal(struct {
				Postings []struct {
					Provider      string `json:"provider"`
					BoardID       string `json:"boardId"`
					ExternalID    string `json:"externalId"`
					SourceURL     string `json:"sourceUrl"`
					OriginalText  []byte `json:"originalText"`
					ContentSHA256 string `json:"contentSha256"`
					ObservedAt    string `json:"observedAt"`
				} `json:"postings"`
				PagesFetched  int `json:"pagesFetched"`
				ItemsExamined int `json:"itemsExamined"`
			}{Postings: []struct {
				Provider      string `json:"provider"`
				BoardID       string `json:"boardId"`
				ExternalID    string `json:"externalId"`
				SourceURL     string `json:"sourceUrl"`
				OriginalText  []byte `json:"originalText"`
				ContentSHA256 string `json:"contentSha256"`
				ObservedAt    string `json:"observedAt"`
			}{{"lever", board.ID, "role-a", url, raw, hex.EncodeToString(hash[:]), observed}}, PagesFetched: 1, ItemsExamined: 1})
			if _, err := db.SaveRoundCollectorBatch(ctx, agent, prior.ID, page.ID, prior.Revision, payload); err != nil {
				t.Fatal(err)
			}
			outcomes, err := db.RoundCollectorOutcomes(ctx, prior.ID, page.ID)
			if err != nil || len(outcomes) != 1 {
				t.Fatalf("source outcomes: %+v %v", outcomes, err)
			}
			if _, err := db.FinishRound(ctx, owner, prior.ID, RoundCompleted, "batch_staged", "partial", json.RawMessage(`{"code":"batch_staged"}`)); err != nil {
				t.Fatal(err)
			}
			if scenario == "stale_source" {
				_, _, err = db.SubmitIngestion(ctx, Actor{Kind: "system", ID: "collector:" + board.ID}, IngestionInput{Origin: "collector", SourceURL: url, OriginalText: string(raw) + " ", ConnectorID: "lever:" + board.ID, ExternalID: "role-a", DiscoveredAt: time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano), IdempotencyKey: "newer-revision"})
				if err != nil {
					t.Fatal(err)
				}
			}
			commissionOwner := owner
			if scenario == "other_owner" {
				commissionOwner.ID = "other-owner"
			}
			boardScope := "board:" + board.ID
			if scenario == "other_board" {
				boardScope = "board:unrelated"
			}
			next, _, err := db.StartRound(ctx, commissionOwner, StartRoundInput{RequestKey: "save-prior", Intent: "Review retained source", Outcome: "discover", ProfileVersion: profile.Version,
				Scope:  RoundScope{Resources: []string{"campaign:active", "company:" + company.ID, boardScope}, Operations: []string{RoundCodexTurn, RoundSaveSourceOpportunity}, Delegates: []string{agent.ID}},
				Limits: RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			next, err = db.ActivateRound(ctx, commissionOwner, next.ID)
			if err != nil {
				t.Fatal(err)
			}
			capability := mutationCapability(t, db, next, agent, "campaign:active")
			input := RoundMutationInput{RequestKey: "save-source", Operation: RoundSaveSourceOpportunity, ResourceID: "source-opening:" + outcomes[0].SourceOpeningID,
				ExpectedRevision: company.Revision, Capability: capability,
				SourceOpportunity: &SourceOpportunityMutationInput{SourceOpeningID: outcomes[0].SourceOpeningID, ExpectedRevision: company.Revision, CompanyID: company.ID,
					Opportunity: OpportunityInput{CompanyID: company.ID, Title: "Engineer", Kind: "employment", Stage: "new", WorkPattern: "remote", LocationText: "Brussels"}}}
			first, created, err := db.ApplyRoundMutation(ctx, agent, next.ID, input)
			if scenario == "same_owner_board" {
				if err != nil || !created {
					t.Fatalf("current saved source should be writable: %v", err)
				}
				replayed, created, err := db.ApplyRoundMutation(ctx, agent, next.ID, input)
				if err != nil || created || replayed != first {
					t.Fatalf("exact processed source replay: result=%+v created=%v err=%v", replayed, created, err)
				}
				input.SourceOpportunity.Opportunity.Title = "Changed Engineer"
				if _, _, err := db.ApplyRoundMutation(ctx, agent, next.ID, input); !errors.Is(err, ErrRoundIdempotencyConflict) {
					t.Fatalf("changed payload reused successful request key: %v", err)
				}
				input.SourceOpportunity.Opportunity.Title = "Engineer"
				input.RequestKey = "new-attempt-after-processing"
				if _, _, err := db.ApplyRoundMutation(ctx, agent, next.ID, input); !errors.Is(err, ErrFenced) {
					t.Fatalf("processed source written twice: %v", err)
				}
			} else if !errors.Is(err, ErrFenced) {
				t.Fatalf("%s crossed prior batch authority: %v", scenario, err)
			}
		})
	}
}
