package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type packRuntimeFixture struct {
	db       *store.Store
	turns    int
	evidence string
}

func (f *packRuntimeFixture) CheckRound(context.Context, string) error { return nil }
func (f *packRuntimeFixture) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	f.turns++
	f.evidence = input.Evidence
	turn, _, err := f.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	turn, err = f.db.MarkRoundDispatched(ctx, roundID, turn.ID)
	if err != nil {
		return turn, err
	}
	if err := f.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "fixture-thread"); err != nil {
		return turn, err
	}
	if err := f.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-thread", "fixture-turn"); err != nil {
		return turn, err
	}
	if err := f.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-thread", "fixture-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	capability, err := f.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	opportunity, err := f.db.Opportunity(ctx, strings.TrimPrefix(input.ResourceID, "opportunity:"))
	if err != nil {
		return turn, err
	}
	profile, err := f.db.CurrentPreferences(ctx)
	if err != nil {
		return turn, err
	}
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"opportunityId": opportunity.ID, "opportunityRevision": opportunity.Revision, "profileRevision": profile.Version}, "draft": map[string]any{"focus": map[string]any{"text": "Cited focus"}, "materialUnknowns": []string{"Application route unverified"}}})
	source := []byte("= CV")
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	content, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, source, pdf})
	digest := sha256.Sum256(content)
	_, _, err = f.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "prepare-pack", Operation: store.RoundPrepareApplicationPack, ResourceID: input.ResourceID, ExpectedRevision: opportunity.Revision, Capability: capability,
		ApplicationPack: &store.ApplicationPackMutationInput{OpportunityID: opportunity.ID, ExpectedOpportunityRevision: opportunity.Revision, ExpectedProfileRevision: profile.Version, ContentSHA256: hex.EncodeToString(digest[:]), ManifestJSON: manifest, TypstSource: source, PDF: pdf}})
	if err != nil {
		return turn, err
	}
	ended, finishErr := f.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"prepared":true}`), "")
	return ended, finishErr
}

func prepareRoundFixture(t *testing.T, db *store.Store, selected bool) (store.Round, store.Opportunity) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "prepare-owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.invalid/opening", OriginalText: "Build Go services and Linux tooling. Application route and questions are absent.", Stage: "new", WorkPattern: "remote", LocationText: "Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	if selected {
		if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{RequestKey: "select", ExpectedOpportunityRevision: 1, ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "prepare", Intent: "Prepare selected role", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{resource}, Operations: []string{store.RoundCodexTurn, store.RoundJevRequest, store.RoundPrepareApplicationPack}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 7, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	return round, opportunity
}

func waitPackRound(t *testing.T, db *store.Store, id string) store.Round {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := db.Round(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == store.RoundCompleted {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	last, _ := db.Round(context.Background(), id)
	history, _ := db.RoundHistory(context.Background(), id)
	var states []string
	_ = db.Read(context.Background(), func(r store.Reader) error {
		rows, err := r.QueryContext(context.Background(), `SELECT state FROM round_attempts WHERE round_id=?`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var state string
			if err := rows.Scan(&state); err != nil {
				return err
			}
			states = append(states, state)
		}
		return rows.Err()
	})
	t.Fatalf("prepare round did not finish: state=%s stop=%s report=%s history=%+v attempts=%v", last.State, last.StopReason, last.Report, history, states)
	return store.Round{}
}

func TestPrepareCommissionSuppliesApprovedEvidenceAndReportsPack(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := filepath.Abs("../../../../../")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &packRuntimeFixture{db: db}
	engine := &Engine{Store: db, Runtime: runtime, PackSources: LocalPackSources{ProjectRoot: root}, Context: ctx}
	round, opportunity := prepareRoundFixture(t, db, true)
	if err := engine.launchPrepare(round); err != nil {
		t.Fatal(err)
	}
	finished := waitPackRound(t, db, round.ID)
	if finished.DeliverableStatus != "complete" || runtime.turns != 1 {
		t.Fatalf("round=%+v turns=%d", finished, runtime.turns)
	}
	var evidence packTurnEvidence
	if err := json.Unmarshal([]byte(runtime.evidence), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.OriginalText != opportunity.OriginalText || evidence.OpportunityRevision != opportunity.Revision || len(evidence.CareerSources) != 3 || len(runtime.evidence) > 30000 || evidence.Preferences.PreferredLocation == "" || len(evidence.Preferences.RoleCriteria) == 0 || evidence.Preferences.TargetHoursHundredths == 0 {
		t.Fatalf("evidence=%+v", evidence)
	}
	if evidence.CareerSources[1].ID != "cv-vince-liem.md" || !strings.Contains(evidence.CareerSources[1].Excerpt, "Personal engineering projects") || evidence.CareerSources[0].OmittedBytes == 0 {
		t.Fatalf("career excerpts=%+v", evidence.CareerSources)
	}
	var report packReport
	if err := json.Unmarshal(finished.Report, &report); err != nil {
		t.Fatal(err)
	}
	if report.Code != "pack_ready" || report.PackID == "" || report.Version != 1 || len(report.MaterialUnknowns) != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrepareCommissionStopsOnMissingSelectionAndOversizedRole(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := filepath.Abs("../../../../../")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &packRuntimeFixture{db: db}
	engine := &Engine{Store: db, Runtime: runtime, PackSources: LocalPackSources{ProjectRoot: root}, Context: ctx}
	round, _ := prepareRoundFixture(t, db, false)
	if err := engine.launchPrepare(round); err != nil {
		t.Fatal(err)
	}
	finished := waitPackRound(t, db, round.ID)
	if runtime.turns != 0 || finished.DeliverableStatus != "partial" {
		t.Fatalf("missing selection dispatched: %+v turns=%d", finished, runtime.turns)
	}
	var detail packReport
	if err := json.Unmarshal(finished.Report, &detail); err != nil || detail.Code != "selected_role_decision_changed" {
		t.Fatalf("report=%+v err=%v", detail, err)
	}
	sources, err := (LocalPackSources{ProjectRoot: root}).LoadPackSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildPackTurnEvidence(store.Opportunity{ID: "large", Revision: 1, Title: "Role", SourceURL: "https://example.invalid", OriginalText: strings.Repeat("x", 30000)}, store.Company{Name: "Employer"}, store.Preferences{Version: 1}, sources)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("oversized full role silently truncated: %v", err)
	}
}

var _ PackSourceLoader = LocalPackSources{}
