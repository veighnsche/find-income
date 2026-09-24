// Research view projections: run, identity and report DTOs plus the
// redacted activity summarizer. Every projection derives from durable
// state (round, checkpoint, ledger, journal, records); nothing is
// invented, and anything unverifiable stays omitted or unknown.
package researchservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// runView builds the run DTO from round + checkpoint + ledger + journal +
// notes. Missing durable state is an error, never a silent rebuild.
func (s *Service) runView(ctx context.Context, runID string) (generated.ResearchRunView, error) {
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return generated.ResearchRunView{}, mapRoundError(err)
	}
	cp, err := s.sup.Checkpoint(ctx, runID)
	if err != nil {
		return generated.ResearchRunView{}, err
	}
	ledger, err := s.sup.Usage(ctx, runID)
	if err != nil {
		return generated.ResearchRunView{}, err
	}
	rec, err := s.commissionOf(ctx, runID)
	if err != nil {
		return generated.ResearchRunView{}, err
	}
	allow := generated.ResearchAllowance{TimeMs: int(rec.Allowance.TimeMs),
		MaxActions: int(rec.Allowance.MaxActions), MaxJev: int(rec.Allowance.MaxJev),
		MaxTurns: int(rec.Allowance.MaxTurns), MaxConcurrent: int(rec.Allowance.MaxConcurrent)}
	var bytes int64
	truncated, err := s.journalScan(ctx, runID, func(e researchcontract.Event) {
		if e.Kind != "run.observed" {
			return
		}
		var payload struct {
			BytesIn  int64 `json:"bytesIn"`
			BytesOut int64 `json:"bytesOut"`
		}
		if json.Unmarshal(e.Payload, &payload) == nil {
			bytes += payload.BytesIn + payload.BytesOut
		}
	})
	if err != nil {
		return generated.ResearchRunView{}, err
	}
	notes, err := s.researchNotes(ctx, runID)
	if err != nil {
		return generated.ResearchRunView{}, err
	}
	view := generated.ResearchRunView{
		RunId: round.ID, State: generated.ResearchRunViewState(string(round.State)),
		Allowance: allow, Usage: mapUsage(allow, ledger, bytes, truncated),
		Investigations: notes, SavedIds: append([]string{}, cp.SavedRecordIDs...),
		UnresolvedCount: len(cp.UnresolvedAttempts),
	}
	if view.SavedIds == nil {
		view.SavedIds = []string{}
	}
	view.BriefVersion.ProfileVersion = int(cp.ProfileVersion)
	view.BriefVersion.RubricVersion = cp.RubricVersion
	if round.StopReason != "" {
		view.StopReason = ptrOf(round.StopReason)
	}
	// No persisted report artifact exists yet: the report computes live
	// from the same durable state, so no ref is advertised.
	return view, nil
}

// mapUsage projects the ledger into DTO usage. The commission derivation
// (requests=actions+jev, items=actions, tools=actions+turns, turns=turns)
// inverts to actions=items, turns=turns, jev=requests-items; observed byte
// volume accumulates from the journaled observations.
func mapUsage(enforced generated.ResearchAllowance, ledger researchcontract.UsageLedger, bytes int64, scanTruncated bool) generated.ResearchUsage {
	out := generated.ResearchUsage{Enforced: enforced, Unknown: ledger.Unknown || scanTruncated}
	out.Reserved.Actions = int(ledger.Reserved.Items)
	out.Reserved.Jev = int(max64(ledger.Reserved.Requests-ledger.Reserved.Items, 0))
	out.Reserved.Turns = int(ledger.Reserved.Turns)
	out.Observed.Actions = int(ledger.Observed.Items)
	out.Observed.Jev = int(max64(ledger.Observed.Requests-ledger.Observed.Items, 0))
	out.Observed.Turns = int(ledger.Observed.Turns)
	out.Observed.Bytes = int(bytes)
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s *Service) researchNotes(ctx context.Context, runID string) ([]generated.ResearchInvestigation, error) {
	var notes []store.ResearchNote
	if err := s.db.Read(ctx, func(r store.Reader) error {
		var err error
		notes, err = store.ListResearchNotesByRound(ctx, r, runID)
		return err
	}); err != nil {
		return nil, err
	}
	out := []generated.ResearchInvestigation{}
	for _, n := range notes {
		status := generated.ResearchInvestigationStatusOpen
		switch {
		case strings.TrimSpace(n.Conclusion) != "":
			status = generated.ResearchInvestigationStatusConcluded
		case strings.TrimSpace(n.OutstandingJSON) != "":
			status = generated.ResearchInvestigationStatusActive
		}
		out = append(out, generated.ResearchInvestigation{Id: n.ID, Intent: n.Intent, Status: status})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// activity summaries
// ---------------------------------------------------------------------------

// payloadOf extracts allowlisted scalar fields from an event payload. Only
// the named keys are ever read: brief text, steering bodies, prompts and
// credentials never reach a summary.
func payloadOf(raw json.RawMessage, keys ...string) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return out
	}
	for _, key := range keys {
		switch value := decoded[key].(type) {
		case string:
			out[key] = value
		case float64:
			out[key] = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.0f", value), "0"), ".")
			if out[key] == "" {
				out[key] = "0"
			}
		case bool:
			out[key] = fmt.Sprintf("%v", value)
		}
	}
	return out
}

func shortID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// summarizeEvent renders one journal event as a redacted one-liner. Unknown
// kinds degrade to a generic line; raw payloads are never dumped.
func summarizeEvent(e researchcontract.Event) string {
	outcome := string(e.Outcome)
	switch e.Kind {
	case researchcontract.EventClaim:
		return "Claimed exact request " + shortID(e.RequestFingerprint, 12)
	case researchcontract.EventReuse:
		return "Reused prior result for " + shortID(e.RequestFingerprint, 12)
	case researchcontract.EventCapture:
		p := payloadOf(e.Payload, "bytes", "complete")
		return fmt.Sprintf("Captured %s bytes (complete: %s)", p["bytes"], p["complete"])
	case researchcontract.EventObservation:
		ref := e.ObservationID
		if ref == "" {
			ref = shortID(e.RequestFingerprint, 12)
		}
		return "Observed " + ref + " (" + outcome + ")"
	case researchcontract.EventNote:
		return "Recorded investigation note"
	case researchcontract.EventRefresh:
		p := payloadOf(e.Payload, "reason")
		return "Refreshed " + shortID(e.RequestFingerprint, 12) + " (" + p["reason"] + ")"
	case researchcontract.EventLeaseExpired:
		return "Lease expired for " + shortID(e.RequestFingerprint, 12)
	case researchcontract.EventLeaseTakeover:
		return "Lease taken over for " + shortID(e.RequestFingerprint, 12)
	case researchcontract.EventLateObservation:
		return "Late bytes retained as inert observation"
	case researchcontract.EventExhausted:
		return "Freshness budget exhausted for " + shortID(e.RequestFingerprint, 12)
	case "run.commissioned":
		p := payloadOf(e.Payload, "agentId", "maxConcurrent")
		return "Run commissioned for agent " + p["agentId"] + " (max " + p["maxConcurrent"] + " concurrent)"
	case "run.dispatched":
		p := payloadOf(e.Payload, "operation", "requestKey", "resumedThread")
		line := "Dispatched " + p["operation"] + " (" + p["requestKey"] + ")"
		if p["resumedThread"] == "true" {
			line += " on the stored thread"
		}
		return line
	case "run.observed":
		p := payloadOf(e.Payload, "requests", "bytesIn", "bytesOut", "receiptId", "truncated")
		line := "Execution settled: " + outcome + " (" + p["requests"] + " requests)"
		if p["truncated"] == "true" {
			line += ", truncated"
		}
		if p["receiptId"] != "" {
			line += ", receipt " + shortID(p["receiptId"], 12)
		}
		return line
	case "run.turn_observed":
		p := payloadOf(e.Payload, "status", "via")
		return "Turn " + p["status"] + " via " + p["via"]
	case "run.checkpointed":
		p := payloadOf(e.Payload, "generation")
		return "Checkpoint saved (generation " + p["generation"] + ")"
	case "run.saved":
		p := payloadOf(e.Payload, "count")
		return "Saved " + p["count"] + " records to the run checkpoint"
	case "run.stopped":
		p := payloadOf(e.Payload, "reason", "generation", "released")
		line := "Run stopped (generation " + p["generation"] + ", " + p["released"] + " holds released)"
		if p["reason"] != "" {
			line += ": " + p["reason"]
		}
		return line
	case "run.steer_received":
		p := payloadOf(e.Payload, "revision", "correction")
		line := "Steering message received (revision " + p["revision"] + ")"
		if p["correction"] == "true" {
			line += " as an owner correction"
		}
		return line
	case "run.steered":
		p := payloadOf(e.Payload, "revision", "applied", "reason")
		line := "Steering revision " + p["revision"] + " " + p["applied"]
		if p["reason"] != "" {
			line += " (" + p["reason"] + ")"
		}
		return line
	case "run.steer_rejected":
		p := payloadOf(e.Payload, "code", "field")
		return "Steering rejected (" + p["code"] + ": " + p["field"] + ")"
	case "run.correction_applied":
		p := payloadOf(e.Payload, "revision", "generation")
		return "Owner correction applied (revision " + p["revision"] + ", generation " + p["generation"] + ")"
	case "run.recovered":
		return "Turn recovered from the checkpoint on a fresh conversation"
	case "run.dispatch_fenced":
		p := payloadOf(e.Payload, "requestKey", "generation")
		return "Dispatch fenced by stop (" + p["requestKey"] + ", generation " + p["generation"] + ")"
	case "run.interrupted":
		p := payloadOf(e.Payload, "status")
		return "Turn interrupted (" + p["status"] + ")"
	case "run.resumed":
		return "Stored conversation resumed"
	case "run.uncertain":
		p := payloadOf(e.Payload, "reason")
		return "Outcome uncertain (" + p["reason"] + "); reconcile before retrying"
	case "run.unknown_events":
		p := payloadOf(e.Payload, "count")
		if p["count"] == "" {
			return "Unrecognized protocol notifications observed"
		}
		return p["count"] + " unrecognized protocol notifications observed"
	case "run.item_started", "run.item_completed":
		return "Model tool activity observed (" + outcome + ")"
	case "run.item_unmatched":
		return "Unmatched model tool activity (" + outcome + ")"
	default:
		return "Event " + e.Kind + " (" + outcome + ")"
	}
}

// eventRefs lifts saved references from the envelope plus allowlisted
// payload keys. Unknown shapes yield no refs, never an error.
func eventRefs(e researchcontract.Event) *struct {
	AssessmentId  *string `json:"assessmentId,omitempty"`
	CaptureId     *string `json:"captureId,omitempty"`
	ObservationId *string `json:"observationId,omitempty"`
	RecordId      *string `json:"recordId,omitempty"`
} {
	p := payloadOf(e.Payload, "assessmentId", "recordId", "observationId", "captureId")
	observation := e.ObservationID
	if observation == "" {
		observation = p["observationId"]
	}
	capture := e.CaptureID
	if capture == "" {
		capture = p["captureId"]
	}
	if p["assessmentId"] == "" && capture == "" && observation == "" && p["recordId"] == "" {
		return nil
	}
	refs := &struct {
		AssessmentId  *string `json:"assessmentId,omitempty"`
		CaptureId     *string `json:"captureId,omitempty"`
		ObservationId *string `json:"observationId,omitempty"`
		RecordId      *string `json:"recordId,omitempty"`
	}{}
	if p["assessmentId"] != "" {
		refs.AssessmentId = ptrOf(p["assessmentId"])
	}
	if capture != "" {
		refs.CaptureId = ptrOf(capture)
	}
	if observation != "" {
		refs.ObservationId = ptrOf(observation)
	}
	if p["recordId"] != "" {
		refs.RecordId = ptrOf(p["recordId"])
	}
	return refs
}

// ---------------------------------------------------------------------------
// identity explanations
// ---------------------------------------------------------------------------

// ResearchIdentity explains one saved subject's identity: the newest
// recorded decision for it plus its capture sightings. Subjects without a
// recorded decision report unresolved with an explicit basis.
func (s *Service) ResearchIdentity(ctx context.Context, actor store.Actor, subjectKind, subjectID string) (generated.ResearchIdentityView, error) {
	if actor.Kind == "" || actor.ID == "" {
		return generated.ResearchIdentityView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading identity requires an authenticated actor")
	}
	var kind string
	switch subjectKind {
	case "employer":
		kind = "company"
	case "vacancy":
		kind = "opportunity"
	default:
		return generated.ResearchIdentityView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"subjectKind", "subject kind must be employer|vacancy")
	}
	if strings.TrimSpace(subjectID) == "" || len(subjectID) > 128 {
		return generated.ResearchIdentityView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"subjectId", "subject id required (1..128 chars)")
	}
	if err := s.requireSubject(ctx, kind, subjectID); err != nil {
		return generated.ResearchIdentityView{}, err
	}
	decision, err := s.newestDecision(ctx, kind, subjectID)
	if err != nil {
		return generated.ResearchIdentityView{}, err
	}
	sightings, err := s.subjectSightings(ctx, kind, subjectID)
	if err != nil {
		return generated.ResearchIdentityView{}, err
	}
	view := generated.ResearchIdentityView{SubjectId: subjectID,
		Candidates: []generated.ResearchIdentityCandidate{}}
	if decision == nil {
		view.Decision = generated.ResearchIdentityViewDecisionUnresolved
		view.Basis = "no recorded identity decision for this subject"
	} else {
		switch decision.Decision {
		case "same":
			view.Decision = generated.ResearchIdentityViewDecisionSame
		case "new":
			view.Decision = generated.ResearchIdentityViewDecisionNew
		default:
			view.Decision = generated.ResearchIdentityViewDecisionUnresolved
		}
		view.Basis = decision.DecisionBasis
		if view.Basis == "" {
			view.Basis = "recorded " + decision.Decision + " decision " + decision.ID
		}
		for _, c := range decision.Candidates {
			view.Candidates = append(view.Candidates, generated.ResearchIdentityCandidate{
				RecordId: c.CandidateID, Revision: int(c.Revision)})
		}
		if decision.JevAssessmentID != "" {
			view.AssessmentId = ptrOf(decision.JevAssessmentID)
		}
	}
	if len(sightings) > 0 {
		view.Sightings = &sightings
	}
	return view, nil
}

func (s *Service) requireSubject(ctx context.Context, kind, id string) error {
	var err error
	if kind == "company" {
		_, err = s.db.Company(ctx, id)
	} else {
		_, err = s.db.Opportunity(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return researchcontract.NewError(researchcontract.OutcomeNotFound,
			"subject", "unknown "+kind+" "+id)
	}
	return err
}

// newestDecision scans the bounded newest-first decision lists for the
// latest decision naming this subject. Readers filter by (kind, decision),
// so the adapter filters by subject id on top. Decision rows use the
// employer|vacancy subject vocabulary; subject ids live on the
// company/opportunity columns.
func (s *Service) newestDecision(ctx context.Context, kind, subjectID string) (*store.IdentityDecisionRecord, error) {
	subjectKind := "vacancy"
	if kind == "company" {
		subjectKind = "employer"
	}
	var found *store.IdentityDecisionRecord
	if err := s.db.Read(ctx, func(r store.Reader) error {
		for _, decision := range []string{"same", "new", "unresolved"} {
			rows, err := store.ListIdentityDecisionsBySubject(ctx, r, subjectKind, decision, 50)
			if err != nil {
				return err
			}
			for _, row := range rows {
				match := row.SubjectCompanyID == subjectID && kind == "company" ||
					row.SubjectOpportunityID == subjectID && kind == "opportunity"
				if !match {
					continue
				}
				if found == nil || row.CreatedAt > found.CreatedAt ||
					row.CreatedAt == found.CreatedAt && row.ID > found.ID {
					dup := row
					found = &dup
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return found, nil
}

func (s *Service) subjectSightings(ctx context.Context, kind, subjectID string) ([]struct {
	CaptureId   string    `json:"captureId"`
	ObservedAt  time.Time `json:"observedAt"`
	ObservedUrl string    `json:"observedUrl"`
}, error) {
	var rows []store.RecordSighting
	if err := s.db.Read(ctx, func(r store.Reader) error {
		var err error
		if kind == "company" {
			rows, err = store.ListRecordSightingsByCompany(ctx, r, subjectID)
		} else {
			rows, err = store.ListRecordSightingsByOpportunity(ctx, r, subjectID)
		}
		return err
	}); err != nil {
		return nil, err
	}
	out := []struct {
		CaptureId   string    `json:"captureId"`
		ObservedAt  time.Time `json:"observedAt"`
		ObservedUrl string    `json:"observedUrl"`
	}{}
	for _, row := range rows {
		at, err := parseStoreTime(row.ObservedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, struct {
			CaptureId   string    `json:"captureId"`
			ObservedAt  time.Time `json:"observedAt"`
			ObservedUrl string    `json:"observedUrl"`
		}{CaptureId: row.CaptureID, ObservedAt: at, ObservedUrl: row.ObservedURL})
	}
	return out, nil
}

func parseStoreTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable store timestamp %q", value)
}

// ---------------------------------------------------------------------------
// reports
// ---------------------------------------------------------------------------

// ResearchReport aggregates the run's actual outcomes from durable state:
// saved records, dispatched operations, reused results, unresolved work,
// the ledger and the checkpoint's next work. Pure read.
func (s *Service) ResearchReport(ctx context.Context, actor store.Actor, runID string) (generated.ResearchReportView, error) {
	if actor.Kind == "" || actor.ID == "" {
		return generated.ResearchReportView{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading a report requires an authenticated actor")
	}
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return generated.ResearchReportView{}, mapRoundError(err)
	}
	cp, err := s.sup.Checkpoint(ctx, runID)
	if err != nil {
		return generated.ResearchReportView{}, err
	}
	ledger, err := s.sup.Usage(ctx, runID)
	if err != nil {
		return generated.ResearchReportView{}, err
	}
	rec, err := s.commissionOf(ctx, runID)
	if err != nil {
		return generated.ResearchReportView{}, err
	}
	allow := generated.ResearchAllowance{TimeMs: int(rec.Allowance.TimeMs),
		MaxActions: int(rec.Allowance.MaxActions), MaxJev: int(rec.Allowance.MaxJev),
		MaxTurns: int(rec.Allowance.MaxTurns), MaxConcurrent: int(rec.Allowance.MaxConcurrent)}
	var bytes int64
	dispatched := map[string]int{}
	reused := []string{}
	truncated, err := s.journalScan(ctx, runID, func(e researchcontract.Event) {
		switch e.Kind {
		case "run.dispatched":
			if op := payloadOf(e.Payload, "operation")["operation"]; op != "" {
				dispatched[op]++
			}
		case "run.observed":
			var payload struct {
				BytesIn  int64 `json:"bytesIn"`
				BytesOut int64 `json:"bytesOut"`
			}
			if json.Unmarshal(e.Payload, &payload) == nil {
				bytes += payload.BytesIn + payload.BytesOut
			}
			if e.Outcome == researchcontract.OutcomeReused {
				reused = append(reused, reuseLine(e))
			}
		}
		if e.Outcome == researchcontract.OutcomeReused && e.Kind != "run.observed" &&
			(e.Kind == researchcontract.EventReuse || e.Kind == researchcontract.EventObservation) {
			reused = append(reused, reuseLine(e))
		}
	})
	if err != nil {
		return generated.ResearchReportView{}, err
	}
	view := generated.ResearchReportView{RunId: runID,
		Outcomes: []string{}, Searched: []string{}, Reused: reused, Uncertainty: []string{}}
	for _, id := range cp.SavedRecordIDs {
		view.Outcomes = append(view.Outcomes, s.savedLine(ctx, id))
	}
	for op, count := range dispatched {
		view.Searched = append(view.Searched, fmt.Sprintf("%s x %d", op, count))
	}
	sort.Strings(view.Searched)
	if len(reused) > 100 {
		view.Reused = append(append([]string{}, reused[:100]...),
			fmt.Sprintf("+%d further reused results", len(reused)-100))
	}
	for _, u := range cp.UnresolvedAttempts {
		view.Uncertainty = append(view.Uncertainty, "attempt "+u.AttemptID+": "+u.Reason)
	}
	if ledger.Unknown && len(cp.UnresolvedAttempts) == 0 {
		view.Uncertainty = append(view.Uncertainty, "unknown usage is present on resolved work; see the activity journal")
	}
	view.Budget.Observed = mapUsage(allow, ledger, bytes, truncated)
	view.Budget.Unknown = ledger.Unknown || truncated
	if round.StopReason != "" {
		view.Budget.StopReason = ptrOf(round.StopReason)
	}
	if len(cp.NextWork) > 0 {
		view.NextWork = ptrOf(append([]string{}, cp.NextWork...))
	}
	return view, nil
}

func reuseLine(e researchcontract.Event) string {
	ref := e.ObservationID
	if ref == "" {
		ref = e.CaptureID
	}
	if ref == "" {
		ref = shortID(e.RequestFingerprint, 12)
	}
	if ref == "" {
		ref = e.ID
	}
	return e.Kind + " reused " + ref
}

// savedLine names one saved record from the live row. Unreadable ids stay
// visible as bare ids rather than vanishing from the report.
func (s *Service) savedLine(ctx context.Context, id string) string {
	if opp, err := s.db.Opportunity(ctx, id); err == nil {
		return fmt.Sprintf("opportunity %q (%s rev %d)", opp.Title, opp.ID, opp.Revision)
	}
	if company, err := s.db.Company(ctx, id); err == nil {
		return fmt.Sprintf("company %q (%s rev %d)", company.Name, company.ID, company.Revision)
	}
	return "record " + id
}
