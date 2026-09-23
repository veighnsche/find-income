package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestCommissionedChangedLeverSourceRefreshesOneOwnerCorrectedRole(t *testing.T) {
	ctx := context.Background()
	firstRaw := fixturePosting("same-role")
	changedRaw := json.RawMessage(`{ "id":"same-role", "text":"Senior Backend Engineer", "descriptionPlain":"Build changed services in Rust.", "hostedUrl":"https://jobs.lever.co/example/same-role" }`)
	var mu sync.Mutex
	currentRaw := firstRaw
	failRead := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		raw := append([]byte(nil), currentRaw...)
		failed := failRead
		mu.Unlock()
		if failed {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(append([]byte("["), raw...), ']'))
	}))
	defer server.Close()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "collector-agent"}
	board, err := db.CreateCollectorBoard(ctx, owner, store.CollectorBoardInput{
		Provider: "lever", Site: "example", Region: "global", Enabled: true, IntervalMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Example Employer"})
	if err != nil {
		t.Fatal(err)
	}
	independent, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Independent source record", Kind: "employment", Stage: "discovered",
		SourceURL: "https://jobs.lever.co/example/same-role", OriginalText: string(firstRaw)})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	var firstRoleID string
	save := func(number int, expectedRevision int64, proposedTitle, pattern string) store.Opportunity {
		t.Helper()
		round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
			RequestKey: fmt.Sprintf("source-round-%d", number), Intent: "Save the found Lever opening",
			Outcome: "discover", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
			Scope: store.RoundScope{Resources: []string{"board:" + board.ID, "company:" + company.ID},
				Operations: []string{store.RoundCollectorPage, store.RoundCodexTurn, store.RoundSaveSourceOpportunity},
				Delegates:  []string{agent.ID}},
			Limits: store.RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
		pageAttempt, _, err := db.ReserveCollectorAcquisition(ctx, agent, round.ID,
			store.RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.MarkRoundDispatched(ctx, round.ID, pageAttempt.ID); err != nil {
			t.Fatal(err)
		}
		batch, err := adapter.AcquireLever(ctx, Request{Board: board, MaxPages: 1, MaxItems: 1})
		if err != nil || len(batch.Postings) != 1 {
			t.Fatalf("acquire: %+v %v", batch, err)
		}
		payload, _ := json.Marshal(batch)
		currentRound, err := db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.SaveRoundCollectorBatch(ctx, agent, round.ID, pageAttempt.ID, currentRound.Revision, payload); err != nil {
			t.Fatal(err)
		}
		page, err := db.ListIngestions(ctx, "", 20)
		if err != nil || len(page.Items) != number {
			t.Fatalf("source revisions: %+v %v", page, err)
		}
		latest := page.Items[len(page.Items)-1]
		if number == 2 {
			if err := db.Read(ctx, func(reader store.Reader) error {
				var reason string
				if err := reader.QueryRowContext(ctx, `SELECT reason FROM qualification_refresh_queue WHERE opportunity_id=?`, firstRoleID).Scan(&reason); err != nil {
					return err
				}
				if reason != "source.revision_observed" {
					return fmt.Errorf("changed source did not invalidate this role: %s", reason)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{
			RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "company:" + company.ID,
			Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
			t.Fatal(err)
		}
		capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, created, err := db.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{
			RequestKey: "save", Operation: store.RoundSaveSourceOpportunity,
			ResourceID: "source-opening:" + latest.SourceOpeningID, ExpectedRevision: expectedRevision,
			SourceOpportunity: &store.SourceOpportunityMutationInput{SourceOpeningID: latest.SourceOpeningID,
				ExpectedRevision: expectedRevision, CompanyID: company.ID,
				Opportunity: store.OpportunityInput{Title: proposedTitle, Kind: "employment", WorkPattern: pattern,
					SourceURL: "https://fabricated.invalid", OriginalText: "fabricated", Stage: "applied", Notes: "fabricated"}},
			Capability: capability,
		})
		if err != nil || !created || result.EntityKind != "opportunity" {
			t.Fatalf("source save: %+v created=%v err=%v", result, created, err)
		}
		if err = db.BindRoundThread(ctx, round.ID, turn.ID, turn.Generation, "thread-1"); err != nil {
			t.Fatal(err)
		}
		if err = db.BindRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "thread-1", "turn-1"); err != nil {
			t.Fatal(err)
		}
		if err = db.ObserveRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "thread-1", "turn-1", "completed", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.FinishRoundAttempt(ctx, agent, round.ID, turn.ID, true, json.RawMessage(`{}`), ""); err != nil {
			t.Fatal(err)
		}
		if _, err = db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "source_saved", "partial", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		role, err := db.Opportunity(ctx, result.EntityID)
		if err != nil {
			t.Fatal(err)
		}
		return role
	}
	first := save(1, company.Revision, "Backend Engineer", "onsite")
	firstRoleID = first.ID
	if first.SourceURL != "https://jobs.lever.co/example/same-role" || first.OriginalText != string(firstRaw) ||
		first.Stage != "discovered" || first.Notes != "" {
		t.Fatalf("model source or stage was trusted: %+v", first)
	}
	ownerTitle, ownerNotes, ownerStage, ownerPattern := "Owner-corrected title", "Keep this note", "applied", "remote"
	correctionRound, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
		RequestKey: "owner-correction-round", Intent: "Apply my correction to the found role", Outcome: "correct",
		ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Resources: []string{"company:" + company.ID},
			Operations: []string{store.RoundCodexTurn, store.RoundCorrectOpportunity}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, correctionRound.ID); err != nil {
		t.Fatal(err)
	}
	instruction, createdInstruction, err := db.AddOwnerInstruction(ctx, owner, store.OwnerInstructionInput{
		RequestKey: "correct-role", TargetKind: "opportunity", TargetID: first.ID,
		ExpectedRevision: first.Revision, RoundID: correctionRound.ID,
		Text: "Use my corrected title, mark this role applied and remote, and keep my note.",
	})
	if err != nil || !createdInstruction {
		t.Fatalf("owner instruction: %+v %v %v", instruction, createdInstruction, err)
	}
	correctionTurn, _, err := db.ReserveRoundAttempt(ctx, agent, correctionRound.ID, store.RoundAttemptInput{
		RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "company:" + company.ID,
		Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, correctionRound.ID, correctionTurn.ID); err != nil {
		t.Fatal(err)
	}
	correctionCapability, err := db.IssueRoundToolCapability(ctx, correctionRound.ID, correctionTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	correction, applied, err := db.ApplyRoundMutation(ctx, agent, correctionRound.ID, store.RoundMutationInput{
		RequestKey: "apply-owner-correction", Operation: store.RoundCorrectOpportunity,
		ResourceID: "opportunity:" + first.ID, ExpectedRevision: first.Revision,
		OwnerInstructionID: instruction.ID,
		OpportunityPatch: &store.OpportunityPatch{ExpectedRevision: first.Revision,
			Title: &ownerTitle, Notes: &ownerNotes, Stage: &ownerStage, WorkPattern: &ownerPattern},
		Capability: correctionCapability,
	})
	if err != nil || !applied || correction.EntityID != first.ID {
		t.Fatalf("apply owner correction: %+v %v %v", correction, applied, err)
	}
	if err = db.BindRoundThread(ctx, correctionRound.ID, correctionTurn.ID, correctionTurn.Generation, "correction-thread"); err != nil {
		t.Fatal(err)
	}
	if err = db.BindRoundTurn(ctx, correctionRound.ID, correctionTurn.ID, correctionTurn.Generation, "correction-thread", "correction-turn"); err != nil {
		t.Fatal(err)
	}
	if err = db.ObserveRoundTurn(ctx, correctionRound.ID, correctionTurn.ID, correctionTurn.Generation,
		"correction-thread", "correction-turn", "completed", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRoundAttempt(ctx, agent, correctionRound.ID, correctionTurn.ID, true, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRound(ctx, owner, correctionRound.ID, store.RoundCompleted, "correction_saved", "partial", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	corrected, err := db.Opportunity(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	currentRaw = changedRaw
	mu.Unlock()
	second := save(2, corrected.Revision, "Model changed title", "hybrid")
	if second.ID != first.ID || second.OriginalText != string(changedRaw) ||
		second.Title != ownerTitle || second.Notes != ownerNotes || second.Stage != ownerStage ||
		second.WorkPattern != ownerPattern {
		t.Fatalf("refresh duplicated or erased owner correction: first=%+v second=%+v", first, second)
	}
	mu.Lock()
	failRead = true
	mu.Unlock()
	failureRound, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
		RequestKey: "source-read-failed", Intent: "Inspect a failing Lever page", Outcome: "discover",
		ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Resources: []string{"board:" + board.ID},
			Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, failureRound.ID); err != nil {
		t.Fatal(err)
	}
	failureAttempt, _, err := db.ReserveCollectorAcquisition(ctx, agent, failureRound.ID,
		store.RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, failureRound.ID, failureAttempt.ID); err != nil {
		t.Fatal(err)
	}
	failureBatch, err := adapter.AcquireLever(ctx, Request{Board: board, MaxPages: 1, MaxItems: 1})
	if err != nil || failureBatch.ErrorCode != "lever_http_status" || len(failureBatch.Postings) != 0 || failureBatch.Next == nil {
		t.Fatalf("failed acquisition status: %+v %v", failureBatch, err)
	}
	failurePayload, _ := json.Marshal(failureBatch)
	failureCurrent, err := db.Round(ctx, failureRound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SaveRoundCollectorBatch(ctx, agent, failureRound.ID, failureAttempt.ID, failureCurrent.Revision, failurePayload); err != nil {
		t.Fatal(err)
	}
	unchanged, err := db.Opportunity(ctx, second.ID)
	if err != nil || unchanged.ArchivedAt != "" || unchanged.Revision != second.Revision || unchanged.OriginalText != second.OriginalText {
		t.Fatalf("failed read changed availability or role: %+v %v", unchanged, err)
	}
	if err := db.Read(ctx, func(reader store.Reader) error {
		var roles, snapshots, sightings int
		if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM opportunities`).Scan(&roles); err != nil {
			return err
		}
		if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM evidence_sources WHERE opportunity_id=? AND source_kind='vacancy_snapshot'`, first.ID).Scan(&snapshots); err != nil {
			return err
		}
		if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings`).Scan(&sightings); err != nil {
			return err
		}
		if roles != 2 || snapshots != 2 || sightings != 2 {
			return fmt.Errorf("roles=%d snapshots=%d sightings=%d", roles, snapshots, sightings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if first.ID == independent.ID {
		t.Fatal("uncertain cross-source duplicate was silently merged")
	}
}
