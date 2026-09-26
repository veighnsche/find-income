package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestMaterialReadsLive(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	for _, path := range []string{
		"/api/v1/opportunities/synthetic-role/materials/current",
		"/api/v1/opportunities/synthetic-role/materials/versions/1",
	} {
		response := h.request("GET", path, "", cookie, "", "", "")
		if response.Code != 404 {
			t.Fatalf("GET %s: got %d, want honest 404", path, response.Code)
		}
	}

	opportunity := createCheckedOpportunity(t, h, "material-read-1")
	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/materials/current", "", cookie, "", "", "")
	var status struct {
		Status  string `json:"status"`
		Current *struct {
			Version int64 `json:"version"`
		} `json:"current"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || status.Status != "not_prepared" || status.Current != nil {
		t.Fatalf("current unprepared: %d %+v", response.Code, status)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/materials/versions/1", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing version: got %d, want 404", response.Code)
	}
	for _, version := range []string{"0", "-2", "abc"} {
		response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/materials/versions/"+version, "", cookie, "", "", "")
		if response.Code != 400 {
			t.Fatalf("version %s: got %d, want 400", version, response.Code)
		}
	}
}

func TestMaterialWritesHonestUnavailable(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	// The default harness wires no MaterialPreparer, so all three
	// operations report unavailable through the nil guard.
	writes := []struct{ method, path, body string }{
		{"POST", "/api/v1/opportunities/synthetic-role/materials/prepare", `{"requestKey":"k","expectedCheckId":"c","expectedQuestionSetSha256":"s","expectedWorkflowRevision":0}`},
		{"PUT", "/api/v1/opportunities/synthetic-role/materials/current", `{"requestKey":"k","expectedVersion":1,"text":"exact"}`},
		{"POST", "/api/v1/opportunities/synthetic-role/materials/rewrite", `{"requestKey":"k","expectedVersion":1}`},
	}
	for _, write := range writes {
		response := h.request(write.method, write.path, write.body, cookie, "", csrf, origin)
		if response.Code != 503 {
			t.Fatalf("%s %s: got %d, want honest 503", write.method, write.path, response.Code)
		}
	}
}

type stubPreparer struct {
	editView       store.MaterialVersionView
	editCreated    bool
	editErr        error
	rewriteView    store.MaterialVersionView
	rewriteCreated bool
	rewriteErr     error
	editCalls      int
	rewriteCalls   int
	lastText       string
	lastInstruct   string
	draftSet       store.ArtifactReadinessSet
	draftCreated   bool
	draftErr       error
	draftCalls     int
}

func (s *stubPreparer) PrepareOpportunityMaterials(context.Context, store.Actor, string, string, string, string, int64) (store.MaterialStatusView, bool, error) {
	return store.MaterialStatusView{Status: store.MaterialStatusNotPrepared}, false, nil
}

func (s *stubPreparer) EditOpportunityMaterials(_ context.Context, _ store.Actor, _ string, _ string, _ int64, text string) (store.MaterialVersionView, bool, error) {
	s.editCalls++
	s.lastText = text
	return s.editView, s.editCreated, s.editErr
}

func (s *stubPreparer) RewriteOpportunityMaterials(_ context.Context, _ store.Actor, _ string, _ string, _ int64, instruction string) (store.MaterialVersionView, bool, error) {
	s.rewriteCalls++
	s.lastInstruct = instruction
	return s.rewriteView, s.rewriteCreated, s.rewriteErr
}

func (s *stubPreparer) DraftOpportunityArtifacts(context.Context, store.Actor, string, string, string, string, int64) (store.ArtifactReadinessSet, bool, error) {
	s.draftCalls++
	return s.draftSet, s.draftCreated, s.draftErr
}

func materialVersionFixture(origin string, version int64) store.MaterialVersionView {
	return store.MaterialVersionView{PackID: "pack-" + origin, Version: version,
		OpportunityID: "synthetic-role", OpportunityRevision: 2, ProfileRevision: 3,
		CheckID: "check-1", QuestionSetSHA256: "abc", CreatedAt: "2026-09-25T10:00:00Z",
		Answers: []store.MaterialAnswerRef{{QuestionID: "q1", QuestionTextSHA256: "qsha",
			AnswerVersion: 1, TextSHA256: "tsha"}},
		Readiness:  store.MaterialReadiness{Ready: true},
		Provenance: store.MaterialProvenance{Origin: origin, SourceShas: []string{"sha"}},
		CreatedBy:  store.Actor{Kind: "administrator", ID: "owner"}}
}

func TestMaterialEditLive(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	stub := &stubPreparer{editView: materialVersionFixture("direct_edit", 2), editCreated: true}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Materials: stub})

	response := h.request("PUT", "/api/v1/opportunities/synthetic-role/materials/current",
		`{"requestKey":"edit-1","expectedVersion":1,"text":"exact bytes ✓"}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("edit create: got %d, want 201", response.Code)
	}
	var view struct {
		Version    int64  `json:"version"`
		PackID     string `json:"packId"`
		Provenance struct {
			Origin string `json:"origin"`
		} `json:"provenance"`
		Answers []struct {
			QuestionID string `json:"questionId"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Version != 2 || view.PackID != "pack-direct_edit" || view.Provenance.Origin != "direct_edit" || len(view.Answers) != 1 {
		t.Fatalf("edit model: %+v", view)
	}
	if stub.editCalls != 1 || stub.lastText != "exact bytes ✓" {
		t.Fatalf("edit passthrough: calls=%d text=%q", stub.editCalls, stub.lastText)
	}

	stub.editCreated = false
	response = h.request("PUT", "/api/v1/opportunities/synthetic-role/materials/current",
		`{"requestKey":"edit-1","expectedVersion":1,"text":"exact bytes ✓"}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("edit replay: got %d, want 200", response.Code)
	}

	stub.editErr = store.ErrConflict
	response = h.request("PUT", "/api/v1/opportunities/synthetic-role/materials/current",
		`{"requestKey":"edit-2","expectedVersion":1,"text":"stale"}`, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("edit conflict: got %d, want 409", response.Code)
	}
}

func TestMaterialRewriteLive(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	stub := &stubPreparer{rewriteView: materialVersionFixture("rewrite", 3), rewriteCreated: true}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Materials: stub})

	response := h.request("POST", "/api/v1/opportunities/synthetic-role/materials/rewrite",
		`{"requestKey":"rw-1","expectedVersion":2,"instruction":"shorter"}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("rewrite create: got %d, want 201", response.Code)
	}
	if stub.rewriteCalls != 1 || stub.lastInstruct != "shorter" {
		t.Fatalf("rewrite passthrough: calls=%d instruction=%q", stub.rewriteCalls, stub.lastInstruct)
	}

	response = h.request("POST", "/api/v1/opportunities/synthetic-role/materials/rewrite",
		`{"requestKey":"rw-2","expectedVersion":3}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("rewrite without instruction: got %d, want 201", response.Code)
	}
	if stub.lastInstruct != "" {
		t.Fatalf("missing instruction: got %q, want empty", stub.lastInstruct)
	}

	stub.rewriteErr = store.ErrConflict
	response = h.request("POST", "/api/v1/opportunities/synthetic-role/materials/rewrite",
		`{"requestKey":"rw-3","expectedVersion":2}`, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("rewrite conflict: got %d, want 409", response.Code)
	}
}
