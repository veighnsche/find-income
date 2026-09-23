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

func TestCommissionedLeverBatchPublishesIdentityAndRevisions(t *testing.T) {
	ctx := context.Background()
	firstRaw := fixturePosting("one")
	changedRaw := json.RawMessage(`{ "id": "one", "text": "Backend Engineer", "descriptionPlain": "Build Go and Rust services <today>.", "hostedUrl": "https://jobs.lever.co/example/one" }`)
	var mu sync.Mutex
	currentRaw := firstRaw
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		raw := append([]byte(nil), currentRaw...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(append([]byte("["), raw...), ']'))
	}))
	defer server.Close()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "collector-agent"}
	board, err := db.CreateCollectorBoard(ctx, owner, store.CollectorBoardInput{
		Provider: "lever", Site: "example", Region: "global", Enabled: true, IntervalMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stage := func(number int) []store.CollectorSourceOutcome {
		t.Helper()
		round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
			RequestKey: fmt.Sprintf("collect-%d", number), Intent: "Inspect one Lever item", Outcome: "discover",
			ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
			Scope: store.RoundScope{Resources: []string{"board:" + board.ID},
				Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
			Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
		attempt, created, err := db.ReserveCollectorAcquisition(ctx, agent, round.ID,
			store.RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
		if err != nil || !created {
			t.Fatalf("reserve: %+v %v %v", attempt, created, err)
		}
		if _, err = db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
			t.Fatal(err)
		}
		batch, err := adapter.AcquireLever(ctx, Request{Board: board, MaxPages: 1, MaxItems: 1})
		if err != nil || batch.ErrorCode != "" || len(batch.Postings) != 1 {
			t.Fatalf("acquire: %+v %v", batch, err)
		}
		payload, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		current, err := db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.SaveRoundCollectorBatch(ctx, agent, round.ID, attempt.ID, current.Revision, payload); err != nil {
			t.Fatalf("stage: %v", err)
		}
		outcomes, err := db.RoundCollectorOutcomes(ctx, round.ID, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "bounded_item_read", "partial", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		return outcomes
	}
	assertState := func(sightings, ingestions, openings int, text string) {
		t.Helper()
		if err := db.Read(ctx, func(reader store.Reader) error {
			var foundSightings, foundIngestions, foundOpenings int
			if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings`).Scan(&foundSightings); err != nil {
				return err
			}
			if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM ingestion_requests WHERE origin='collector'`).Scan(&foundIngestions); err != nil {
				return err
			}
			if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM source_openings`).Scan(&foundOpenings); err != nil {
				return err
			}
			if foundSightings != sightings || foundIngestions != ingestions || foundOpenings != openings {
				return fmt.Errorf("counts sightings=%d ingestions=%d openings=%d", foundSightings, foundIngestions, foundOpenings)
			}
			var exact int
			if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings WHERE original_text=?`, text).Scan(&exact); err != nil {
				return err
			}
			if exact < 1 {
				return fmt.Errorf("exact source text absent")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	firstOutcome := stage(1)
	if len(firstOutcome) != 1 || firstOutcome[0].Decision != "new" || !firstOutcome[0].Current {
		t.Fatalf("new source outcome: %+v", firstOutcome)
	}
	assertState(1, 1, 1, string(firstRaw))
	secondOutcome := stage(2)
	if len(secondOutcome) != 1 || secondOutcome[0].Decision != "unchanged" || !secondOutcome[0].Current ||
		secondOutcome[0].IngestionID != firstOutcome[0].IngestionID {
		t.Fatalf("unchanged source scheduled extraction: %+v", secondOutcome)
	}
	assertState(2, 1, 1, string(firstRaw))
	mu.Lock()
	currentRaw = changedRaw
	mu.Unlock()
	thirdOutcome := stage(3)
	if len(thirdOutcome) != 1 || thirdOutcome[0].Decision != "changed" || !thirdOutcome[0].Current ||
		thirdOutcome[0].SourceOpeningID != firstOutcome[0].SourceOpeningID ||
		thirdOutcome[0].IngestionID == firstOutcome[0].IngestionID {
		t.Fatalf("changed source identity: %+v", thirdOutcome)
	}
	assertState(3, 2, 1, string(changedRaw))
	badRound, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
		RequestKey: "collect-corrupt", Intent: "Inspect one Lever item", Outcome: "discover",
		ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Resources: []string{"board:" + board.ID},
			Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, badRound.ID); err != nil {
		t.Fatal(err)
	}
	badAttempt, _, err := db.ReserveCollectorAcquisition(ctx, agent, badRound.ID,
		store.RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, badRound.ID, badAttempt.ID); err != nil {
		t.Fatal(err)
	}
	corrupt, err := adapter.AcquireLever(ctx, Request{Board: board, MaxPages: 1, MaxItems: 1})
	if err != nil || len(corrupt.Postings) != 1 {
		t.Fatalf("acquire corrupt fixture: %+v %v", corrupt, err)
	}
	corrupt.Postings[0].ContentSHA256 = "untrusted-digest"
	payload, _ := json.Marshal(corrupt)
	currentRound, err := db.Round(ctx, badRound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SaveRoundCollectorBatch(ctx, agent, badRound.ID, badAttempt.ID, currentRound.Revision, payload); err == nil {
		t.Fatal("corrupt source was staged")
	}
	assertState(3, 2, 1, string(changedRaw))
	if err := db.Read(ctx, func(reader store.Reader) error {
		var staged int
		if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM round_collector_batches WHERE attempt_id=?`, badAttempt.ID).Scan(&staged); err != nil {
			return err
		}
		if staged != 0 {
			return fmt.Errorf("corrupt batch committed: %d", staged)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
