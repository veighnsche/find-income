// Package offercomparison validates supplied offer terms and calculates only
// exact, explicitly comparable employee pay. It does not accept an offer.
package offercomparison

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

var ErrInvalid = errors.New("invalid sourced offer comparison")
var ErrContextTooLarge = errors.New("offer context exceeds 50000 bytes")

type Source struct {
	ID       string `json:"id"`
	OfferID  string `json:"offerId,omitempty"` // Empty only for owner-supplied priorities.
	Kind     string `json:"kind"`              // owner_paste, employer_offer, recruiter_message, role, or owner_priorities.
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
	Body     string `json:"body"`
}

type CitedText struct {
	Text      string                      `json:"text"`
	Citations []applicationpacks.Citation `json:"citations"`
}

type PayTerm struct {
	AmountKind         string                     `json:"amountKind"`              // exact, range, from, raw, or unknown
	RawAmountText      string                     `json:"rawAmountText,omitempty"` // cited amount when minor units are not established
	MinCents           *int64                     `json:"minCents,omitempty"`
	MaxCents           *int64                     `json:"maxCents,omitempty"`
	Currency           string                     `json:"currency,omitempty"`
	Period             fit.PayPeriod              `json:"period"`
	Basis              fit.PayBasis               `json:"basis"`
	AnnualConversion   string                     `json:"annualConversion,omitempty"`
	Citation           *applicationpacks.Citation `json:"citation,omitempty"`
	ConversionCitation *applicationpacks.Citation `json:"conversionCitation,omitempty"`
}

type HoursTerm struct {
	WeeklyHundredths *int64                     `json:"weeklyHundredths,omitempty"`
	Citation         *applicationpacks.Citation `json:"citation,omitempty"`
}

type HolidayTerm struct {
	Treatment string                     `json:"treatment"` // included, excluded, or unknown
	RateBPS   *int64                     `json:"rateBps,omitempty"`
	Citation  *applicationpacks.Citation `json:"citation,omitempty"`
}

type Offer struct {
	ID          string      `json:"id"`
	Employer    string      `json:"employer"`
	Engagement  string      `json:"engagement"` // employment, project, or unknown
	Pay         PayTerm     `json:"pay"`
	Hours       HoursTerm   `json:"hours"`
	Holiday     HolidayTerm `json:"holiday"`
	Benefits    []CitedText `json:"benefits,omitempty"`
	Arrangement []CitedText `json:"arrangement,omitempty"`
	Unknowns    []string    `json:"unknowns,omitempty"`
}

type Alternative struct {
	ID   string    `json:"id"`
	Kind string    `json:"kind"` // review, clarify, or weigh
	Why  CitedText `json:"why"`
}

type Input struct {
	Sources      []Source      `json:"sources"`
	Offers       []Offer       `json:"offers"`
	Alternatives []Alternative `json:"alternatives,omitempty"`
}

type ExactCents struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

type ExactRange struct {
	Kind string      `json:"kind"` // exact, range, or from
	Min  ExactCents  `json:"min"`
	Max  *ExactCents `json:"max,omitempty"`
}

// PayView keeps the stated actual-hours figure even when a pair cannot be
// compared. It is never a hypothetical pro-rated offer.
type PayView struct {
	OfferID               string        `json:"offerId"`
	Engagement            string        `json:"engagement"`
	Currency              string        `json:"currency,omitempty"`
	Period                fit.PayPeriod `json:"period"`
	Basis                 fit.PayBasis  `json:"basis"`
	WeeklyHoursHundredths *int64        `json:"weeklyHoursHundredths,omitempty"`
	HolidayTreatment      string        `json:"holidayTreatment"`
	Reported              *ExactRange   `json:"reported,omitempty"`
	ReportedText          string        `json:"reportedText,omitempty"`
	MonthlyEquivalent     *ExactRange   `json:"monthlyEquivalent,omitempty"`
	MonthlyAssumption     string        `json:"monthlyAssumption,omitempty"`
}

type PayComparison struct {
	LeftID   string        `json:"leftId"`
	RightID  string        `json:"rightId"`
	Status   string        `json:"status"` // comparable, unknown, incompatible, or project_economics
	Reason   string        `json:"reason"`
	Currency string        `json:"currency,omitempty"`
	Period   fit.PayPeriod `json:"period,omitempty"`
	Left     *ExactRange   `json:"left,omitempty"`
	Right    *ExactRange   `json:"right,omitempty"`
	Delta    *ExactRange   `json:"deltaRightMinusLeft,omitempty"`
}

type OfferMissing struct {
	OfferID string   `json:"offerId"`
	Terms   []string `json:"terms"`
}

type Comparison struct {
	Input       Input           `json:"input"`
	InputSHA256 string          `json:"inputSha256"`
	Views       []PayView       `json:"views"`
	Pay         []PayComparison `json:"pay"`
	Missing     []OfferMissing  `json:"missing"`
}

// Prepare checks byte-exact citations and preserves every supplied term. It
// does not decide legal entitlements, net pay, hiring likelihood, or acceptance.
func Prepare(input Input) (Comparison, error) {
	if len(input.Offers) < 1 || len(input.Offers) > 5 || len(input.Sources) < 1 || len(input.Sources) > 15 || len(input.Alternatives) > 8 {
		return Comparison{}, ErrInvalid
	}
	index := make(map[string]Source, len(input.Sources))
	contextBytes := 0
	for _, source := range input.Sources {
		if !bounded(source.ID, 100) || !bounded(source.Revision, 100) || !validBody(source.Body, 30000) ||
			(source.Kind != "owner_paste" && source.Kind != "employer_offer" && source.Kind != "recruiter_message" && source.Kind != "role" && source.Kind != "owner_priorities") ||
			index[source.ID].ID != "" || digest(source.Body) != source.SHA256 {
			return Comparison{}, ErrInvalid
		}
		contextBytes += len(source.Body)
		if contextBytes > 50000 {
			return Comparison{}, ErrContextTooLarge
		}
		index[source.ID] = source
	}
	seenOffers := map[string]bool{}
	for _, offer := range input.Offers {
		if !bounded(offer.ID, 100) || seenOffers[offer.ID] || !bounded(offer.Employer, 200) ||
			(offer.Engagement != "employment" && offer.Engagement != "project" && offer.Engagement != "unknown") ||
			len(offer.Benefits) > 12 || len(offer.Arrangement) > 12 || len(offer.Unknowns) > 20 {
			return Comparison{}, ErrInvalid
		}
		seenOffers[offer.ID] = true
		foundSource := false
		for _, source := range input.Sources {
			if source.OfferID == offer.ID {
				foundSource = true
				break
			}
		}
		if !foundSource || !validPay(offer.Pay, offer.ID, index) || !validHours(offer.Hours, offer.ID, index) || !validHoliday(offer.Holiday, offer.ID, index) {
			return Comparison{}, ErrInvalid
		}
		for _, term := range append(append([]CitedText(nil), offer.Benefits...), offer.Arrangement...) {
			if !validCited(term, offer.ID, index) {
				return Comparison{}, ErrInvalid
			}
		}
		for _, unknown := range offer.Unknowns {
			if !bounded(unknown, 500) {
				return Comparison{}, ErrInvalid
			}
		}
	}
	for _, source := range input.Sources {
		if source.OfferID != "" && !seenOffers[source.OfferID] || source.OfferID == "" && source.Kind != "owner_priorities" {
			return Comparison{}, ErrInvalid
		}
	}
	seenAlternatives := map[string]bool{}
	for _, alternative := range input.Alternatives {
		if !bounded(alternative.ID, 80) || seenAlternatives[alternative.ID] ||
			(alternative.Kind != "review" && alternative.Kind != "clarify" && alternative.Kind != "weigh") ||
			!validCited(alternative.Why, "", index) || len(alternative.Why.Text) > 1000 {
			return Comparison{}, ErrInvalid
		}
		seenAlternatives[alternative.ID] = true
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 250000 {
		return Comparison{}, ErrInvalid
	}
	var snapshot Input
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return Comparison{}, err
	}
	comparison := Comparison{Input: snapshot, InputSHA256: digestBytes(encoded)}
	for _, offer := range snapshot.Offers {
		comparison.Views = append(comparison.Views, payView(offer))
		comparison.Missing = append(comparison.Missing, OfferMissing{OfferID: offer.ID, Terms: missingTerms(offer)})
	}
	for i := range snapshot.Offers {
		for j := i + 1; j < len(snapshot.Offers); j++ {
			comparison.Pay = append(comparison.Pay, comparePay(snapshot.Offers[i], snapshot.Offers[j]))
		}
	}
	return comparison, nil
}

func validPay(pay PayTerm, offerID string, sources map[string]Source) bool {
	switch pay.Period {
	case fit.Monthly, fit.Annual, fit.Hourly, fit.ProjectRate, fit.UnknownPeriod:
	default:
		return false
	}
	switch pay.Basis {
	case fit.Base, fit.Inclusive, fit.UnknownBasis:
	default:
		return false
	}
	if pay.AmountKind != "exact" && pay.AmountKind != "range" && pay.AmountKind != "from" && pay.AmountKind != "raw" && pay.AmountKind != "unknown" {
		return false
	}
	if (pay.AmountKind == "unknown" || pay.AmountKind == "raw") && (pay.MinCents != nil || pay.MaxCents != nil) ||
		(pay.AmountKind == "exact" || pay.AmountKind == "from") && (pay.MinCents == nil || pay.MaxCents != nil) ||
		pay.AmountKind == "range" && (pay.MinCents == nil || pay.MaxCents == nil) {
		return false
	}
	if pay.MinCents != nil && (*pay.MinCents < 0 || *pay.MinCents > 1_000_000_000_000) ||
		pay.MaxCents != nil && (*pay.MaxCents < *pay.MinCents || *pay.MaxCents > 1_000_000_000_000) {
		return false
	}
	if pay.Currency != "" && !currencyCode(pay.Currency) {
		return false
	}
	if pay.AmountKind == "raw" {
		if !bounded(pay.RawAmountText, 500) || pay.Citation == nil ||
			!validCitation(*pay.Citation, offerID, sources) || !strings.Contains(pay.Citation.Excerpt, pay.RawAmountText) {
			return false
		}
	} else if pay.RawAmountText != "" {
		return false
	}
	if pay.MinCents != nil && !twoDecimalCurrency(pay.Currency) {
		return false
	}
	if pay.AnnualConversion != "" && (pay.Period != fit.Annual || pay.AnnualConversion != "twelve_equal_monthly_base_payments" ||
		pay.ConversionCitation == nil || !validCitation(*pay.ConversionCitation, offerID, sources)) {
		return false
	}
	if pay.ConversionCitation != nil && pay.AnnualConversion == "" {
		return false
	}
	if pay.MinCents != nil || pay.Currency != "" || pay.Period != fit.UnknownPeriod || pay.Basis != fit.UnknownBasis {
		return pay.Citation != nil && validCitation(*pay.Citation, offerID, sources)
	}
	return pay.Citation == nil
}

func validHours(hours HoursTerm, offerID string, sources map[string]Source) bool {
	if hours.WeeklyHundredths == nil {
		return hours.Citation == nil
	}
	return *hours.WeeklyHundredths >= 100 && *hours.WeeklyHundredths <= 16800 &&
		hours.Citation != nil && validCitation(*hours.Citation, offerID, sources)
}

func validHoliday(holiday HolidayTerm, offerID string, sources map[string]Source) bool {
	if holiday.Treatment != "included" && holiday.Treatment != "excluded" && holiday.Treatment != "unknown" {
		return false
	}
	if holiday.RateBPS != nil && (*holiday.RateBPS < 0 || *holiday.RateBPS > 10000) {
		return false
	}
	if holiday.Treatment == "unknown" && holiday.RateBPS == nil {
		return holiday.Citation == nil
	}
	return holiday.Citation != nil && validCitation(*holiday.Citation, offerID, sources)
}

func validCited(term CitedText, offerID string, sources map[string]Source) bool {
	if !bounded(term.Text, 1200) || len(term.Citations) < 1 || len(term.Citations) > 5 {
		return false
	}
	seen := map[string]bool{}
	for _, citation := range term.Citations {
		key := citation.SourceID + "\x00" + citation.Excerpt
		if seen[key] || !validCitation(citation, offerID, sources) {
			return false
		}
		seen[key] = true
	}
	return true
}

func validCitation(citation applicationpacks.Citation, offerID string, sources map[string]Source) bool {
	source, ok := sources[citation.SourceID]
	return ok && (offerID == "" || source.OfferID == offerID) && validBody(citation.Excerpt, 1200) && strings.Contains(source.Body, citation.Excerpt)
}

func missingTerms(offer Offer) []string {
	missing := []string{}
	if offer.Pay.AmountKind == "raw" && !twoDecimalCurrency(offer.Pay.Currency) {
		missing = append(missing, "numeric minor-unit basis for stated currency")
	} else if offer.Pay.MinCents == nil || offer.Pay.Currency == "" || offer.Pay.Period == fit.UnknownPeriod || offer.Pay.Basis == fit.UnknownBasis {
		missing = append(missing, "pay amount, currency, period, or basis")
	}
	if offer.Hours.WeeklyHundredths == nil {
		missing = append(missing, "weekly hours")
	}
	if offer.Holiday.Treatment == "unknown" {
		missing = append(missing, "holiday-pay treatment")
	}
	if len(offer.Benefits) == 0 {
		missing = append(missing, "benefits")
	}
	if len(offer.Arrangement) == 0 {
		missing = append(missing, "work arrangement")
	}
	return missing
}

func comparePay(left, right Offer) PayComparison {
	out := PayComparison{LeftID: left.ID, RightID: right.ID, Status: "unknown"}
	if left.Engagement == "project" || right.Engagement == "project" {
		out.Status, out.Reason = "project_economics", "Project amounts are revenue or rates, not employee salary; scope, costs and utilisation remain separate."
		return out
	}
	if left.Engagement != "employment" || right.Engagement != "employment" {
		out.Reason = "Engagement type is not established for both offers."
		return out
	}
	if (left.Pay.Currency != "" && !twoDecimalCurrency(left.Pay.Currency)) || (right.Pay.Currency != "" && !twoDecimalCurrency(right.Pay.Currency)) {
		out.Reason = "A stated currency has no established minor-unit basis here; arithmetic and FX conversion are unavailable."
		return out
	}
	if left.Pay.MinCents == nil || right.Pay.MinCents == nil || left.Pay.Currency == "" || right.Pay.Currency == "" ||
		left.Hours.WeeklyHundredths == nil || right.Hours.WeeklyHundredths == nil ||
		left.Holiday.Treatment == "unknown" || right.Holiday.Treatment == "unknown" ||
		left.Pay.Basis == fit.UnknownBasis || right.Pay.Basis == fit.UnknownBasis {
		out.Reason = "A directly sourced pay amount, currency, weekly hours, holiday treatment, or basis is missing."
		return out
	}
	if left.Pay.Currency != right.Pay.Currency || *left.Hours.WeeklyHundredths != *right.Hours.WeeklyHundredths ||
		left.Holiday.Treatment != right.Holiday.Treatment || left.Pay.Basis != fit.Base || right.Pay.Basis != fit.Base {
		out.Status, out.Reason = "incompatible", "Currencies, actual weekly hours, gross-base basis, or holiday-pay treatment differ."
		return out
	}
	if left.Pay.AmountKind == "from" || right.Pay.AmountKind == "from" {
		out.Reason = "At least one sourced amount has no upper bound; an exact difference range is unavailable."
		return out
	}
	period := left.Pay.Period
	leftDivisor, rightDivisor := int64(1), int64(1)
	if period != right.Pay.Period {
		if (period != fit.Annual && period != fit.Monthly) || (right.Pay.Period != fit.Annual && right.Pay.Period != fit.Monthly) {
			out.Reason = "Pay periods differ without a supported explicit conversion."
			return out
		}
		if period == fit.Annual {
			if left.Pay.AnnualConversion == "" {
				out.Reason = "Annual offer lacks cited twelve-payment monthly-base conversion."
				return out
			}
			leftDivisor = 12
		} else {
			if right.Pay.AnnualConversion == "" {
				out.Reason = "Annual offer lacks cited twelve-payment monthly-base conversion."
				return out
			}
			rightDivisor = 12
		}
		period = fit.Monthly
	}
	if period != fit.Monthly && period != fit.Annual {
		out.Reason = "Hourly or project period cannot be compared as employee base without additional sourced assumptions."
		return out
	}
	leftRange, leftMin, leftMax := scaledRange(left.Pay, leftDivisor)
	rightRange, rightMin, rightMax := scaledRange(right.Pay, rightDivisor)
	deltaMin := new(big.Rat).Sub(rightMin, leftMax)
	deltaMax := new(big.Rat).Sub(rightMax, leftMin)
	deltaRange := exactRangeFromRats(deltaMin, deltaMax)
	out.Status, out.Reason, out.Currency, out.Period = "comparable", "Same sourced employee gross-base basis, currency, weekly hours, holiday treatment and pay period.", left.Pay.Currency, period
	out.Left, out.Right, out.Delta = &leftRange, &rightRange, &deltaRange
	return out
}

func payView(offer Offer) PayView {
	view := PayView{OfferID: offer.ID, Engagement: offer.Engagement, Currency: offer.Pay.Currency, Period: offer.Pay.Period,
		Basis: offer.Pay.Basis, WeeklyHoursHundredths: offer.Hours.WeeklyHundredths, HolidayTreatment: offer.Holiday.Treatment,
		ReportedText: offer.Pay.RawAmountText}
	if offer.Pay.MinCents == nil {
		return view
	}
	raw, _, _ := scaledRange(offer.Pay, 1)
	view.Reported = &raw
	if offer.Engagement != "employment" {
		return view
	}
	if offer.Pay.Period == fit.Monthly {
		view.MonthlyEquivalent = &raw
		view.MonthlyAssumption = "Direct reported monthly amount at stated weekly hours."
	} else if offer.Pay.Period == fit.Annual && offer.Pay.AnnualConversion == "twelve_equal_monthly_base_payments" {
		monthly, _, _ := scaledRange(offer.Pay, 12)
		view.MonthlyEquivalent = &monthly
		view.MonthlyAssumption = "Cited annual gross base divided by twelve equal monthly base payments at stated weekly hours."
	}
	return view
}

func scaledRange(pay PayTerm, divisor int64) (ExactRange, *big.Rat, *big.Rat) {
	min := new(big.Rat).SetFrac(big.NewInt(*pay.MinCents), big.NewInt(divisor))
	max := new(big.Rat).Set(min)
	if pay.MaxCents != nil {
		max.SetFrac(big.NewInt(*pay.MaxCents), big.NewInt(divisor))
	}
	result := ExactRange{Kind: pay.AmountKind, Min: exactCents(min)}
	if pay.AmountKind == "range" {
		upper := exactCents(max)
		result.Max = &upper
	}
	return result, min, max
}

func exactRangeFromRats(min, max *big.Rat) ExactRange {
	result := ExactRange{Kind: "exact", Min: exactCents(min)}
	if min.Cmp(max) != 0 {
		result.Kind = "range"
		upper := exactCents(max)
		result.Max = &upper
	}
	return result
}

func exactCents(value *big.Rat) ExactCents {
	return ExactCents{Numerator: value.Num().Int64(), Denominator: value.Denom().Int64()}
}

func twoDecimalCurrency(value string) bool {
	switch value {
	case "EUR", "GBP", "USD", "CAD", "AUD", "CHF", "NZD":
		return true
	}
	return false
}

func currencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, letter := range value {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

func bounded(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validBody(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0)
}

func digest(value string) string { return digestBytes([]byte(value)) }
func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
