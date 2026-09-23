package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAdvertisedAnnualConversionNeedsCurrentExactSourceSpan(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	status, body := h.owner("POST", "/companies", `{"name":"Synthetic Platform Company"}`)
	requireStatus(t, status, 201, body)
	companyID := decodeObject(t, body)["company"].(map[string]any)["id"].(string)
	original := "Terms 🧭: annual gross base 72000 EUR, paid in twelve equal monthly base payments."
	quote := "twelve equal monthly base payments"
	start := strings.Index(original, quote)
	if start < 0 {
		t.Fatal("test quote missing")
	}
	input := map[string]any{
		"companyId": companyID, "title": "Systems Engineer", "kind": "employment",
		"stage": "saved", "originalText": original,
		"compensation": map[string]any{
			"currency": "EUR", "period": "year", "basis": "base",
			"minAmountCents": 7200000, "referenceHours": "37.5",
			"annualConversion":          "twelve_equal_monthly_base_payments",
			"annualConversionSpanStart": start, "annualConversionSpanEnd": start + len(quote),
		},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner("POST", "/opportunities", string(encoded))
	requireStatus(t, status, 201, body)
	opportunity := decodeObject(t, body)["opportunity"].(map[string]any)
	compensation := opportunity["compensation"].(map[string]any)
	if compensation["annualConversionSpanStart"] != float64(start) ||
		compensation["annualConversionSpanEnd"] != float64(start+len(quote)) ||
		compensation["referenceHours"] != "37.5" {
		t.Fatalf("source-backed conversion or fractional hours lost: %s", body)
	}
	id := opportunity["id"].(string)
	status, body = h.owner("PATCH", "/opportunities/"+id,
		fmt.Sprintf(`{"expectedRevision":1,"originalText":%q}`, strings.Replace(original, quote, "bonus inclusive payment arrangement", 1)))
	requireStatus(t, status, 400, body)
	missingSpan := input["compensation"].(map[string]any)
	delete(missingSpan, "annualConversionSpanEnd")
	encoded, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner("POST", "/opportunities", string(encoded))
	requireStatus(t, status, 400, body)
}
