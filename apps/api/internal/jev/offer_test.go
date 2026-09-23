package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func offerTradeoffFixture() OfferTradeoffInput {
	first := "Offer A is EUR 4,800 monthly at 32 hours with remote work."
	second := "Offer B is EUR 5,000 monthly at 40 hours with hybrid work."
	a, b := sha256.Sum256([]byte(first)), sha256.Sum256([]byte(second))
	return OfferTradeoffInput{OfferIDs: []string{"offer-a", "offer-b"}, MaxReportedTokens: 100,
		CalculatedFacts: json.RawMessage(`{"views":[{"offerId":"offer-a","monthlyEquivalent":{"kind":"exact","min":{"numerator":480000,"denominator":1}},"monthlyAssumption":"Direct reported monthly amount at stated weekly hours."},{"offerId":"offer-b","monthlyEquivalent":{"kind":"exact","min":{"numerator":500000,"denominator":1}},"monthlyAssumption":"Direct reported monthly amount at stated weekly hours."}],"pairs":[{"leftId":"offer-a","rightId":"offer-b","status":"incompatible","reason":"Stated weekly hours differ."}]}`),
		Sources: []OfferTradeoffSource{
			{ID: "a", OfferID: "offer-a", Kind: "owner_paste", Revision: "1", SHA256: hex.EncodeToString(a[:]), Body: first},
			{ID: "b", OfferID: "offer-b", Kind: "owner_paste", Revision: "1", SHA256: hex.EncodeToString(b[:]), Body: second},
		},
		Evidence:   []OfferTradeoffEvidence{{ID: "hours-a", SourceID: "a", Excerpt: "32 hours"}, {ID: "hours-b", SourceID: "b", Excerpt: "40 hours"}},
		Pairs:      []OfferTradeoffPair{{LeftID: "offer-a", RightID: "offer-b", Status: "incompatible", Reason: "Stated weekly hours differ."}},
		Candidates: []OfferTradeoffCandidate{{ID: "clarify-hours", Kind: "clarify", Description: "Clarify whether B offers fewer hours.", EvidenceIDs: []string{"hours-a", "hours-b"}}},
	}
}

func TestOfferTradeoffChoiceHasFullContextAndAbstention(t *testing.T) {
	input := offerTradeoffFixture()
	fake := screenFake(func(request Request) (Result, error) {
		state := request.State.(OfferTradeoffInput)
		if len(state.Sources) != 2 || state.Sources[0].Body != input.Sources[0].Body || len(state.Pairs) != 1 || !strings.Contains(string(state.CalculatedFacts), `"numerator":480000`) {
			t.Fatal("full offer context or arithmetic limitation omitted")
		}
		if len(request.Questions[offerTradeoffQuestionID].(ChoiceQuestion).Criteria) != 2 {
			t.Fatal("unresolved option omitted")
		}
		return screenResult(request, map[string]string{offerTradeoffQuestionID: "clarify-hours"}), nil
	})
	selected, err := SelectOfferTradeoff(context.Background(), fake, input)
	if err != nil || selected.Disposition != OfferTradeoffSelected || selected.SelectedID != "clarify-hours" || selected.InputSHA256 == "" || len(selected.RequestSnapshot) == 0 {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}
	abstain := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{offerTradeoffQuestionID: offerTradeoffUnresolved}), nil
	})
	unresolved, err := SelectOfferTradeoff(context.Background(), abstain, input)
	if err != nil || unresolved.Disposition != OfferTradeoffUnresolved || unresolved.SelectedID != "" {
		t.Fatalf("abstention=%+v err=%v", unresolved, err)
	}
}

func TestRecoverCapturedOfferTradeoffUsesExactRequestAndResponse(t *testing.T) {
	input := offerTradeoffFixture()
	raw := []byte(`{"model":"jev-1.13.0","answers":{"offer_tradeoff":{"type":"choice","choice":"clarify-hours","probabilities":{"clarify-hours":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	var request Request
	_, err := SelectOfferTradeoff(context.Background(), screenFake(func(r Request) (Result, error) {
		request = r
		return screenResult(r, map[string]string{offerTradeoffQuestionID: "clarify-hours"}), nil
	}), input)
	if err != nil {
		t.Fatal(err)
	}
	logical, _ := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{request.State, request.Questions})
	recovered, err := RecoverCapturedOfferTradeoff(input, logical, raw, "jev-1.13.0")
	if err != nil || recovered.SelectedID != "clarify-hours" || string(recovered.ProviderResult.RawResponse) != string(raw) {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	logical[0] = '['
	if _, err := RecoverCapturedOfferTradeoff(input, logical, raw, "jev-1.13.0"); err == nil {
		t.Fatal("altered request accepted")
	}
	if _, err := RecoverCapturedOfferTradeoff(input, recovered.RequestSnapshot, []byte(`{"answers":{}}`), "jev-1.13.0"); err == nil {
		t.Fatal("incomplete response accepted")
	}
}

func TestOfferTradeoffRejectsInvalidChoiceAndEvidence(t *testing.T) {
	input := offerTradeoffFixture()
	bad := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{offerTradeoffQuestionID: "accept-offer-b"}), nil
	})
	if _, err := SelectOfferTradeoff(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("unsupported choice accepted: %v", err)
	}
	input.Evidence[0].Excerpt = "invented leave entitlement"
	if _, err := SelectOfferTradeoff(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("unsupported evidence accepted: %v", err)
	}
	input = offerTradeoffFixture()
	input.Sources[0].Body += strings.Repeat("x", 30000)
	input.Sources[0].SHA256 = strings.Repeat("0", 64)
	if _, err := SelectOfferTradeoff(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("changed source bytes accepted: %v", err)
	}
}
