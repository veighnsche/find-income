package codexservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// assessPreparedPackRoute runs inside the already commissioned pack-prepare
// turn after the immutable pack exists. Missing route/sender support never
// discards the useful pack; the returned limitation is visible to the owner.
func (s *Service) assessPreparedPackRoute(ctx context.Context, round store.Round, authority store.RoundToolAuthority,
	args applicationPackPrepareArgs, packID string, relevance jevservice.Service) string {
	if err := s.db.CurrentDeliveryPackRole(ctx, packID); err != nil {
		return "pack_role_changed"
	}
	opportunity, err := s.db.Opportunity(ctx, args.OpportunityID)
	if err != nil {
		return "opportunity_unavailable"
	}
	routes, err := s.db.ListOpportunityRoutes(ctx, opportunity.ID)
	if err != nil {
		return "route_unavailable"
	}
	candidates := make([]store.OpportunityRoute, 0, 3)
	for i := range routes {
		if store.DeliveryRouteCandidate(routes[i], opportunity.OriginalText) {
			candidates = append(candidates, routes[i])
		}
	}
	if len(candidates) == 0 {
		return "no_sourced_email_route"
	}
	if len(candidates) > 3 {
		return "too_many_email_routes"
	}
	accepted, unresolved := 0, false
	for _, route := range candidates {
		choice := s.assessOnePreparedRoute(ctx, round, authority, args, relevance, opportunity, route)
		if choice == "application_mailbox" {
			accepted++
		} else if choice != "other_contact" {
			unresolved = true
		}
	}
	if accepted > 1 {
		return "ambiguous_email_routes"
	}
	if unresolved {
		return "route_assessment_unresolved"
	}
	if accepted == 1 {
		return "application_mailbox"
	}
	return "other_contact"
}

func (s *Service) assessOnePreparedRoute(ctx context.Context, round store.Round, authority store.RoundToolAuthority,
	args applicationPackPrepareArgs, relevance jevservice.Service, opportunity store.Opportunity, route store.OpportunityRoute) string {
	if assessment, err := s.db.CurrentDeliveryRouteAssessment(ctx, route, opportunity); err == nil {
		return assessment.Choice
	} else if !errors.Is(err, store.ErrNotFound) {
		return "route_assessment_unavailable"
	}
	if !deliveryRouteRoundAllows(round, opportunity.ID) {
		return "route_assessment_unavailable"
	}
	input := jev.DeliveryRouteInput{OpportunityID: opportunity.ID, RouteID: route.ID, Title: opportunity.Title,
		SourceURL: opportunity.SourceURL, OriginalText: opportunity.OriginalText,
		SourceSHA256: store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText),
		RouteKind:    route.Kind, Destination: route.DestinationText, RouteSourceKind: route.SourceKind,
		RouteSourceRef: route.SourceRef, RouteExcerpt: route.SourceExcerpt, RouteSHA256: store.DeliveryRouteHash(route),
		MaxReportedTokens: 2500}
	// A saved pack can outlive its turn. Inspect every captured route attempt
	// before allocating another request key or contacting the provider.
	priorAttempts, err := s.db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		return "route_assessment_unresolved"
	}
	for _, attempt := range priorAttempts {
		if attempt.Purpose != "delivery_route" {
			continue
		}
		var captured struct {
			State jev.DeliveryRouteInput `json:"state"`
		}
		if json.Unmarshal(attempt.LogicalRequestJSON, &captured) != nil || captured.State != input {
			continue
		}
		if attempt.Status != "succeeded" || attempt.ResponseTruncated || attempt.ResponseReadError {
			return "route_assessment_unresolved"
		}
		result, err := jev.RecoverCapturedDeliveryRoute(input, attempt.LogicalRequestJSON, attempt.RawResponseBytes, attempt.RequestedModel)
		if err != nil {
			return "route_assessment_unresolved"
		}
		assessment, err := s.db.SaveDeliveryRouteAssessment(ctx, authority.Actor, round.ID, attempt.ID, input, result)
		if err != nil {
			return "route_assessment_unresolved"
		}
		return assessment.Choice
	}
	if relevance.Store == nil || relevance.Client == nil {
		return "jev_unavailable"
	}
	prefix := args.RequestKey + "/delivery-route/" + route.ID
	attemptIDs, err := s.db.JevAttemptIDsForRequestPrefix(ctx, round.ID, prefix)
	if err != nil || len(attemptIDs) > 1 {
		return "route_assessment_unresolved"
	}
	if len(attemptIDs) == 1 {
		attempt, err := s.db.JevAttempt(ctx, attemptIDs[0])
		if err != nil || attempt.Status != "succeeded" || attempt.Purpose != "delivery_route" ||
			attempt.ResponseTruncated || attempt.ResponseReadError {
			return "route_assessment_unresolved"
		}
		result, err := jev.RecoverCapturedDeliveryRoute(input, attempt.LogicalRequestJSON, attempt.RawResponseBytes, attempt.RequestedModel)
		if err != nil {
			return "route_assessment_unresolved"
		}
		assessment, err := s.db.SaveDeliveryRouteAssessment(ctx, authority.Actor, round.ID, attempt.ID, input, result)
		if err != nil {
			return "route_assessment_unresolved"
		}
		return assessment.Choice
	}
	result, err := relevance.RunDeliveryRoute(ctx, jevservice.Binding{Actor: authority.Actor, RoundID: round.ID,
		ResourceID: "opportunity:" + opportunity.ID, RequestKeyPrefix: prefix, ProfileVersion: round.ProfileVersion,
		BoundCapability: args.Capability, MaxReportedTokens: 2500}, input)
	if err != nil {
		return "route_assessment_unresolved"
	}
	attemptIDs, err = s.db.JevAttemptIDsForRequestPrefix(ctx, round.ID, prefix)
	if err != nil || len(attemptIDs) != 1 {
		return "route_assessment_unresolved"
	}
	assessment, err := s.db.SaveDeliveryRouteAssessment(ctx, authority.Actor, round.ID, attemptIDs[0], input, result)
	if err != nil {
		return "route_assessment_unresolved"
	}
	return assessment.Choice
}

// CompletePackDeliveryRoute finishes the remaining route step for an already
// committed pack on Resume. The trusted agency calls this in-process; it
// never re-renders a pack or replays a Codex turn.
func (s *Service) CompletePackDeliveryRoute(ctx context.Context, roundID, packID string) (string, error) {
	if s == nil || s.db == nil || roundID == "" || packID == "" {
		return "", store.ErrInvalid
	}
	round, err := s.db.Round(ctx, roundID)
	if err != nil {
		return "", err
	}
	pack, err := s.db.ApplicationPack(ctx, packID)
	if err != nil {
		return "", err
	}
	if !deliveryRouteRoundAllows(round, pack.OpportunityID) || pack.ProfileRevision != round.ProfileVersion {
		return "", store.ErrFenced
	}
	if err := s.db.CurrentDeliveryPackRole(ctx, pack.ID); err != nil {
		return "", err
	}
	opportunity, err := s.db.Opportunity(ctx, pack.OpportunityID)
	if err != nil {
		return "", err
	}
	events, err := s.db.RoundHistory(ctx, round.ID)
	if err != nil {
		return "", err
	}
	linked := false
	for _, event := range events {
		if event.Operation == store.RoundPrepareApplicationPack && event.EntityKind == "application_pack" && event.EntityID == pack.ID {
			linked = true
			break
		}
	}
	if !linked {
		return "", store.ErrFenced
	}
	s.mu.Lock()
	relevance := s.packConfig.Relevance
	s.mu.Unlock()
	status := s.assessPreparedPackRoute(ctx, round, store.RoundToolAuthority{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}},
		applicationPackPrepareArgs{RoundID: round.ID, OpportunityID: opportunity.ID, RequestKey: "recovered-pack-" + pack.ID}, pack.ID, relevance)
	return status, nil
}

func deliveryRouteRoundAllows(round store.Round, opportunityID string) bool {
	if round.State != store.RoundRunning || round.Outcome != "prepare" && round.Outcome != "process_input" {
		return false
	}
	resource := "opportunity:" + opportunityID
	if round.Outcome == "process_input" && (len(round.Scope.Resources) != 2 || round.Scope.Resources[0] != resource || round.Scope.Resources[1] != "campaign:active") {
		return false
	}
	hasResource, hasJev, hasPack := false, false, false
	for _, item := range round.Scope.Resources {
		hasResource = hasResource || item == resource
	}
	for _, item := range round.Scope.Operations {
		hasJev = hasJev || item == store.RoundJevRequest
		hasPack = hasPack || item == store.RoundPrepareApplicationPack
	}
	return hasResource && hasJev && hasPack
}
