package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDiscoveryStageRollbackAndReplay(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "agent"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: "discovery-stage", Intent: "inspect", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{Resources: []string{"discovery:himalayas"}, Operations: []string{RoundCodexTurn, RoundSearchSource, RoundStageDiscovery}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 1, Items: 3, Tools: 5, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "turn", Operation: RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "source", Operation: RoundSearchSource, ResourceID: "discovery:himalayas", Cost: RoundAllowance{Requests: 1, Tools: 1}, BoundCapability: capability})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	quote := "🚀 **Architect**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/architect"
	response, _ := json.Marshal(map[string]any{"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": quote}}}})
	v := DiscoveryHTTP{AttemptID: source.ID, RoundID: r.ID, Method: "search_jobs", RequestJSON: `{"method":"tools/call"}`, Endpoint: "https://mcp.himalayas.app/mcp", ObservedAt: utcNow()}
	if err = db.PrepareDiscoveryHTTP(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.StatusCode = 200
	v.ResponseBody = response
	if err = db.CompleteDiscoveryHTTP(ctx, v); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRoundAttempt(ctx, agent, r.ID, source.ID, true, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	input := DiscoveryStageInput{RoundID: r.ID, Capability: capability, RequestKey: "candidate", Candidate: DiscoveryCandidate{AttemptID: source.ID, Kind: "job", Title: "Architect", URL: "https://himalayas.app/companies/example/jobs/architect", EvidenceQuote: quote}}
	if _, err = db.db.ExecContext(ctx, `CREATE TRIGGER force_discovery_failure BEFORE INSERT ON discovery_candidates BEGIN SELECT RAISE(FAIL,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.StageDiscoveryCandidate(ctx, input); err == nil {
		t.Fatal("wanted insert failure")
	}
	assertStageCounts(t, db, r.ID, 0, 0)
	if _, err = db.db.ExecContext(ctx, `DROP TRIGGER force_discovery_failure`); err != nil {
		t.Fatal(err)
	}
	first, err := db.StageDiscoveryCandidate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertStageCounts(t, db, r.ID, 1, 1)
	replay, err := db.StageDiscoveryCandidate(ctx, input)
	if err != nil || replay != first {
		t.Fatalf("replay %+v: %v", replay, err)
	}
	assertStageCounts(t, db, r.ID, 1, 1)
	input.RequestKey = "duplicate"
	if _, err = db.StageDiscoveryCandidate(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	assertStageCounts(t, db, r.ID, 1, 1)
}

func assertStageCounts(t *testing.T, db *Store, roundID string, items, attempts int) {
	t.Helper()
	var used, count, candidates int
	if err := db.db.QueryRow(`SELECT items_used FROM rounds WHERE id=?`, roundID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM round_attempts WHERE round_id=? AND operation=?`, roundID, RoundStageDiscovery).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM discovery_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if used != items || count != attempts || candidates != attempts {
		t.Fatalf("used=%d attempts=%d candidates=%d", used, count, candidates)
	}
}
