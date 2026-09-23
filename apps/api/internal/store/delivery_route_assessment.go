package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type DeliveryRouteAssessment struct {
	ID            string `json:"id"`
	OpportunityID string `json:"opportunityId"`
	RouteID       string `json:"routeId"`
	SourceSHA256  string `json:"sourceSha256"`
	RouteSHA256   string `json:"routeSha256"`
	InputSHA256   string `json:"inputSha256"`
	Choice        string `json:"choice"`
	RoundID       string `json:"roundId"`
	JevAttemptID  string `json:"jevAttemptId"`
	CreatedAt     string `json:"createdAt"`
}

// DeliveryRouteCandidate checks only identity, exact source provenance and
// SMTP mailbox syntax. Application intent is a recorded Jev judgment.
func DeliveryRouteCandidate(route OpportunityRoute, originalText string) bool {
	if route.Kind != "direct" || route.DestinationText == "" || route.SourceExcerpt == "" ||
		!strings.Contains(originalText, route.SourceExcerpt) || len(route.DestinationText) > 254 ||
		strings.TrimSpace(route.DestinationText) != route.DestinationText {
		return false
	}
	address, err := mail.ParseAddress(route.DestinationText)
	return err == nil && address.Address == route.DestinationText && address.Name == ""
}

func (s *Store) CurrentDeliveryRouteAssessment(ctx context.Context, route OpportunityRoute, opportunity Opportunity) (DeliveryRouteAssessment, error) {
	if route.OpportunityID != opportunity.ID || !DeliveryRouteCandidate(route, opportunity.OriginalText) {
		return DeliveryRouteAssessment{}, ErrInvalid
	}
	var a DeliveryRouteAssessment
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.opportunity_id,a.route_id,a.source_sha256,a.route_sha256,a.input_sha256,
	 a.choice,a.round_id,a.jev_attempt_id,a.created_at FROM delivery_route_assessments a JOIN jev_attempts j ON j.id=a.jev_attempt_id
	 WHERE a.route_id=? AND a.opportunity_id=? AND a.source_sha256=? AND a.route_sha256=? AND j.status='succeeded'
	 ORDER BY a.created_at DESC LIMIT 1`, route.ID, opportunity.ID,
		DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText), DeliveryRouteHash(route)).
		Scan(&a.ID, &a.OpportunityID, &a.RouteID, &a.SourceSHA256, &a.RouteSHA256, &a.InputSHA256,
			&a.Choice, &a.RoundID, &a.JevAttemptID, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryRouteAssessment{}, ErrNotFound
	}
	return a, err
}

// SaveDeliveryRouteAssessment links a complete captured Jev exchange to the
// same current opening and route. A caller cannot author a supported verdict.
func (s *Store) SaveDeliveryRouteAssessment(ctx context.Context, actor Actor, roundID, jevAttemptID string,
	input jev.DeliveryRouteInput, result jev.DeliveryRouteResult) (DeliveryRouteAssessment, error) {
	if actor.Kind != "agent" || actor.ID == "" || roundID == "" || jevAttemptID == "" || result.Choice == "" || len(result.RequestSnapshot) == 0 {
		return DeliveryRouteAssessment{}, ErrInvalid
	}
	digest, err := jev.DeliveryRouteInputDigest(input)
	if err != nil || digest != result.InputSHA256 {
		return DeliveryRouteAssessment{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	defer tx.Rollback()
	if r.State != RoundRunning || !deliveryRouteRoundScope(r, input.OpportunityID) ||
		r.ProfileVersion < 1 || !scopeAllowsActor(r.Scope, actor) {
		return DeliveryRouteAssessment{}, ErrFenced
	}
	var title, sourceURL, originalText, kind, destination, sourceKind, sourceRef, excerpt string
	err = tx.QueryRowContext(ctx, `SELECT o.title,o.source_url,o.original_text,r.kind,r.destination_text,r.source_kind,
	 COALESCE(r.source_ref,''),r.source_excerpt FROM opportunity_routes r JOIN opportunities o ON o.id=r.opportunity_id
	 WHERE r.id=? AND r.opportunity_id=? AND o.archived_at IS NULL`, input.RouteID, input.OpportunityID).
		Scan(&title, &sourceURL, &originalText, &kind, &destination, &sourceKind, &sourceRef, &excerpt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryRouteAssessment{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	if title != input.Title || sourceURL != input.SourceURL || originalText != input.OriginalText ||
		kind != input.RouteKind || destination != input.Destination || sourceKind != input.RouteSourceKind ||
		sourceRef != input.RouteSourceRef || excerpt != input.RouteExcerpt ||
		DeliverySourceHash(title, sourceURL, originalText) != input.SourceSHA256 ||
		deliveryRouteHash(kind, destination, sourceKind, sourceRef, excerpt) != input.RouteSHA256 {
		return DeliveryRouteAssessment{}, ErrConflict
	}
	if !DeliveryRouteCandidate(OpportunityRoute{OpportunityRouteInput: OpportunityRouteInput{Kind: kind, DestinationText: destination, SourceExcerpt: excerpt}}, originalText) {
		return DeliveryRouteAssessment{}, ErrInvalid
	}
	var attemptPurpose, attemptStatus, attemptRound, operation, resource string
	var requestedModel sql.NullString
	var roundAttemptState RoundAttemptState
	var generation, profileVersion int64
	var logical, raw []byte
	err = tx.QueryRowContext(ctx, `SELECT j.purpose,j.status,j.round_id,j.logical_request_json,j.raw_response_bytes,j.requested_model,
	 a.operation,a.resource_id,a.state,a.generation,j.profile_version FROM jev_attempts j JOIN round_attempts a ON a.id=j.round_attempt_id WHERE j.id=?`, jevAttemptID).
		Scan(&attemptPurpose, &attemptStatus, &attemptRound, &logical, &raw, &requestedModel, &operation, &resource, &roundAttemptState, &generation, &profileVersion)
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	if attemptPurpose != "delivery_route" || attemptStatus != "succeeded" || attemptRound != roundID ||
		operation != RoundJevRequest || resource != "opportunity:"+input.OpportunityID ||
		(roundAttemptState != AttemptSucceeded && roundAttemptState != AttemptObservedSuccess) ||
		generation < 1 || generation > r.Generation || profileVersion != r.ProfileVersion {
		return DeliveryRouteAssessment{}, ErrFenced
	}
	result, err = jev.ValidateCapturedDeliveryRoute(input, result, logical, raw, requestedModel.String)
	if err != nil {
		return DeliveryRouteAssessment{}, ErrFenced
	}
	id, err := randomID()
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_route_assessments
	 (id,opportunity_id,route_id,source_sha256,route_sha256,input_sha256,choice,round_id,jev_attempt_id,result_json,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, input.OpportunityID, input.RouteID, input.SourceSHA256, input.RouteSHA256,
		result.InputSHA256, result.Choice, roundID, jevAttemptID, string(resultJSON), utcNow())
	if err != nil {
		return DeliveryRouteAssessment{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "delivery.route_assess", id); err != nil {
		return DeliveryRouteAssessment{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryRouteAssessment{}, err
	}
	return s.CurrentDeliveryRouteAssessment(ctx, OpportunityRoute{OpportunityRouteInput: OpportunityRouteInput{ID: input.RouteID, OpportunityID: input.OpportunityID,
		Kind: kind, DestinationText: destination, SourceKind: sourceKind, SourceRef: sourceRef, SourceExcerpt: excerpt}}, Opportunity{
		ID: input.OpportunityID, Title: title, SourceURL: sourceURL, OriginalText: originalText})
}

func currentApplicationJudgmentTx(ctx context.Context, tx *sql.Tx, routeID, opportunityID, sourceHash, routeHash string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM delivery_route_assessments a JOIN jev_attempts j ON j.id=a.jev_attempt_id
	 WHERE a.route_id=? AND a.opportunity_id=? AND a.source_sha256=? AND a.route_sha256=?
	 AND a.choice='application_mailbox' AND j.status='succeeded'`, routeID, opportunityID, sourceHash, routeHash).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
