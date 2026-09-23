package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func packReadFixture(t *testing.T, h *harness) (string, string) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "pack-owner"}
	agent := store.Actor{Kind: "agent", ID: "pack-agent"}
	company, _, err := h.db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.invalid/role", OriginalText: "Build Go services.", Stage: "new", WorkPattern: "remote", LocationText: "Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := h.db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "pack-read", Intent: "Prepare fixture pack", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{resource}, Operations: []string{store.RoundCodexTurn, store.RoundPrepareApplicationPack}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = h.db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := h.db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "bound", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := h.db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"opportunityId": opportunity.ID, "opportunityRevision": 1, "profileRevision": 1}, "draft": map[string]any{"focus": map[string]any{"text": "Literal role focus"}}})
	typst := []byte(`#let application_focus = json("focus.json").focus`)
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	content, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, typst, pdf})
	digest := sha256.Sum256(content)
	input := store.RoundMutationInput{RequestKey: "pack", Operation: store.RoundPrepareApplicationPack, ResourceID: resource, ExpectedRevision: 1, Capability: capability,
		ApplicationPack: &store.ApplicationPackMutationInput{OpportunityID: opportunity.ID, ExpectedOpportunityRevision: 1, ExpectedProfileRevision: 1,
			ContentSHA256: hex.EncodeToString(digest[:]), ManifestJSON: manifest, TypstSource: typst, PDF: pdf}}
	result, _, err := h.db.ApplyRoundMutation(ctx, agent, round.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	return opportunity.ID, result.EntityID
}

func TestApplicationPackPrivateReviewAndCompleteSourceArchive(t *testing.T) {
	h := newHarness(t)
	opportunityID, packID := packReadFixture(t, h)
	paths := []string{"/api/v1/opportunities/" + opportunityID + "/application-packs", "/api/v1/application-packs/" + packID, "/api/v1/application-packs/" + packID + "/pdf", "/api/v1/application-packs/" + packID + "/source.zip"}
	for _, path := range paths {
		if response := h.request("GET", path, "", nil, "", "", ""); response.Code != http.StatusUnauthorized {
			t.Fatalf("public pack path %s: %d", path, response.Code)
		}
	}
	cookie, _ := h.login()
	list := h.request("GET", paths[0], "", cookie, "", "", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), packID) {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	review := h.request("GET", paths[1], "", cookie, "", "", "")
	if review.Code != 200 || !strings.Contains(review.Body.String(), "Literal role focus") {
		t.Fatalf("review: %d %s", review.Code, review.Body.String())
	}
	pdf := h.request("GET", paths[2], "", cookie, "", "", "")
	if pdf.Code != 200 || pdf.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(pdf.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("pdf: %d", pdf.Code)
	}
	archiveResponse := h.request("GET", paths[3], "", cookie, "", "", "")
	if archiveResponse.Code != 200 || archiveResponse.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("archive: %d", archiveResponse.Code)
	}
	archive, err := zip.NewReader(bytes.NewReader(archiveResponse.Body.Bytes()), int64(archiveResponse.Body.Len()))
	if err != nil || len(archive.File) != 2 {
		t.Fatalf("zip: %v %d", err, len(archive.File))
	}
	contents := map[string]string{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[file.Name] = string(body)
	}
	if !strings.Contains(contents["cv.typ"], `json("focus.json")`) || !strings.Contains(contents["focus.json"], "Literal role focus") {
		t.Fatalf("incomplete source archive: %v", contents)
	}
}
