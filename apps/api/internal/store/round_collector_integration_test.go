package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestCollectorFinalPageDrainsThenMovesToNextScopedBoard(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "collector-agent"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "two-boards", Intent: "Inspect two boards",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Resources: []string{"board:board-a", "board:board-b"},
			Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 2, Items: 6, Tools: 3}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	stage := func(key, board, previous string, pages, items int, batch collector.Batch) string {
		t.Helper()
		a, created, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, store.RoundCollectorAcquisitionInput{
			RequestKey: key, BoardID: board, CursorAttemptID: previous, MaxPages: pages, MaxItems: items})
		if err != nil || !created {
			t.Fatalf("reserve %s: %+v %v", key, a, err)
		}
		if _, err := s.MarkRoundDispatched(ctx, r.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		current, err := s.Round(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveRoundCollectorBatch(ctx, agent, r.ID, a.ID, current.Revision, payload); err != nil {
			t.Fatal(err)
		}
		saved, err := s.RoundCollectorBatch(ctx, a.ID)
		if err != nil || !bytes.Equal(saved, payload) {
			t.Fatalf("staged source bytes changed: %v", err)
		}
		return a.ID
	}
	raw := []byte(" {\"id\": \"exact raw\"} ")
	first := stage("a-final-page", "board-a", "", 1, 3, collector.Batch{
		Postings: []collector.StagedPosting{{BoardID: "board-a", OriginalText: raw}},
		Next: &collector.Cursor{BoardID: "board-a", NextOffset: 5, EndOfBoard: true,
			Pending: []collector.PendingPosting{{Raw: []byte(" item-four "), ObservedAt: "now"}, {Raw: []byte(" item-five "), ObservedAt: "now"}}},
		PagesFetched: 1, ItemsExamined: 3})
	if _, _, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, store.RoundCollectorAcquisitionInput{
		RequestKey: "skip-a", BoardID: "board-b", CursorAttemptID: first, MaxPages: 1, MaxItems: 1}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("skipped unread board-a postings: %v", err)
	}
	second := stage("a-buffer", "board-a", first, 0, 2, collector.Batch{
		Postings: []collector.StagedPosting{{BoardID: "board-a", OriginalText: []byte(" item-four ")},
			{BoardID: "board-a", OriginalText: []byte(" item-five ")}},
		PagesFetched: 0, ItemsExamined: 2, Next: nil})
	if _, _, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, store.RoundCollectorAcquisitionInput{
		RequestKey: "restart-a", BoardID: "board-a", CursorAttemptID: second, MaxPages: 1, MaxItems: 1}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("restarted completed board-a: %v", err)
	}
	stage("b-first", "board-b", second, 1, 1, collector.Batch{
		Postings:     []collector.StagedPosting{{BoardID: "board-b", OriginalText: []byte(" board-b ")}},
		PagesFetched: 1, ItemsExamined: 1, Next: nil})
	r, err = s.Round(ctx, r.ID)
	if err != nil || r.Used != (store.RoundAllowance{Requests: 2, Items: 6, Tools: 3}) {
		t.Fatalf("wrong two-board charge: %+v %v", r.Used, err)
	}
	var cursor struct {
		CompletedBoardIDs []string `json:"completedBoardIds"`
	}
	if err := json.Unmarshal(r.Cursor, &cursor); err != nil || len(cursor.CompletedBoardIDs) != 2 ||
		cursor.CompletedBoardIDs[0] != "board-a" || cursor.CompletedBoardIDs[1] != "board-b" {
		t.Fatalf("source completion history: %+v %v", cursor, err)
	}
}
