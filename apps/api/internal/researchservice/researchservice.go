// Package researchservice implements the T10 research HTTP contract over
// the T13 run supervisor (lane B, T17). It lives in its own package
// because httpapi already imports codexservice and rounds: implementing
// httpapi.ResearchService from either side would import-cycle.
//
// The adapter maps supervisor outputs (round, checkpoint, ledger, journal)
// to the redacted T10 DTOs, resolves the commission-time rubric version
// from the current brief store (recording its source on the commission
// record), and drives the commissioned agent loop. Reads are
// side-effect-free; DTOs carry no credentials and no raw protocol payloads.
package researchservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// DiscoveryCommissioner admits one bounded Contributor discovery run.
// *musewire.Service satisfies it; tests use doubles.
type DiscoveryCommissioner interface {
	DiscoveryCeiling() musecode.Bounds
	CommissionDiscoveryAsync(ctx context.Context, runRef string, criteria musecode.PublicCriteria, profileVersion int64, rubricVersion string, bounds musecode.Bounds) (musewire.AsyncAdmission, error)
}

// Deps wires one research service. The journal must be the same sink
// instance the supervisor journals to; captures serves capture views;
// AgentID is the delegated agent commissioned runs run as. Muse is the
// single discovery commissioner; nil keeps reads working while
// commissions report unavailable.
type Deps struct {
	DB         *store.Store
	Supervisor *rounds.Supervisor
	Journal    researchcontract.EventSink
	Captures   researchcontract.CaptureReader
	AgentID    string
	Muse       DiscoveryCommissioner
	// NewKey synthesizes idempotency keys when the caller omits them.
	// Nil selects crypto/rand hex keys; tests inject deterministic keys.
	NewKey func(prefix string) string
}

// Service implements httpapi.ResearchService over a run supervisor.
type Service struct {
	db       *store.Store
	sup      *rounds.Supervisor
	journal  researchcontract.EventSink
	caps     researchcontract.CaptureReader
	agent    string
	muse     DiscoveryCommissioner
	newKey   func(prefix string) string
	roundNow func() time.Time
}

var _ httpapi.ResearchService = (*Service)(nil)

// New builds the T10 research adapter.
func New(deps Deps) (*Service, error) {
	if deps.DB == nil || deps.Supervisor == nil || deps.Journal == nil || deps.Captures == nil {
		return nil, errors.New("researchservice: store, supervisor, journal and capture reader required")
	}
	if deps.AgentID == "" || len(deps.AgentID) > 128 || strings.TrimSpace(deps.AgentID) != deps.AgentID {
		return nil, errors.New("researchservice: delegated agent id required")
	}
	newKey := deps.NewKey
	if newKey == nil {
		newKey = defaultKey
	}
	return &Service{db: deps.DB, sup: deps.Supervisor, journal: deps.Journal,
		caps: deps.Captures, agent: deps.AgentID, muse: deps.Muse, newKey: newKey}, nil
}

// museAgentID names the delegated Contributor session commissioned
// discovery runs run as.
const museAgentID = "muse-contributor"

const (
	maxCommissionBriefText   = 20000
	maxCommissionCorrections = 512
)

// commissionJournal mirrors the durable rounds commission record so run
// views, checkpoint recovery and audits read muse commissions
// identically. AllowanceInput has no JSON tags, so its keys are the Go
// field names.
type commissionJournal struct {
	Brief         string                `json:"brief"`
	RubricVersion string                `json:"rubricVersion"`
	RubricSource  string                `json:"rubricSource,omitempty"`
	AgentID       string                `json:"agentId"`
	Allowance     rounds.AllowanceInput `json:"allowance"`
	MaxConcurrent int                   `json:"maxConcurrent"`
	Corrections   string                `json:"correctionsRef,omitempty"`
}

// discoveryCriteria renders the generalized public search criteria for one
// Contributor run: role-criteria labels, the run's brief words, the general
// region and remote/hybrid markers. Entries cap at 20 keywords of 200
// chars so the prompt stays bounded; longer briefs chunk on word
// boundaries. Verified owner facts stay server-side for Jev matching.
func discoveryCriteria(brief codexservice.OwnerBrief, briefText string) musecode.PublicCriteria {
	var out musecode.PublicCriteria
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 200 || len(out.RoleKeywords) >= 20 || seen[value] {
			return
		}
		seen[value] = true
		out.RoleKeywords = append(out.RoleKeywords, value)
	}
	for _, p := range brief.Preferences {
		add(p.Value)
	}
	for _, chunk := range chunkBrief(briefText) {
		add(chunk)
	}
	for _, f := range brief.Facts {
		switch f.Key {
		case "preferredLocation":
			if value := strings.TrimSpace(f.Value); value != "" && len(value) <= 200 {
				out.RegionText = value
			}
		case "allowRemote":
			if f.Value == "true" {
				add("remote work acceptable")
			}
		case "allowHybrid":
			if f.Value == "true" {
				add("hybrid work acceptable")
			}
		}
	}
	return out
}

// chunkBrief splits run brief words into keywords of at most 200 chars on
// word boundaries. A single overlong word is dropped, never mangled.
func chunkBrief(briefText string) []string {
	var chunks []string
	var current strings.Builder
	for _, word := range strings.Fields(briefText) {
		if len(word) > 200 {
			continue
		}
		if current.Len() > 0 && current.Len()+1+len(word) > 200 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(word)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

func defaultKey(prefix string) string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return prefix + "-" + hex.EncodeToString(raw[:])
}

// CommissionResearch commissions one research run: resolve the current
// brief (profile + rubric + source), admit one bounded Contributor
// discovery run through the single commissioner, journal the
// deterministic commission record, and project the run view. Same-key
// replays return the existing run; the journal append is idempotent, so
// a replay also heals a missing commission record.
func (s *Service) CommissionResearch(ctx context.Context, in httpapi.CommissionResearchInput) (httpapi.CommissionResearchOutput, error) {
	if s.muse == nil {
		return httpapi.CommissionResearchOutput{}, rounds.ErrNotReady
	}
	brief, err := codexservice.CurrentOwnerBrief(ctx, s.db)
	if err != nil {
		return httpapi.CommissionResearchOutput{}, err
	}
	if len(in.BriefText) > maxCommissionBriefText {
		return httpapi.CommissionResearchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "briefText",
			"brief exceeds 20000 chars")
	}
	if len(in.CorrectionsRef) > maxCommissionCorrections {
		return httpapi.CommissionResearchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "correctionsRef",
			"corrections reference exceeds 512 chars")
	}
	key := in.IdempotencyKey
	if key == "" {
		key = s.newKey("run")
	}
	allow := rounds.AllowanceInput{TimeMs: int64(rounds.DefaultRunMinutes) * 60 * 1000,
		MaxActions: int64(rounds.DefaultMaxActions), MaxJev: int64(rounds.DefaultMaxJev),
		MaxTurns: int64(rounds.DefaultMaxTurns), MaxConcurrent: int64(rounds.DefaultMaxConcurrent)}
	if in.Allowance != nil {
		allow = rounds.AllowanceInput{TimeMs: int64(in.Allowance.TimeMs),
			MaxActions: int64(in.Allowance.MaxActions), MaxJev: int64(in.Allowance.MaxJev),
			MaxTurns: int64(in.Allowance.MaxTurns), MaxConcurrent: int64(in.Allowance.MaxConcurrent)}
	}
	if allow.TimeMs < 1 || allow.MaxActions < 1 || allow.MaxJev < 0 || allow.MaxTurns < 1 ||
		allow.MaxConcurrent < 1 || allow.MaxConcurrent > 4 {
		return httpapi.CommissionResearchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "allowance",
			"allowance needs timeMs>=1, maxActions>=1, maxJev>=0, maxTurns>=1, maxConcurrent 1..4")
	}
	bounds := s.muse.DiscoveryCeiling()
	if wall := time.Duration(allow.TimeMs) * time.Millisecond; wall < bounds.MaxWallClock {
		bounds.MaxWallClock = wall
	}
	if steps := int(allow.MaxTurns); steps < bounds.MaxModelSteps {
		bounds.MaxModelSteps = steps
	}
	if calls := int(allow.MaxActions); calls < bounds.MaxToolCalls {
		bounds.MaxToolCalls = calls
	}
	admitted, err := s.muse.CommissionDiscoveryAsync(ctx, key,
		discoveryCriteria(brief, in.BriefText), brief.ProfileVersion, brief.RubricVersion, bounds)
	if err != nil {
		return httpapi.CommissionResearchOutput{}, err
	}
	raw, err := json.Marshal(commissionJournal{Brief: in.BriefText,
		RubricVersion: brief.RubricVersion, RubricSource: brief.Source, AgentID: museAgentID,
		Allowance: allow, MaxConcurrent: int(allow.MaxConcurrent), Corrections: in.CorrectionsRef})
	if err != nil {
		return httpapi.CommissionResearchOutput{}, err
	}
	now := time.Now()
	if err := s.journal.Append(ctx, researchcontract.Event{
		ID: "run." + admitted.RoundID + ".commissioned", RunID: admitted.RoundID,
		Kind: rounds.SuperviseEventCommissioned, Outcome: researchcontract.OutcomeOK,
		Payload: raw, ObservedAt: now, RecordedAt: now,
	}); err != nil {
		return httpapi.CommissionResearchOutput{}, errors.Join(
			researchcontract.NewError(researchcontract.OutcomeUncertain, "journal",
				"run commissioned but the commission record was not journaled; retry the same idempotency key to heal"),
			err)
	}
	view, err := s.runView(ctx, admitted.RoundID)
	if err != nil {
		return httpapi.CommissionResearchOutput{}, err
	}
	return httpapi.CommissionResearchOutput{View: view, Created: admitted.Created}, nil
}

// SteerResearch records one owner steering message and reports its
// acknowledgment. The presented generation resolves from the live run;
// the T10 DTO carries no correction flag, so corrections are never sent
// from this path. Muse discovery runs conduct one bounded session and
// are not steerable: the owner commissions a new run instead.
func (s *Service) SteerResearch(ctx context.Context, in httpapi.SteerResearchInput) (generated.SteeringMessage, error) {
	round, err := s.db.Round(ctx, in.RunID)
	if err != nil {
		return generated.SteeringMessage{}, mapRoundError(err)
	}
	if in.Actor.Kind != "administrator" || in.Actor.ID == "" {
		return generated.SteeringMessage{}, researchcontract.NewError(researchcontract.OutcomeForbidden,
			string(researchcontract.AuthorityPermission), "run control requires the owner administrator")
	}
	if round.Intent == musewire.CommissionIntent {
		return generated.SteeringMessage{}, researchcontract.NewError(researchcontract.OutcomeConflict,
			"run", "muse discovery runs are not steerable; commission a new run")
	}
	key := in.IdempotencyKey
	if key == "" {
		key = s.newKey("steer")
	}
	out, err := s.sup.Steer(ctx, rounds.SteerInput{Actor: in.Actor, RunID: in.RunID,
		Generation: round.Generation, Body: in.Body, IdempotencyKey: key})
	if err != nil {
		return generated.SteeringMessage{}, err
	}
	return generated.SteeringMessage{
		MessageId: "steer." + in.RunID + "." + key,
		Revision:  int(out.Revision),
		Body:      in.Body,
		Ack:       steerAck(out),
	}, nil
}

func steerAck(out rounds.SteerOutput) generated.SteeringMessageAck {
	switch out.State {
	case rounds.SteerApplied:
		return generated.SteeringMessageAckApplied
	case rounds.SteerDuplicate:
		if out.Applied {
			return generated.SteeringMessageAckApplied
		}
		return generated.SteeringMessageAckAcknowledged
	case rounds.SteerQueued, rounds.SteerPaused:
		return generated.SteeringMessageAckPending
	default:
		// Every steering path journals durably, so an unknown state is
		// still an acknowledged message, never a silent drop.
		return generated.SteeringMessageAckAcknowledged
	}
}

// ResearchRun projects one run: round state, binding brief versions,
// commissioned allowance, ledger usage, investigations, saves and the
// unresolved count. Pure read: missing state is not_found, never rebuilt.
func (s *Service) ResearchRun(ctx context.Context, actor store.Actor, runID string) (generated.ResearchRunView, error) {
	if actor.Kind == "" || actor.ID == "" {
		return generated.ResearchRunView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading a run requires an authenticated actor")
	}
	return s.runView(ctx, runID)
}

// ResearchActivity pages the run journal as redacted summaries. The cursor
// is the last seen event id; per-event cursors carry the same resume token.
func (s *Service) ResearchActivity(ctx context.Context, actor store.Actor, runID, cursor string, limit int) (generated.ResearchActivityPage, error) {
	if actor.Kind == "" || actor.ID == "" {
		return generated.ResearchActivityPage{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading run activity requires an authenticated actor")
	}
	if _, err := s.db.Round(ctx, runID); err != nil {
		return generated.ResearchActivityPage{}, mapRoundError(err)
	}
	if limit <= 0 {
		limit = 25
	}
	events, next, err := s.journal.List(ctx, runID, cursor, limit)
	if err != nil {
		return generated.ResearchActivityPage{}, mapJournalError(err)
	}
	page := generated.ResearchActivityPage{Events: []generated.ResearchActivityEvent{}}
	for _, e := range events {
		event := generated.ResearchActivityEvent{
			EventId: e.ID, Kind: e.Kind, Summary: summarizeEvent(e),
		}
		event.Cursor = ptrOf(e.ID)
		event.At = e.ObservedAt
		if event.At.IsZero() {
			event.At = e.RecordedAt
		}
		if refs := eventRefs(e); refs != nil {
			event.Refs = refs
		}
		page.Events = append(page.Events, event)
	}
	if next != "" {
		page.NextCursor = ptrOf(next)
	}
	return page, nil
}

// ResearchCapture projects one immutable capture. Excerpt indexes and
// truncation notes are not visible behind the capture-reader interface,
// so they stay omitted rather than invented.
func (s *Service) ResearchCapture(ctx context.Context, actor store.Actor, captureID string) (generated.ResearchCaptureView, error) {
	if actor.Kind == "" || actor.ID == "" {
		return generated.ResearchCaptureView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading a capture requires an authenticated actor")
	}
	if strings.TrimSpace(captureID) == "" || len(captureID) > 128 {
		return generated.ResearchCaptureView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"captureId", "capture id required (1..128 chars)")
	}
	capture, rc, err := s.caps.OpenCapture(ctx, captureID)
	if err != nil {
		return generated.ResearchCaptureView{}, err
	}
	_ = rc.Close()
	view := generated.ResearchCaptureView{
		CaptureId: capture.ID, ContentHash: capture.SHA256,
		MediaType: capture.ContentType, ObservedUrl: capture.Request.URLOrQuery,
		RetrievedAt: capture.ObservedAt, Status: capture.Status,
	}
	view.Extent.Bytes = int(capture.Bytes)
	view.Extent.Complete = capture.Complete
	if capture.FinalURL != "" {
		view.FinalUrl = ptrOf(capture.FinalURL)
	}
	return view, nil
}

func ptrOf[T any](value T) *T { return &value }

func mapRoundError(err error) error {
	if err == nil {
		return nil
	}
	var cerr *researchcontract.Error
	if errors.As(err, &cerr) {
		return err
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return researchcontract.NewError(researchcontract.OutcomeNotFound, "run", "unknown run")
	case errors.Is(err, store.ErrInvalid):
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "invalid run request")
	}
	return err
}

func mapJournalError(err error) error {
	if err == nil {
		return nil
	}
	var cerr *researchcontract.Error
	if errors.As(err, &cerr) {
		return err
	}
	if errors.Is(err, store.ErrInvalid) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "cursor",
			"unknown activity cursor; re-page from the head")
	}
	return err
}

// commissionCopy mirrors the durable commission record (rounds keeps the
// authoritative struct private). AllowanceInput has no JSON tags, so its
// keys are the Go field names.
type commissionCopy struct {
	RubricVersion string `json:"rubricVersion"`
	RubricSource  string `json:"rubricSource"`
	AgentID       string `json:"agentId"`
	Allowance     struct {
		TimeMs        int64 `json:"TimeMs"`
		MaxActions    int64 `json:"MaxActions"`
		MaxJev        int64 `json:"MaxJev"`
		MaxTurns      int64 `json:"MaxTurns"`
		MaxConcurrent int64 `json:"MaxConcurrent"`
	} `json:"allowance"`
	MaxConcurrent int `json:"maxConcurrent"`
}

// commissionOf reads the deterministic commission record behind a run.
// Its absence means a partial commission: the owner heals it by
// recommissioning the same idempotency key.
func (s *Service) commissionOf(ctx context.Context, runID string) (commissionCopy, error) {
	var rec commissionCopy
	want := "run." + runID + ".commissioned"
	cursor := ""
	for page := 0; page < 100; page++ {
		events, next, err := s.journal.List(ctx, runID, cursor, 100)
		if err != nil {
			return rec, mapJournalError(err)
		}
		for _, e := range events {
			if e.ID != want {
				continue
			}
			if err := json.Unmarshal(e.Payload, &rec); err != nil {
				return rec, err
			}
			return rec, nil
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return rec, researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
		"run "+runID+" has no commission record; recommission with the same idempotency key to heal")
}

// journalScan folds one run's journal through fn in record order. It stops
// after 10k events and reports truncation so callers can latch unknown
// instead of silently undercounting.
func (s *Service) journalScan(ctx context.Context, runID string, fn func(researchcontract.Event)) (truncated bool, err error) {
	cursor := ""
	for page := 0; page < 100; page++ {
		events, next, err := s.journal.List(ctx, runID, cursor, 100)
		if err != nil {
			return false, mapJournalError(err)
		}
		for _, e := range events {
			fn(e)
		}
		if next == "" {
			return false, nil
		}
		cursor = next
	}
	return true, nil
}
