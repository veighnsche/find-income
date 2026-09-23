package offercomparison

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

func cents(value int64) *int64 { return &value }

func offerFixture() Input {
	first := "Fixture A offers EUR 4,800 gross base monthly for 32 hours weekly, excluding holiday pay. The role is remote and includes a learning budget."
	second := "Fixture B offers EUR 5,000 gross base monthly for 40 hours weekly, excluding holiday pay. The role is hybrid and includes a pension contribution."
	quote := func(id, excerpt string) *applicationpacks.Citation {
		return &applicationpacks.Citation{SourceID: id, Excerpt: excerpt}
	}
	return Input{Sources: []Source{
		{ID: "offer-a-text", OfferID: "offer-a", Kind: "owner_paste", Revision: "paste-1", SHA256: digest(first), Body: first},
		{ID: "offer-b-text", OfferID: "offer-b", Kind: "owner_paste", Revision: "paste-2", SHA256: digest(second), Body: second},
	}, Offers: []Offer{
		{ID: "offer-a", Employer: "Fixture A", Engagement: "employment",
			Pay:         PayTerm{AmountKind: "exact", MinCents: cents(480000), Currency: "EUR", Period: fit.Monthly, Basis: fit.Base, Citation: quote("offer-a-text", "EUR 4,800 gross base monthly")},
			Hours:       HoursTerm{WeeklyHundredths: cents(3200), Citation: quote("offer-a-text", "32 hours weekly")},
			Holiday:     HolidayTerm{Treatment: "excluded", Citation: quote("offer-a-text", "excluding holiday pay")},
			Benefits:    []CitedText{{Text: "Learning budget is reported.", Citations: []applicationpacks.Citation{*quote("offer-a-text", "learning budget")}}},
			Arrangement: []CitedText{{Text: "Remote work is reported.", Citations: []applicationpacks.Citation{*quote("offer-a-text", "remote")}}}},
		{ID: "offer-b", Employer: "Fixture B", Engagement: "employment",
			Pay:         PayTerm{AmountKind: "exact", MinCents: cents(500000), Currency: "EUR", Period: fit.Monthly, Basis: fit.Base, Citation: quote("offer-b-text", "EUR 5,000 gross base monthly")},
			Hours:       HoursTerm{WeeklyHundredths: cents(4000), Citation: quote("offer-b-text", "40 hours weekly")},
			Holiday:     HolidayTerm{Treatment: "excluded", Citation: quote("offer-b-text", "excluding holiday pay")},
			Benefits:    []CitedText{{Text: "Pension contribution is reported.", Citations: []applicationpacks.Citation{*quote("offer-b-text", "pension contribution")}}},
			Arrangement: []CitedText{{Text: "Hybrid work is reported.", Citations: []applicationpacks.Citation{*quote("offer-b-text", "hybrid")}}}},
	}, Alternatives: []Alternative{{ID: "clarify-hours", Kind: "clarify", Why: CitedText{Text: "Clarify whether Fixture B can offer fewer weekly hours before comparing the amounts at equal hours.", Citations: []applicationpacks.Citation{*quote("offer-a-text", "32 hours weekly"), *quote("offer-b-text", "40 hours weekly")}}}}}
}

func TestKnownActualHoursRemainVisibleWithoutProration(t *testing.T) {
	comparison, err := Prepare(offerFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Views) != 2 || len(comparison.Pay) != 1 || comparison.Pay[0].Status != "incompatible" || comparison.Pay[0].Delta != nil {
		t.Fatalf("different-hours result=%+v", comparison)
	}
	if comparison.Views[0].MonthlyEquivalent.Min.Numerator != 480000 || *comparison.Views[0].WeeklyHoursHundredths != 3200 ||
		comparison.Views[1].MonthlyEquivalent.Min.Numerator != 500000 || *comparison.Views[1].WeeklyHoursHundredths != 4000 {
		t.Fatalf("actual-hours monthly facts disappeared: %+v", comparison.Views)
	}
}

func TestExactSameBasisAndCitedAnnualConversion(t *testing.T) {
	input := offerFixture()
	input.Offers[1].Hours.WeeklyHundredths = cents(3200)
	input.Offers[1].Hours.Citation.Excerpt = "40 hours weekly"
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "40 hours weekly", "32 hours weekly", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Hours.Citation.Excerpt = "32 hours weekly"
	input.Alternatives[0].Why.Citations[1].Excerpt = "32 hours weekly"
	input.Alternatives[0].Why.Text = "Weigh the cited offers at the same stated weekly hours."
	comparison, err := Prepare(input)
	if err != nil || comparison.Pay[0].Status != "comparable" || comparison.Pay[0].Delta.Min.Numerator != 20000 || comparison.Pay[0].Delta.Min.Denominator != 1 {
		t.Fatalf("same-hours monthly comparison=%+v err=%v", comparison.Pay, err)
	}
	tradeoff, err := comparison.TradeoffInput(1000)
	if err != nil || !strings.Contains(string(tradeoff.CalculatedFacts), `"deltaRightMinusLeft":{"kind":"exact","min":{"numerator":20000,"denominator":1}}`) {
		t.Fatalf("exact Go delta omitted from Jev input: %s err=%v", tradeoff.CalculatedFacts, err)
	}
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "EUR 5,000 gross base monthly", "EUR 60,000 gross base annually in twelve equal monthly base payments", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Pay.MinCents = cents(6000000)
	input.Offers[1].Pay.Period = fit.Annual
	input.Offers[1].Pay.Citation.Excerpt = "EUR 60,000 gross base annually"
	input.Offers[1].Pay.AnnualConversion = "twelve_equal_monthly_base_payments"
	input.Offers[1].Pay.ConversionCitation = &applicationpacks.Citation{SourceID: "offer-b-text", Excerpt: "twelve equal monthly base payments"}
	comparison, err = Prepare(input)
	if err != nil || comparison.Pay[0].Status != "comparable" || comparison.Pay[0].Period != fit.Monthly ||
		comparison.Pay[0].Delta.Min.Numerator != 20000 || comparison.Views[1].MonthlyEquivalent.Min.Numerator != 500000 {
		t.Fatalf("annual/monthly comparison=%+v err=%v", comparison, err)
	}
	input.Offers[1].Pay.AnnualConversion = ""
	input.Offers[1].Pay.ConversionCitation = nil
	comparison, err = Prepare(input)
	if err != nil || comparison.Pay[0].Status != "unknown" || comparison.Pay[0].Delta != nil || comparison.Views[1].MonthlyEquivalent != nil {
		t.Fatalf("uncited annual conversion assumed: %+v err=%v", comparison, err)
	}
}

func TestRangesMissingTermsAndProjectEconomics(t *testing.T) {
	input := offerFixture()
	input.Offers[1].Hours.WeeklyHundredths = cents(3200)
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "40 hours weekly", "32 hours weekly", 1)
	input.Offers[1].Hours.Citation.Excerpt = "32 hours weekly"
	input.Alternatives[0].Why.Citations[1].Excerpt = "32 hours weekly"
	input.Alternatives[0].Why.Text = "Weigh the cited offers at the same stated weekly hours."
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "EUR 5,000 gross base monthly", "EUR 4,800 to 5,200 gross base monthly", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Pay.AmountKind = "range"
	input.Offers[1].Pay.MinCents, input.Offers[1].Pay.MaxCents = cents(480000), cents(520000)
	input.Offers[1].Pay.Citation.Excerpt = "EUR 4,800 to 5,200 gross base monthly"
	comparison, err := Prepare(input)
	if err != nil || comparison.Pay[0].Status != "comparable" || comparison.Pay[0].Delta.Kind != "range" ||
		comparison.Pay[0].Delta.Min.Numerator != 0 || comparison.Pay[0].Delta.Max.Numerator != 40000 || comparison.Views[1].Reported.Max.Numerator != 520000 {
		t.Fatalf("range comparison=%+v err=%v", comparison, err)
	}
	input.Offers[1].Hours = HoursTerm{}
	comparison, err = Prepare(input)
	if err != nil || comparison.Pay[0].Status != "unknown" || comparison.Pay[0].Delta != nil || len(comparison.Missing[1].Terms) == 0 {
		t.Fatalf("missing hours treated as comparable: %+v err=%v", comparison, err)
	}
	input.Offers[1].Engagement = "project"
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "EUR 4,800 to 5,200 gross base monthly", "EUR 4,800 to 5,200 project fee", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Pay.Citation.Excerpt = "EUR 4,800 to 5,200 project fee"
	input.Offers[1].Pay.Period = fit.ProjectRate
	input.Offers[1].Pay.Basis = fit.UnknownBasis
	input.Offers[1].Holiday = HolidayTerm{Treatment: "unknown"}
	comparison, err = Prepare(input)
	if err != nil || comparison.Pay[0].Status != "project_economics" || comparison.Views[1].Reported == nil || comparison.Views[1].MonthlyEquivalent != nil {
		t.Fatalf("project revenue treated as salary: %+v err=%v", comparison, err)
	}
}

func TestCurrencyAndHolidayBasisCannotBeEquated(t *testing.T) {
	input := offerFixture()
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "EUR 5,000", "GBP 5,000", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Pay.Currency = "GBP"
	input.Offers[1].Pay.Citation.Excerpt = "GBP 5,000 gross base monthly"
	comparison, err := Prepare(input)
	if err != nil || comparison.Pay[0].Status != "incompatible" || comparison.Pay[0].Delta != nil {
		t.Fatalf("cross-currency delta produced: %+v err=%v", comparison.Pay, err)
	}
	input = offerFixture()
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "excluding holiday pay", "including holiday pay", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Holiday.Treatment = "included"
	input.Offers[1].Holiday.Citation.Excerpt = "including holiday pay"
	comparison, err = Prepare(input)
	if err != nil || comparison.Pay[0].Status != "incompatible" || comparison.Pay[0].Delta != nil {
		t.Fatalf("holiday basis mismatch equated: %+v err=%v", comparison.Pay, err)
	}
}

func TestUnsupportedCurrencyPreservesCitedAmountWithoutArithmetic(t *testing.T) {
	input := offerFixture()
	input.Sources[1].Body = strings.Replace(input.Sources[1].Body, "EUR 5,000", "JPY 800,000", 1)
	input.Sources[1].SHA256 = digest(input.Sources[1].Body)
	input.Offers[1].Pay = PayTerm{AmountKind: "raw", RawAmountText: "JPY 800,000", Currency: "JPY", Period: fit.Monthly, Basis: fit.Base,
		Citation: &applicationpacks.Citation{SourceID: "offer-b-text", Excerpt: "JPY 800,000 gross base monthly"}}
	comparison, err := Prepare(input)
	if err != nil || comparison.Views[1].ReportedText != "JPY 800,000" || comparison.Views[1].Reported != nil ||
		comparison.Views[1].MonthlyEquivalent != nil || comparison.Pay[0].Status != "unknown" || comparison.Pay[0].Delta != nil ||
		!strings.Contains(comparison.Pay[0].Reason, "minor-unit") || len(comparison.Missing[1].Terms) == 0 {
		t.Fatalf("unsupported currency lost or compared: %+v err=%v", comparison, err)
	}
	input.Offers[1].Pay.RawAmountText = "JPY 900,000"
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncited raw amount accepted: %v", err)
	}
}

func TestTradeoffUsesRecomputedNumericFacts(t *testing.T) {
	comparison, err := Prepare(offerFixture())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := comparison.TradeoffInput(1000)
	if err != nil {
		t.Fatal(err)
	}
	var facts struct {
		Views []PayView       `json:"views"`
		Pairs []PayComparison `json:"pairs"`
	}
	if err := json.Unmarshal(baseline.CalculatedFacts, &facts); err != nil || len(facts.Views) != 2 || len(facts.Pairs) != 1 ||
		facts.Views[0].MonthlyEquivalent.Min.Numerator != 480000 || facts.Views[1].MonthlyEquivalent.Min.Numerator != 500000 ||
		facts.Views[0].MonthlyAssumption == "" || facts.Pairs[0].Reason == "" {
		t.Fatalf("exact facts or assumptions absent: %+v err=%v", facts, err)
	}
	comparison.Pay[0].Status = "comparable"
	comparison.Pay[0].Reason = "tampered"
	comparison.Pay[0].Delta = &ExactRange{Kind: "exact", Min: ExactCents{Numerator: 1, Denominator: 1}}
	comparison.Views[0].MonthlyEquivalent.Min.Numerator = 1
	comparison.Views[0].MonthlyAssumption = "tampered"
	after, err := comparison.TradeoffInput(1000)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := jev.OfferTradeoffInputDigest(baseline)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := jev.OfferTradeoffInputDigest(after)
	if err != nil || beforeDigest != afterDigest || string(baseline.CalculatedFacts) != string(after.CalculatedFacts) || after.Pairs[0].Reason == "tampered" {
		t.Fatalf("exported comparison tampering changed Jev input: before=%+v after=%+v err=%v", baseline, after, err)
	}
}

func TestCitationAndTradeoffBinding(t *testing.T) {
	input := offerFixture()
	input.Offers[0].Pay.Citation.Excerpt = "invented amount"
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported pay claim accepted: %v", err)
	}
	input = offerFixture()
	input.Offers[0].Pay.Citation.SourceID = "offer-b-text"
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-offer pay source accepted: %v", err)
	}
	comparison, err := Prepare(offerFixture())
	if err != nil {
		t.Fatal(err)
	}
	choiceInput, err := comparison.TradeoffInput(1000)
	if err != nil || len(choiceInput.Sources) != 2 || choiceInput.Sources[0].Body == "" || len(choiceInput.Pairs) != 1 {
		t.Fatalf("Jev context omitted: %+v err=%v", choiceInput, err)
	}
	choiceDigest, err := jev.OfferTradeoffInputDigest(choiceInput)
	if err != nil {
		t.Fatal(err)
	}
	selection := jev.OfferTradeoffResult{Disposition: jev.OfferTradeoffSelected, SelectedID: "clarify-hours", InputSHA256: choiceDigest,
		RequestSnapshot: json.RawMessage(`{"state":"synthetic"}`), ProviderResult: jev.Result{Answers: map[string]jev.Answer{
			"offer_tradeoff": {Type: "choice", Choice: &jev.ChoiceAnswer{Choice: "clarify-hours"}},
		}}}
	prepared, err := comparison.BindTradeoffSelection(selection, 1000)
	if err != nil || prepared.Alternative == nil || prepared.Alternative.ID != "clarify-hours" {
		t.Fatalf("bound choice=%+v err=%v", prepared.Alternative, err)
	}
	comparison.Pay[0].Reason = "tampered"
	comparison.Views[0].MonthlyEquivalent.Min.Numerator = 1
	prepared, err = comparison.BindTradeoffSelection(selection, 1000)
	if err != nil || prepared.Comparison.Pay[0].Reason == "tampered" || prepared.Comparison.Views[0].MonthlyEquivalent.Min.Numerator != 480000 {
		t.Fatalf("mutable exported fields leaked into bound state: %+v err=%v", prepared.Comparison, err)
	}
	selection.InputSHA256 = strings.Repeat("0", 64)
	if _, err := comparison.BindTradeoffSelection(selection, 1000); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale selection accepted: %v", err)
	}
}
