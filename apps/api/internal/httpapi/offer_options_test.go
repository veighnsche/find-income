package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOfferAlternativesRequireExactCurrentSourceAndLabels(t *testing.T) {
	h := newRecordHTTP(t)
	opportunityID, _, _ := evidenceFixture(t, h)
	path := "/opportunities/" + opportunityID
	status, body := h.owner("GET", path+"/qualification")
	requireStatus(t, status, 200, body)
	versions := evidenceVersions(t, decodeObject(t, body))
	statement := "Plans 🧭: choose Core API or Data Platform for this role."
	sourceBody := fmt.Sprintf(`{"expectedContextVersion":%.0f,"statement":{"speakerAffiliation":"employer_representative","speakerName":"Synthetic Hiring Lead","speakerRole":"Hiring Lead","speakerOrganisation":"Synthetic Company","channel":"email","occurredAt":"2026-09-23T10:00:00Z","originalText":%q}}`, versions["contextVersion"].(float64), statement)
	status, body = h.owner("POST", path+"/evidence-sources", sourceBody)
	requireStatus(t, status, 201, body)
	result := decodeObject(t, body)
	sourceID := result["source"].(map[string]any)["id"].(string)
	versions = evidenceVersions(t, result)
	quote := "choose Core API or Data Platform"
	start := strings.Index(statement, quote)
	if start < 0 {
		t.Fatal("missing quote")
	}
	request := map[string]any{
		"sourceId":                sourceID,
		"expectedContextVersion":  versions["contextVersion"],
		"expectedEvidenceVersion": versions["evidenceVersion"],
		"spanStart":               start, "spanEnd": start + len(quote),
		"labels": []string{"Core API", "Data Platform"},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner("POST", path+"/offer-option-sets", string(encoded))
	requireStatus(t, status, 201, body)
	created := decodeObject(t, body)
	set := created["set"].(map[string]any)
	if set["sourceExcerpt"] != quote || len(set["options"].([]any)) != 2 ||
		evidenceVersions(t, created)["evidenceVersion"].(float64) <= versions["evidenceVersion"].(float64) {
		t.Fatalf("option proof or evidence version missing: %s", body)
	}
	status, body = h.owner("GET", path+"/offer-option-sets")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("current option set missing: %s", body)
	}
	status, body = h.owner("POST", path+"/offer-option-sets", string(encoded))
	requireStatus(t, status, 409, body)
	request["expectedEvidenceVersion"] = evidenceVersions(t, created)["evidenceVersion"]
	request["labels"] = []string{"Core API", "Unquoted choice"}
	encoded, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner("POST", path+"/offer-option-sets", string(encoded))
	requireStatus(t, status, 400, body)
}
