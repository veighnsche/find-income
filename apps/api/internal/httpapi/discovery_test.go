package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
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

// D4: the sourced owner-context read serves approved CV/source experience
// with provenance plus the supported owner identity, separately from the
// reusable answer library (this DB holds zero answers and the read still
// serves). Route registration stays I-owned, so this calls the handler
// method directly. No model call happens on the read path by construction:
// the handler only runs the pinned file loader.
func TestSourcedOwnerContextRead(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	handler := &Handler{database: h.db, auth: h.service}
	t.Cleanup(func() { SetOwnerCareerLoader(nil) })

	call := func(target string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.getSourcedOwnerContext(response, request)
		return response
	}

	SetOwnerCareerLoader(func() ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{
			{ID: "cv", Name: "cv-vince-liem.md", SHA256: "sha-cv", Approved: true, Body: "Senior support engineer, ten years."},
			{ID: "cases", Name: "portfolio-case-studies.md", SHA256: "sha-cases", Approved: true, Body: "Case studies."},
		}, nil
	})
	response := call("/api/v1/research/owner-context", cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("owner context: got %d %s, want 200", response.Code, response.Body.String())
	}
	var view SourcedOwnerContextView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Owner.Kind != "administrator" || view.Owner.ID != "owner" {
		t.Fatalf("owner identity: %+v", view.Owner)
	}
	if !view.SourcesConnected || len(view.Sources) != 2 ||
		view.Sources[0].Name != "cv-vince-liem.md" || view.Sources[0].SHA256 != "sha-cv" ||
		!view.Sources[0].Approved || view.Sources[0].Body != "Senior support engineer, ten years." {
		t.Fatalf("career sources: %+v", view)
	}

	SetOwnerCareerLoader(func() ([]applicationpacks.Source, error) {
		return nil, errors.New("approved source changed: cv-vince-liem.md")
	})
	if response := call("/api/v1/research/owner-context", cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken loader: got %d, want honest 503", response.Code)
	}

	SetOwnerCareerLoader(nil)
	response = call("/api/v1/research/owner-context", cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("unwired loader: got %d, want 200 with connected=false", response.Code)
	}
	view = SourcedOwnerContextView{}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.SourcesConnected || len(view.Sources) != 0 || view.Owner.ID != "owner" {
		t.Fatalf("unwired owner context: %+v", view)
	}

	if response := call("/api/v1/research/owner-context", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated owner context: got %d, want 401", response.Code)
	}
}
