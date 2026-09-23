package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RecoverCapturedDeliveryRouteAttempt resolves only a route-classification
// dispatch whose complete provider exchange was already captured. An absent,
// incomplete or invalid capture remains uncertain without spending another
// request. The caller must not fall through to external reconciliation when
// handled is true.
func (s *Store) RecoverCapturedDeliveryRouteAttempt(ctx context.Context, roundID, attemptID string, expectedGeneration int64) (handled, recovered bool, err error) {
	if roundID == "" || attemptID == "" || expectedGeneration < 1 {
		return false, false, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	if round.State != RoundPaused || round.Generation != expectedGeneration {
		return false, false, ErrFenced
	}
	var operation, resource, requestKey string
	var attemptGeneration int64
	var state RoundAttemptState
	err = tx.QueryRowContext(ctx, `SELECT operation,resource_id,request_key,generation,state FROM round_attempts WHERE id=? AND round_id=?`,
		attemptID, roundID).Scan(&operation, &resource, &requestKey, &attemptGeneration, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, ErrNotFound
	}
	if err != nil {
		return false, false, err
	}
	if operation != RoundJevRequest || !strings.Contains(requestKey, "/delivery-route/") {
		return false, false, nil
	}
	if state != AttemptUncertain || attemptGeneration < 1 || attemptGeneration > round.Generation {
		return true, false, ErrFenced
	}
	var jevID, purpose, status, inputSHA, requestedModel, returnedModel string
	var logical, raw []byte
	var truncated, readError int
	var inputTokens, outputTokens sql.NullInt64
	var profileVersion int64
	err = tx.QueryRowContext(ctx, `SELECT id,purpose,status,input_sha256,logical_request_json,raw_response_bytes,
	 COALESCE(requested_model,''),COALESCE(returned_model,''),response_truncated,response_read_error,
	 input_tokens,output_tokens,profile_version FROM jev_attempts WHERE round_attempt_id=? AND round_id=?`, attemptID, roundID).
		Scan(&jevID, &purpose, &status, &inputSHA, &logical, &raw, &requestedModel, &returnedModel,
			&truncated, &readError, &inputTokens, &outputTokens, &profileVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if purpose != "delivery_route" || status != "succeeded" || truncated != 0 || readError != 0 ||
		profileVersion != round.ProfileVersion || !inputTokens.Valid || !outputTokens.Valid {
		return true, false, nil
	}
	logicalHash := sha256.Sum256(logical)
	if inputSHA != hex.EncodeToString(logicalHash[:]) {
		return true, false, nil
	}
	var captured struct {
		State jev.DeliveryRouteInput `json:"state"`
	}
	if json.Unmarshal(logical, &captured) != nil {
		return true, false, nil
	}
	input := captured.State
	result, err := jev.RecoverCapturedDeliveryRoute(input, logical, raw, requestedModel)
	if err != nil || result.ProviderResult.ReturnedModel != returnedModel ||
		result.ProviderResult.Usage.InputTokens != inputTokens.Int64 || result.ProviderResult.Usage.OutputTokens != outputTokens.Int64 ||
		resource != "opportunity:"+input.OpportunityID || !deliveryRouteRoundScope(round, input.OpportunityID) {
		return true, false, nil
	}
	var title, sourceURL, originalText, kind, destination, sourceKind, sourceRef, excerpt string
	err = tx.QueryRowContext(ctx, `SELECT o.title,o.source_url,o.original_text,r.kind,r.destination_text,r.source_kind,
	 COALESCE(r.source_ref,''),r.source_excerpt FROM opportunity_routes r JOIN opportunities o ON o.id=r.opportunity_id
	 WHERE r.id=? AND r.opportunity_id=? AND o.archived_at IS NULL`, input.RouteID, input.OpportunityID).
		Scan(&title, &sourceURL, &originalText, &kind, &destination, &sourceKind, &sourceRef, &excerpt)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if title != input.Title || sourceURL != input.SourceURL || originalText != input.OriginalText ||
		kind != input.RouteKind || destination != input.Destination || sourceKind != input.RouteSourceKind ||
		sourceRef != input.RouteSourceRef || excerpt != input.RouteExcerpt ||
		DeliverySourceHash(title, sourceURL, originalText) != input.SourceSHA256 ||
		deliveryRouteHash(kind, destination, sourceKind, sourceRef, excerpt) != input.RouteSHA256 ||
		!DeliveryRouteCandidate(OpportunityRoute{OpportunityRouteInput: OpportunityRouteInput{
			Kind: kind, DestinationText: destination, SourceExcerpt: excerpt}}, originalText) {
		return true, false, nil
	}
	resolution, _ := json.Marshal(map[string]any{"jevAttemptId": jevID, "localRecovered": true, "choice": result.Choice})
	changed, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='observed_success',result_json=?,finished_at=?,updated_at=?
	 WHERE id=? AND round_id=? AND state='uncertain'`, string(resolution), utcNow(), utcNow(), attemptID, roundID)
	if err != nil {
		return true, false, err
	}
	count, err := changed.RowsAffected()
	if err != nil || count != 1 {
		return true, false, ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "delivery-route-recovery"}, "delivery.route_recover", jevID); err != nil {
		return true, false, err
	}
	if err := tx.Commit(); err != nil {
		return true, false, err
	}
	return true, true, nil
}

func deliveryRouteRoundScope(round Round, opportunityID string) bool {
	if round.Outcome != "prepare" && round.Outcome != "process_input" ||
		!scopeAllows(round.Scope, RoundJevRequest, "opportunity:"+opportunityID) ||
		!scopeAllows(round.Scope, RoundPrepareApplicationPack, "opportunity:"+opportunityID) {
		return false
	}
	return round.Outcome != "process_input" || len(round.Scope.Resources) == 1 && round.Scope.Resources[0] == "opportunity:"+opportunityID
}
