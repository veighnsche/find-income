package jev

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func routeFixture() DeliveryRouteInput {
	return DeliveryRouteInput{OpportunityID: "role-1", RouteID: "route-1", Title: "Backend Engineer",
		SourceURL: "https://employer.example/jobs/1", OriginalText: "Send your application to jobs@employer.example. General questions go to hello@employer.example.",
		SourceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "direct",
		Destination: "jobs@employer.example", RouteSourceKind: "posting", RouteExcerpt: "Send your application to jobs@employer.example.",
		RouteSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MaxReportedTokens: 1000}
}

func TestCapturedRouteBindsChoiceAndEntireInput(t *testing.T) {
	input := routeFixture()
	raw := []byte(`{"model":"test-model","answers":{"delivery_route":{"type":"choice","choice":"application_mailbox","probabilities":{"application_mailbox":1,"other_contact":0,"unresolved":0},"confidence":1}},"usage":{"input_tokens":12,"output_tokens":4}}`)
	logical, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{input, deliveryRouteRequest(input).Questions})
	if err != nil {
		t.Fatal(err)
	}
	result, err := RecoverCapturedDeliveryRoute(input, logical, raw, "test-model")
	if err != nil || result.Choice != "application_mailbox" {
		t.Fatalf("recover captured answer: %+v %v", result, err)
	}
	if _, err := ValidateCapturedDeliveryRoute(input, result, logical, raw, "test-model"); err != nil {
		t.Fatalf("valid capture rejected: %v", err)
	}
	tampered := result
	tampered.Choice = "other_contact"
	if _, err := ValidateCapturedDeliveryRoute(input, tampered, logical, raw, "test-model"); err == nil {
		t.Fatal("caller-selected verdict accepted")
	}
	changed := input
	changed.RouteID = "route-2"
	if _, err := ValidateCapturedDeliveryRoute(changed, result, logical, raw, "test-model"); err == nil {
		t.Fatal("different route bound to captured request")
	}
	changed = input
	changed.OriginalText += " Changed source text."
	if _, err := RecoverCapturedDeliveryRoute(changed, logical, raw, "test-model"); err == nil {
		t.Fatal("changed source replayed from prior capture")
	}
}

func TestRouteAssessmentGetsCompleteEvidenceAndCanAbstain(t *testing.T) {
	input := routeFixture()
	fake := screenFake(func(request Request) (Result, error) {
		state := request.State.(DeliveryRouteInput)
		if state.OriginalText != input.OriginalText || state.Destination != input.Destination || state.RouteExcerpt != input.RouteExcerpt {
			t.Fatal("full route evidence omitted")
		}
		return screenResult(request, map[string]string{deliveryRouteQuestionID: "application_mailbox"}), nil
	})
	result, err := AssessDeliveryRoute(context.Background(), fake, input)
	if err != nil || result.Choice != "application_mailbox" || result.InputSHA256 == "" || len(result.RequestSnapshot) == 0 {
		t.Fatalf("assessment: %+v %v", result, err)
	}
	abstain := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{deliveryRouteQuestionID: "unresolved"}), nil
	})
	result, err = AssessDeliveryRoute(context.Background(), abstain, input)
	if err != nil || result.Choice != "unresolved" {
		t.Fatalf("abstention: %+v %v", result, err)
	}
}

func TestRouteAssessmentRejectsInventedExcerptAndUnsupportedChoice(t *testing.T) {
	input := routeFixture()
	input.RouteExcerpt = "Invented application instruction"
	if _, err := AssessDeliveryRoute(context.Background(), screenFake(nil), input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("invented excerpt accepted: %v", err)
	}
	input = routeFixture()
	fake := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{deliveryRouteQuestionID: "send_now"}), nil
	})
	if _, err := AssessDeliveryRoute(context.Background(), fake, input); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("unsupported action accepted: %v", err)
	}
}
