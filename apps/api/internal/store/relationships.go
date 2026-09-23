package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Relationship writes are payloads for ApplyRoundMutation only. There is no
// independently callable write method, so scope, allowance and audit stay
// attached to the same transaction as the relationship record.
type RelationshipMutationInput struct {
	Counterparty *RelationshipCounterpartyInput `json:"counterparty,omitempty"`
	Event        *RelationshipEventInput        `json:"event,omitempty"`
	Route        *OpportunityRouteInput         `json:"route,omitempty"`
}

type RelationshipCounterpartyInput struct {
	ID               string `json:"id,omitempty"`
	DisplayName      string `json:"displayName"`
	Kind             string `json:"kind"`
	OrganizationText string `json:"organizationText"`
	SourceKind       string `json:"sourceKind"`
	SourceRef        string `json:"sourceRef"`
	SourceExcerpt    string `json:"sourceExcerpt"`
	ObservedAt       string `json:"observedAt"`
}

type RelationshipEventInput struct {
	ID             string `json:"id,omitempty"`
	CounterpartyID string `json:"counterpartyId"`
	OpportunityID  string `json:"opportunityId"`
	Kind           string `json:"kind"`
	Summary        string `json:"summary"`
	SourceKind     string `json:"sourceKind"`
	SourceRef      string `json:"sourceRef"`
	SourceExcerpt  string `json:"sourceExcerpt"`
	ObservedAt     string `json:"observedAt"`
}

type OpportunityRouteInput struct {
	ID              string `json:"id,omitempty"`
	OpportunityID   string `json:"opportunityId"`
	EventID         string `json:"eventId"`
	CounterpartyID  string `json:"counterpartyId"`
	Kind            string `json:"kind"`
	DestinationText string `json:"destinationText"`
	SourceKind      string `json:"sourceKind"`
	SourceRef       string `json:"sourceRef"`
	SourceExcerpt   string `json:"sourceExcerpt"`
	ObservedAt      string `json:"observedAt"`
}

type RelationshipCounterparty struct {
	RelationshipCounterpartyInput
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type RelationshipEvent struct {
	RelationshipEventInput
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type OpportunityRoute struct {
	OpportunityRouteInput
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func validRelationshipSource(kind, ref, excerpt, observed string) bool {
	if !boundedText(kind, 80, true) || !boundedText(ref, 1000, false) ||
		len(excerpt) == 0 || len(excerpt) > 4000 || strings.TrimSpace(excerpt) == "" || len(observed) > 50 {
		return false
	}
	_, err := time.Parse(time.RFC3339, observed)
	return err == nil
}

func boundedText(value string, max int, required bool) bool {
	return len(value) <= max && strings.TrimSpace(value) == value && (!required || value != "")
}

func validCounterparty(input RelationshipCounterpartyInput) bool {
	return boundedText(input.DisplayName, 200, true) &&
		(input.Kind == "recruiter" || input.Kind == "referrer" || input.Kind == "contact") &&
		boundedText(input.OrganizationText, 200, false) &&
		validRelationshipSource(input.SourceKind, input.SourceRef, input.SourceExcerpt, input.ObservedAt)
}

func validRelationshipEvent(input RelationshipEventInput) bool {
	return boundedText(input.CounterpartyID, 64, false) && boundedText(input.OpportunityID, 64, false) &&
		(input.Kind == "introduction" || input.Kind == "conversation" || input.Kind == "referral" || input.Kind == "other") &&
		boundedText(input.Summary, 2000, true) &&
		validRelationshipSource(input.SourceKind, input.SourceRef, input.SourceExcerpt, input.ObservedAt)
}

func validOpportunityRoute(input OpportunityRouteInput) bool {
	return boundedText(input.OpportunityID, 64, true) && boundedText(input.EventID, 64, false) &&
		boundedText(input.CounterpartyID, 64, false) &&
		(input.Kind == "direct" || input.Kind == "referral" || input.Kind == "recruiter") &&
		boundedText(input.DestinationText, 1000, false) &&
		validRelationshipSource(input.SourceKind, input.SourceRef, input.SourceExcerpt, input.ObservedAt)
}

func presentRelationshipInput(input RelationshipMutationInput) int {
	n := 0
	if input.Counterparty != nil {
		n++
	}
	if input.Event != nil {
		n++
	}
	if input.Route != nil {
		n++
	}
	return n
}

func relationshipInputID(input RelationshipMutationInput) string {
	switch {
	case input.Counterparty != nil:
		return input.Counterparty.ID
	case input.Event != nil:
		return input.Event.ID
	case input.Route != nil:
		return input.Route.ID
	default:
		return ""
	}
}

// writeRelationshipTx is called by the core round switch after capability,
// resource and expected-revision fencing. For a correction, expectedRevision
// is the record revision and input.ID identifies the existing record. The core
// checks both old and new linked resources before a sourced reassociation.
func writeRelationshipTx(ctx context.Context, tx *sql.Tx, operation string, expectedRevision int64, input RelationshipMutationInput) (string, string, int64, error) {
	if presentRelationshipInput(input) != 1 || expectedRevision < 1 {
		return "", "", 0, ErrInvalid
	}
	correct := operation == "relationship.correct"
	if !correct && relationshipInputID(input) != "" {
		return "", "", 0, ErrInvalid
	}
	if correct && relationshipInputID(input) == "" {
		return "", "", 0, ErrInvalid
	}
	now := utcNow()
	if value := input.Counterparty; value != nil {
		if !validCounterparty(*value) || (!correct && operation != "relationship.counterparty_create") {
			return "", "", 0, ErrInvalid
		}
		if correct {
			result, err := tx.ExecContext(ctx, `UPDATE relationship_counterparties SET display_name=?,kind=?,organization_text=?,source_kind=?,source_ref=?,source_excerpt=?,observed_at=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, value.DisplayName, value.Kind, value.OrganizationText, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, value.ID, expectedRevision)
			return changedRelationship(result, err, value.ID, "relationship_counterparty", expectedRevision)
		}
		id, err := randomID()
		if err != nil {
			return "", "", 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO relationship_counterparties(id,display_name,kind,organization_text,source_kind,source_ref,source_excerpt,observed_at,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,1,?,?)`, id, value.DisplayName, value.Kind, value.OrganizationText, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, now)
		return id, "relationship_counterparty", 1, err
	}
	if value := input.Event; value != nil {
		if !validRelationshipEvent(*value) || (!correct && operation != "relationship.event_create") {
			return "", "", 0, ErrInvalid
		}
		if err := checkRelationshipRefsTx(ctx, tx, value.CounterpartyID, value.OpportunityID, "", ""); err != nil {
			return "", "", 0, err
		}
		if correct {
			result, err := tx.ExecContext(ctx, `UPDATE relationship_events SET counterparty_id=?,opportunity_id=?,kind=?,summary=?,source_kind=?,source_ref=?,source_excerpt=?,observed_at=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, optionalText(value.CounterpartyID), optionalText(value.OpportunityID), value.Kind, value.Summary, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, value.ID, expectedRevision)
			return changedRelationship(result, err, value.ID, "relationship_event", expectedRevision)
		}
		id, err := randomID()
		if err != nil {
			return "", "", 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO relationship_events(id,counterparty_id,opportunity_id,kind,summary,source_kind,source_ref,source_excerpt,observed_at,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,1,?,?)`, id, optionalText(value.CounterpartyID), optionalText(value.OpportunityID), value.Kind, value.Summary, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, now)
		return id, "relationship_event", 1, err
	}
	value := input.Route
	if !validOpportunityRoute(*value) || (!correct && operation != "relationship.route_create") {
		return "", "", 0, ErrInvalid
	}
	if err := checkRelationshipRefsTx(ctx, tx, value.CounterpartyID, value.OpportunityID, value.EventID, value.Kind); err != nil {
		return "", "", 0, err
	}
	if correct {
		result, err := tx.ExecContext(ctx, `UPDATE opportunity_routes SET opportunity_id=?,event_id=?,counterparty_id=?,kind=?,destination_text=?,source_kind=?,source_ref=?,source_excerpt=?,observed_at=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, value.OpportunityID, optionalText(value.EventID), optionalText(value.CounterpartyID), value.Kind, value.DestinationText, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, value.ID, expectedRevision)
		return changedRelationship(result, err, value.ID, "opportunity_route", expectedRevision)
	}
	id, err := randomID()
	if err != nil {
		return "", "", 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_routes(id,opportunity_id,event_id,counterparty_id,kind,destination_text,source_kind,source_ref,source_excerpt,observed_at,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, value.OpportunityID, optionalText(value.EventID), optionalText(value.CounterpartyID), value.Kind, value.DestinationText, value.SourceKind, optionalText(value.SourceRef), value.SourceExcerpt, value.ObservedAt, now, now)
	return id, "opportunity_route", 1, err
}

func changedRelationship(result sql.Result, err error, id, kind string, before int64) (string, string, int64, error) {
	if err != nil {
		return "", "", 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", "", 0, err
	}
	if n == 0 {
		return "", "", 0, ErrConflict
	}
	return id, kind, before + 1, nil
}

func checkRelationshipRefsTx(ctx context.Context, tx *sql.Tx, counterpartyID, opportunityID, eventID, routeKind string) error {
	if counterpartyID != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM relationship_counterparties WHERE id=?`, counterpartyID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
	}
	if opportunityID != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
	}
	if eventID != "" {
		var eventOpportunity, eventCounterparty sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT opportunity_id,counterparty_id FROM relationship_events WHERE id=?`, eventID).Scan(&eventOpportunity, &eventCounterparty)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if eventOpportunity.Valid && eventOpportunity.String != opportunityID || eventCounterparty.Valid && eventCounterparty.String != counterpartyID {
			return ErrInvalid
		}
	}
	if routeKind == "referral" && counterpartyID == "" {
		return ErrInvalid
	}
	return nil
}

func (s *Store) ListRelationshipCounterparties(ctx context.Context) ([]RelationshipCounterparty, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,display_name,kind,organization_text,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM relationship_counterparties ORDER BY updated_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RelationshipCounterparty{}
	for rows.Next() {
		var v RelationshipCounterparty
		if err := rows.Scan(&v.ID, &v.DisplayName, &v.Kind, &v.OrganizationText, &v.SourceKind, &v.SourceRef, &v.SourceExcerpt, &v.ObservedAt, &v.Revision, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) ListRelationshipEvents(ctx context.Context) ([]RelationshipEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(counterparty_id,''),COALESCE(opportunity_id,''),kind,summary,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM relationship_events ORDER BY updated_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RelationshipEvent{}
	for rows.Next() {
		var v RelationshipEvent
		if err := rows.Scan(&v.ID, &v.CounterpartyID, &v.OpportunityID, &v.Kind, &v.Summary, &v.SourceKind, &v.SourceRef, &v.SourceExcerpt, &v.ObservedAt, &v.Revision, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) ListOpportunityRoutes(ctx context.Context, opportunityID string) ([]OpportunityRoute, error) {
	if opportunityID == "" {
		return nil, ErrInvalid
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,opportunity_id,COALESCE(event_id,''),COALESCE(counterparty_id,''),kind,destination_text,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM opportunity_routes WHERE opportunity_id=? ORDER BY updated_at DESC,id DESC LIMIT 100`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []OpportunityRoute{}
	for rows.Next() {
		var v OpportunityRoute
		if err := rows.Scan(&v.ID, &v.OpportunityID, &v.EventID, &v.CounterpartyID, &v.Kind, &v.DestinationText, &v.SourceKind, &v.SourceRef, &v.SourceExcerpt, &v.ObservedAt, &v.Revision, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
