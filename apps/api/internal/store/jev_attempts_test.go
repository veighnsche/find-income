package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func jevRound(t *testing.T) (*Store, Round) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := roundInput(t, s, "jev-round", RoundAllowance{Requests: 6, Tools: 3, Turns: 1})
	input.Scope.Operations = append(input.Scope.Operations, RoundJevRequest, RoundCodexTurn, RoundContextTool)
	r, created, err := s.StartRound(ctx, roundOwner(), input)
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	r, err = s.ActivateRound(ctx, roundOwner(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func reservedJevRoundAttempt(t *testing.T, s *Store, r Round, key, operation string) RoundAttempt {
	t.Helper()
	cost, ok := RoundOperationCost(operation)
	if !ok {
		t.Fatal("missing cost")
	}
	a, created, err := s.ReserveRoundAttempt(context.Background(), roundOwner(), r.ID, RoundAttemptInput{
		RequestKey: key, Operation: operation, ResourceID: "source:example", Cost: cost})
	if err != nil || !created {
		t.Fatalf("reserve: %+v %v", a, err)
	}
	a, err = s.MarkRoundDispatched(context.Background(), r.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func testJevStart(r Round, a RoundAttempt) JevAttemptStart {
	return JevAttemptStart{RoundID: r.ID, RoundAttemptID: a.ID, StepIndex: 0, Purpose: "source_research",
		InputSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceRefsJSON: []byte(`[{"id":"source-1","revision":"rev-2"}]`), CandidateSetJSON: []byte(`[{"id":"candidate-1"}]`),
		ProfileVersion: r.ProfileVersion, RubricVersion: "decision-v1", RequestedModel: "jev-1.13.0",
		LogicalRequestJSON: []byte(`{"state":{},"questions":{}}`), TransportRequestBytes: []byte(`{"model":"jev-1.13.0"}`)}
}

func TestJevAttemptRequiresOneChargedJevRequest(t *testing.T) {
	s, r := jevRound(t)
	defer s.Close()
	for _, operation := range []string{RoundCodexTurn, RoundContextTool} {
		a := reservedJevRoundAttempt(t, s, r, "wrong-"+operation, operation)
		if _, err := s.BeginJevAttempt(context.Background(), testJevStart(r, a)); !errors.Is(err, ErrFenced) {
			t.Fatalf("%s attempt allowed Jev transport: %v", operation, err)
		}
	}
	a := reservedJevRoundAttempt(t, s, r, "jev-1", RoundJevRequest)
	first, err := s.BeginJevAttempt(context.Background(), testJevStart(r, a))
	if err != nil || first.Status != "dispatched" {
		t.Fatalf("begin: %+v %v", first, err)
	}
	if _, err := s.BeginJevAttempt(context.Background(), testJevStart(r, a)); !errors.Is(err, ErrFenced) {
		t.Fatalf("second transport under one charge allowed: %v", err)
	}
	changed := testJevStart(r, a)
	changed.StepIndex = 1
	if _, err := s.BeginJevAttempt(context.Background(), changed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("extra step bypassed charge: %v", err)
	}
}

func TestJevAttemptRawFailureAndNullableUsage(t *testing.T) {
	s, r := jevRound(t)
	defer s.Close()
	a := reservedJevRoundAttempt(t, s, r, "jev-invalid", RoundJevRequest)
	started, err := s.BeginJevAttempt(context.Background(), testJevStart(r, a))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte{0, '{', '"', 'x', '"', ':', 0xff, '}'}
	status := 200
	zero := int64(0)
	finished, err := s.FinishJevAttempt(context.Background(), JevAttemptFinish{ID: started.ID, Status: "invalid_response",
		RawResponseBytes: raw, ResponseTruncated: true, ResponseReadError: true, ErrorKind: "response_truncated",
		HTTPStatus: &status, InputTokens: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(finished.RawResponseBytes, raw) || !bytes.Equal(finished.TransportRequestBytes, started.TransportRequestBytes) ||
		finished.InputTokens == nil || *finished.InputTokens != 0 || finished.OutputTokens != nil || !finished.ResponseTruncated || !finished.ResponseReadError || finished.Status != "invalid_response" {
		t.Fatalf("raw/nullable attempt lost: %#v", finished)
	}
	if _, err := s.FinishJevAttempt(context.Background(), JevAttemptFinish{ID: started.ID, Status: "succeeded"}); !errors.Is(err, ErrFenced) {
		t.Fatalf("terminal response overwritten: %v", err)
	}
}
