package httpapi

import (
	"encoding/json"
	"testing"
)

func TestClarificationEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity, check := completeMatchCheck(t, h, "clar-select-1")

	open := `{"requestKey":"clar-http-1","checkId":"` + check.ID + `",` +
		`"requirement":{"statement":"must hold a forklift certificate","captureId":"cap-1","spanStart":10,"spanEnd":46},` +
		`"prompt":"Do you hold a forklift certificate?","affectedWork":[{"kind":"artifact","id":"art-cv"}]}`
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications", open, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("open: got %d %s, want 201", response.Code, response.Body.String())
	}
	var opened struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ID == "" || opened.Origin != "owner_clarification" || opened.Status != "open" {
		t.Fatalf("opened: %+v, want an open owner clarification", opened)
	}

	replay := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications", open, cookie, "", csrf, origin)
	var replayed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(replay.Body.Bytes(), &replayed)
	if replay.Code != 200 || replayed.ID != opened.ID {
		t.Fatalf("replay: got %d %+v, want 200 same id", replay.Code, replayed)
	}

	changed := `{"requestKey":"clar-http-1","checkId":"` + check.ID + `",` +
		`"requirement":{"statement":"must hold a forklift certificate","captureId":"cap-1","spanStart":10,"spanEnd":46},` +
		`"prompt":"A different prompt.","affectedWork":[{"kind":"artifact","id":"art-cv"}]}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications", changed, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("changed-input replay: got %d, want 409", response.Code)
	}

	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/clarifications", "", cookie, "", "", "")
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
		} `json:"items"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &list)
	if response.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != opened.ID {
		t.Fatalf("list: got %d %+v, want the one clarification", response.Code, list)
	}

	answer := `{"requestKey":"clar-ans-http-1","text":"RTITB counterbalance since 2019."}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications/"+opened.ID+"/answer", answer, cookie, "", csrf, origin)
	var done struct {
		Status string `json:"status"`
		Answer string `json:"answer"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &done)
	if response.Code != 200 || done.Status != "answered" || done.Answer != "RTITB counterbalance since 2019." {
		t.Fatalf("answer: got %d %+v, want the exact saved answer", response.Code, done)
	}

	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications/"+opened.ID+"/answer", answer, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("same-text answer replay: got %d, want 200", response.Code)
	}
	other := `{"requestKey":"clar-ans-http-2","text":"A different answer."}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/clarifications/"+opened.ID+"/answer", other, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("second answer: got %d, want 409", response.Code)
	}

	second, _ := completeMatchCheck(t, h, "clar-select-2")
	response = h.request("POST", "/api/v1/opportunities/"+second.ID+"/clarifications/"+opened.ID+"/answer", answer, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("cross-job answer: got %d, want 404", response.Code)
	}
	unselected := createUnselectedOpportunity(t, h)
	response = h.request("POST", "/api/v1/opportunities/"+unselected.ID+"/clarifications", open, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("unselected open: got %d, want 404", response.Code)
	}
}
