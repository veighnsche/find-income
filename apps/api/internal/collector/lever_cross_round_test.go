package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestCommissionedLeverCursorContinuesAcrossRounds(t *testing.T) {
	ctx := context.Background()
	postings := make([]json.RawMessage, 26)
	for i := range postings {
		postings[i] = fixturePosting(strconv.Itoa(i + 1))
	}
	var mu sync.Mutex
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		mu.Lock()
		offsets = append(offsets, skip)
		mu.Unlock()
		end := skip + pageLimit
		if end > len(postings) {
			end = len(postings)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(postings[skip:end])
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
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	stage := func(number int, prior string, cursor Cursor, pages, items int) (string, Cursor) {
		t.Helper()
		round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{
			RequestKey: fmt.Sprintf("continue-%d", number), Intent: "Continue bounded Lever reading", Outcome: "discover",
			ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
			Scope: store.RoundScope{Resources: []string{"board:" + board.ID},
				Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
			Limits: store.RoundAllowance{Requests: int64(pages), Items: int64(items), Tools: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
		if prior != "" {
			if _, err = db.ImportRoundCollectorCursor(ctx, owner, round.ID, prior); err != nil {
				t.Fatalf("import previous page: %v", err)
			}
		}
		attempt, _, err := db.ReserveCollectorAcquisition(ctx, agent, round.ID,
			store.RoundCollectorAcquisitionInput{RequestKey: "page", BoardID: board.ID,
				CursorAttemptID: prior, MaxPages: pages, MaxItems: items})
		if err != nil {
			t.Fatalf("reserve continuation: %v", err)
		}
		if _, err = db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
			t.Fatal(err)
		}
		batch, err := adapter.AcquireLever(ctx, Request{Board: board, Cursor: cursor, MaxPages: pages, MaxItems: items})
		if err != nil || batch.PagesFetched != pages || batch.ItemsExamined != items || len(batch.Postings) != items {
			t.Fatalf("bounded acquisition: %+v %v", batch, err)
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
			t.Fatal(err)
		}
		saved, err := db.RoundCollectorBatch(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		var restored Batch
		if err = json.Unmarshal(saved, &restored); err != nil {
			t.Fatal(err)
		}
		if _, err = db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "bounded_batch_done", "partial", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if restored.Next == nil {
			return attempt.ID, Cursor{}
		}
		return attempt.ID, *restored.Next
	}
	firstID, firstCursor := stage(1, "", Cursor{}, 1, 3)
	if len(firstCursor.Pending) != 22 || firstCursor.NextOffset != 25 {
		t.Fatalf("first page pending lost: %+v", firstCursor)
	}
	secondID, secondCursor := stage(2, firstID, firstCursor, 0, 22)
	if len(secondCursor.Pending) != 0 || secondCursor.NextOffset != 25 {
		t.Fatalf("buffer did not drain: %+v", secondCursor)
	}
	_, finalCursor := stage(3, secondID, secondCursor, 1, 1)
	if finalCursor.BoardID != "" {
		t.Fatalf("final page did not finish: %+v", finalCursor)
	}
	mu.Lock()
	gotOffsets := append([]int(nil), offsets...)
	mu.Unlock()
	if len(gotOffsets) != 2 || gotOffsets[0] != 0 || gotOffsets[1] != 25 {
		t.Fatalf("continuation refetched or skipped a page: %v", gotOffsets)
	}
	if err := db.Read(ctx, func(reader store.Reader) error {
		rows, err := reader.QueryContext(ctx, `SELECT i.external_id,ss.original_text FROM source_sightings ss
  JOIN ingestion_requests i ON i.id=ss.ingestion_id ORDER BY CAST(i.external_id AS INTEGER)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			var id, exact string
			if err := rows.Scan(&id, &exact); err != nil {
				return err
			}
			if index >= len(postings) || id != strconv.Itoa(index+1) || exact != string(postings[index]) {
				return fmt.Errorf("posting %d lost or rewritten: id=%s", index, id)
			}
			index++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if index != len(postings) {
			return fmt.Errorf("only %d of %d postings persisted", index, len(postings))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
