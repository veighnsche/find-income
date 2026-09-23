package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoundCollectorPartialPageAndBufferedContinuation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "collector-agent"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "collector-three", Intent: "Inspect bounded page",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Resources: []string{"board:lever-one"}, Operations: []string{RoundCollectorPage},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 1, Items: 6, Tools: 2}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, RoundCollectorAcquisitionInput{
		RequestKey: "page", BoardID: "lever-one", MaxPages: 1, MaxItems: 3})
	if err != nil || !created || first.Cost != (RoundAllowance{Requests: 1, Items: 3, Tools: 1}) {
		t.Fatalf("first reservation: %+v %v %v", first, created, err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	r, err = s.Round(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"postings":[{"boardId":"lever-one","originalText":" eCBleGFjdA== "}],"pagesFetched":1,"itemsExamined":3,"next":{"boardId":"lever-one","nextOffset":25,"pending":[{"raw":"IHJhdw==","observedAt":"now"},{"raw":"eCBieXRlcw==","observedAt":"now"}]}}`)
	if _, err := s.SaveRoundCollectorBatch(ctx, agent, r.ID, first.ID, r.Revision, payload); err != nil {
		t.Fatal(err)
	}
	saved, err := s.RoundCollectorBatch(ctx, first.ID)
	if err != nil || !bytes.Equal(saved, payload) {
		t.Fatalf("page bytes changed: %v", err)
	}
	if _, _, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, RoundCollectorAcquisitionInput{
		RequestKey: "premature-page", BoardID: "lever-one", CursorAttemptID: first.ID, MaxPages: 1, MaxItems: 3}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fetched before pending drained: %v", err)
	}
	second, created, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, RoundCollectorAcquisitionInput{
		RequestKey: "buffer", BoardID: "lever-one", CursorAttemptID: first.ID, MaxPages: 0, MaxItems: 3})
	if err != nil || !created || second.Cost != (RoundAllowance{Items: 3, Tools: 1}) {
		t.Fatalf("buffer reservation: %+v %v %v", second, created, err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	r, err = s.Round(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	buffered := json.RawMessage(`{"postings":[],"pagesFetched":0,"itemsExamined":2,"next":{"boardId":"lever-one","nextOffset":25,"pending":[]}}`)
	if _, err := s.SaveRoundCollectorBatch(ctx, agent, r.ID, second.ID, r.Revision, buffered); err != nil {
		t.Fatal(err)
	}
	r, err = s.Round(ctx, r.ID)
	if err != nil || r.Used != (RoundAllowance{Requests: 1, Items: 6, Tools: 2}) {
		t.Fatalf("wrong quantity charge: %+v %v", r.Used, err)
	}
	if _, _, err := s.ReserveCollectorAcquisition(ctx, agent, r.ID, RoundCollectorAcquisitionInput{
		RequestKey: "third", BoardID: "lever-one", CursorAttemptID: second.ID, MaxPages: 1, MaxItems: 1}); !errors.Is(err, ErrAllowance) {
		t.Fatalf("over allowance: %v", err)
	}
}
