package offercomparison

import (
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type PreparedComparison struct {
	Comparison  Comparison              `json:"comparison"`
	Alternative *Alternative            `json:"alternative,omitempty"`
	Selection   jev.OfferTradeoffResult `json:"selection"`
}

// TradeoffInput sends the complete bounded supplied context and exact cited
// alternatives to a separately commissioned Jev choice.
func (c Comparison) TradeoffInput(maxReportedTokens int64) (jev.OfferTradeoffInput, error) {
	validated, err := Prepare(c.Input)
	if err != nil || validated.InputSHA256 != c.InputSHA256 || len(c.Input.Alternatives) == 0 {
		return jev.OfferTradeoffInput{}, ErrInvalid
	}
	input := jev.OfferTradeoffInput{MaxReportedTokens: maxReportedTokens}
	facts, err := json.Marshal(struct {
		Views []PayView       `json:"views"`
		Pairs []PayComparison `json:"pairs"`
	}{validated.Views, validated.Pay})
	if err != nil {
		return jev.OfferTradeoffInput{}, ErrInvalid
	}
	input.CalculatedFacts = facts
	for _, offer := range validated.Input.Offers {
		input.OfferIDs = append(input.OfferIDs, offer.ID)
	}
	for _, source := range validated.Input.Sources {
		input.Sources = append(input.Sources, jev.OfferTradeoffSource{ID: source.ID, OfferID: source.OfferID,
			Kind: source.Kind, Revision: source.Revision, SHA256: source.SHA256, Body: source.Body})
	}
	for _, pair := range validated.Pay {
		input.Pairs = append(input.Pairs, jev.OfferTradeoffPair{LeftID: pair.LeftID, RightID: pair.RightID,
			Status: pair.Status, Reason: pair.Reason})
	}
	seen := map[string]string{}
	for _, alternative := range validated.Input.Alternatives {
		candidate := jev.OfferTradeoffCandidate{ID: alternative.ID, Kind: alternative.Kind, Description: alternative.Why.Text}
		for _, citation := range alternative.Why.Citations {
			key := citation.SourceID + "\x00" + citation.Excerpt
			id := seen[key]
			if id == "" {
				id = "e-" + digest(key)[:16]
				seen[key] = id
				input.Evidence = append(input.Evidence, jev.OfferTradeoffEvidence{ID: id, SourceID: citation.SourceID, Excerpt: citation.Excerpt})
			}
			candidate.EvidenceIDs = append(candidate.EvidenceIDs, id)
		}
		input.Candidates = append(input.Candidates, candidate)
	}
	return input, nil
}

// BindTradeoffSelection rejects a stale or omitted Jev choice. It does not
// turn a qualitative recommendation into authority to accept an offer.
func (c Comparison) BindTradeoffSelection(result jev.OfferTradeoffResult, maxReportedTokens int64) (PreparedComparison, error) {
	input, err := c.TradeoffInput(maxReportedTokens)
	if err != nil {
		return PreparedComparison{}, err
	}
	validated, err := Prepare(c.Input)
	if err != nil {
		return PreparedComparison{}, ErrInvalid
	}
	digest, err := jev.OfferTradeoffInputDigest(input)
	if err != nil || digest != result.InputSHA256 || len(result.RequestSnapshot) == 0 {
		return PreparedComparison{}, ErrInvalid
	}
	answer, ok := result.ProviderResult.Answers["offer_tradeoff"]
	if !ok || answer.Choice == nil {
		return PreparedComparison{}, ErrInvalid
	}
	prepared := PreparedComparison{Comparison: validated, Selection: result}
	if result.Disposition == jev.OfferTradeoffUnresolved {
		if result.SelectedID != "" || answer.Choice.Choice != "__unresolved__" {
			return PreparedComparison{}, ErrInvalid
		}
		return prepared, nil
	}
	if result.Disposition != jev.OfferTradeoffSelected || result.SelectedID != answer.Choice.Choice {
		return PreparedComparison{}, ErrInvalid
	}
	for _, alternative := range validated.Input.Alternatives {
		if alternative.ID == result.SelectedID {
			copy := alternative
			prepared.Alternative = &copy
			return prepared, nil
		}
	}
	return PreparedComparison{}, ErrInvalid
}
