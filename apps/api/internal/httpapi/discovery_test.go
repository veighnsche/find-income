package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestDiscoveryFindingsReads(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/research/runs/synthetic-run/findings", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("unknown run: got %d, want 404", response.Code)
	}
	response = h.request("GET", "/api/v1/opportunities/synthetic-role/finding", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing finding: got %d, want 404", response.Code)
	}
	response = h.request("GET", "/api/v1/research/runs/synthetic-run/findings?limit=500", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("bad limit: got %d, want 400", response.Code)
	}
	response = h.request("GET", "/api/v1/research/brief", "", nil, "", "", "")
	if response.Code != 401 {
		t.Fatalf("unauthenticated brief: %d", response.Code)
	}
}

func TestSearchBriefAndCatalogReads(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/research/brief", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("brief: %d %s", response.Code, response.Body.String())
	}
	var brief struct {
		ProfileVersion int64  `json:"profileVersion"`
		RubricVersion  string `json:"rubricVersion"`
		CatalogVersion string `json:"catalogVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &brief); err != nil {
		t.Fatal(err)
	}
	if brief.ProfileVersion < 1 || brief.RubricVersion == "" || brief.CatalogVersion != "" {
		t.Fatalf("unauthored brief view: %+v", brief)
	}
	response = h.request("GET", "/api/v1/research/briefs/7/catalog", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("unauthored catalog: %d", response.Code)
	}
	_, err := h.db.AuthorReasonCatalog(context.Background(), store.Actor{Kind: "agent", ID: "codex-runner"},
		store.ReasonCatalogInput{ProfileVersion: brief.ProfileVersion, Rubric: "Prefer backend roles.",
			Positive: []store.ReasonChoice{{ID: "pos-1", Label: "Backend fit", Detail: "Go services."}},
			Negative: []store.ReasonChoice{{ID: "neg-1", Label: "On-site", Detail: "Five office days."}}})
	if err != nil {
		t.Fatal(err)
	}
	response = h.request("GET", "/api/v1/research/brief", "", cookie, "", "", "")
	var authored struct {
		CatalogVersion string `json:"catalogVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &authored); err != nil {
		t.Fatal(err)
	}
	if authored.CatalogVersion == "" {
		t.Fatalf("brief omits catalog version after authoring: %s", response.Body.String())
	}
	response = h.request("GET", "/api/v1/research/briefs/"+fmt.Sprint(brief.ProfileVersion)+"/catalog", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
	}
}
