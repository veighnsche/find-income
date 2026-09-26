package researchwire

// Saved records must reach the run checkpoint (T27 regression): the
// records_save tool returned saved ids in-band but never journaled them,
// so run views, saved counts and reports always showed zero saves. This
// test drives records_save over the real MCP bridge against the real
// toolchain and pins the durable effect: the checkpoint carries the saved
// record ids, the journal holds one run.saved event per batch key, and an
// exact replay records once with the same ids.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestRecordsSaveToolRecordsCheckpoint(t *testing.T) {
	h := newHarness(t)
	h.commissionSupervisor(t)
	_, capID := h.fetch(t, "noted-fetch-1", h.board.URL+"/roles/1")
	body := captureBytes(t, h, capID)
	s1s, s1e := spanOf(t, body, "REQ-NW-101")
	t1s, t1e := spanOf(t, body, "Senior Backend Engineer")
	ref := researchcontract.EvidenceRef{CaptureID: capID, SpanStart: s1s, SpanEnd: s1e}
	ref2 := researchcontract.EvidenceRef{CaptureID: capID, SpanStart: t1s, SpanEnd: t1e}
	assessID := h.assess(t, "noted-assess-1", "noted-q-1", "same",
		[]researchcontract.EvidenceRef{ref}, []researchcontract.EvidenceRef{ref2})

	token := strings.Repeat("n", 64)
	svc, err := codexservice.New(h.ctx, h.db, codexservice.Config{
		Host: "isolated.test", User: "runner", IdentityFile: "/key", KnownHostsFile: "/known",
		Launcher: "/runner/launch", IsolationVerified: true, BridgeToken: token,
		Model: "test-model", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetResearchTools(h.stack.Toolchain)
	server := httptest.NewServer(svc.MCPHandler())
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "t27", Version: "1"}, nil)
	session, err := client.Connect(h.ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: &http.Client{Transport: bearerTransport{token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		t.Fatal("codex turn cost missing")
	}
	attempt, _, err := h.db.ReserveRoundAttempt(h.ctx, h.agent, h.runID, store.RoundAttemptInput{
		RequestKey: "t27-noted-turn", Operation: store.RoundCodexTurn,
		ResourceID: store.ResearchAuthorityResource, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(h.ctx, h.runID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := h.db.IssueRoundToolCapability(h.ctx, h.runID, attempt.ID, h.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	link := func(s, e int64) map[string]any {
		return map[string]any{"captureId": capID, "spanStart": float64(s), "spanEnd": float64(e),
			"excerptSha256": excerptSHA(body, int(s), int(e))}
	}
	batch := []any{
		map[string]any{"op": "create_company",
			"fields":        map[string]string{"name": "Northwind Traders"},
			"evidenceLinks": []any{link(s1s, s1e)}},
		map[string]any{"op": "create_opportunity",
			"fields": map[string]string{"companyId": "batch:0", "title": "Senior Backend Engineer",
				"kind": "employment", "sourceUrl": h.board.URL + "/roles/1", "originalText": body,
				"locationText": "Berlin", "requisitionId": "REQ-NW-101", "reqIssuer": "northwind",
				"vacancyComplete": "true"},
			"evidenceLinks":    []any{link(s1s, s1e), link(t1s, t1e)},
			"assessmentIds":    []string{assessID},
			"identityDecision": map[string]any{"decision": "new", "candidates": []any{}}},
	}
	call := func(key string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
		defer cancel()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "records_save",
			Arguments: map[string]any{
				"capability": capability, "runId": h.runID, "generation": float64(h.gen),
				"batch": batch, "idempotencyKey": key,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			raw, _ := json.Marshal(result.Content)
			t.Fatalf("records_save %s: %s", key, raw)
		}
		out, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("records_save %s unstructured: %+v", key, result.StructuredContent)
		}
		return out
	}
	first := call("noted-save-1")
	if first["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("records_save: %+v", first)
	}
	saved, ok := first["saved"].([]any)
	if !ok || len(saved) != 2 {
		t.Fatalf("records_save saved: %+v", first)
	}
	want := map[string]bool{}
	for _, item := range saved {
		rec, ok := item.(map[string]any)
		if !ok || rec["recordId"] == "" {
			t.Fatalf("saved record shape: %+v", item)
		}
		want[rec["recordId"].(string)] = true
	}
	checkpoint, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for id := range want {
		found := false
		for _, have := range checkpoint.SavedRecordIDs {
			if have == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("checkpoint saved ids %v missing %s", checkpoint.SavedRecordIDs, id)
		}
	}
	savedEvents := func() int {
		t.Helper()
		events, _, err := h.stack.Journal.List(h.ctx, h.runID, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range events {
			if e.Kind == "run.saved" {
				count++
			}
		}
		return count
	}
	if got := savedEvents(); got != 1 {
		t.Fatalf("run.saved events: %d, want 1", got)
	}
	again := call("noted-save-1")
	if again["outcome"] != string(researchcontract.OutcomeReused) {
		t.Fatalf("replay: %+v", again)
	}
	resaved, ok := again["saved"].([]any)
	if !ok || len(resaved) != 2 {
		t.Fatalf("replay saved: %+v", again)
	}
	for _, item := range resaved {
		if rec, ok := item.(map[string]any); !ok || !want[rec["recordId"].(string)] {
			t.Fatalf("replay id drift: %+v", item)
		}
	}
	if got := savedEvents(); got != 1 {
		t.Fatalf("run.saved events after replay: %d, want 1", got)
	}
}
