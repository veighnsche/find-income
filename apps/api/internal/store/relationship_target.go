package store

import (
	"context"
	"database/sql"
	"errors"
)

type RelationshipTarget struct {
	Kind         string                    `json:"kind"`
	Counterparty *RelationshipCounterparty `json:"counterparty,omitempty"`
	Event        *RelationshipEvent        `json:"event,omitempty"`
	Route        *OpportunityRoute         `json:"route,omitempty"`
}

func (s *Store) RelationshipTarget(ctx context.Context, id string) (RelationshipTarget, error) {
	if id == "" {
		return RelationshipTarget{}, ErrInvalid
	}
	var counterparty RelationshipCounterparty
	err := s.db.QueryRowContext(ctx, `SELECT id,display_name,kind,organization_text,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM relationship_counterparties WHERE id=?`, id).Scan(
		&counterparty.ID, &counterparty.DisplayName, &counterparty.Kind, &counterparty.OrganizationText, &counterparty.SourceKind, &counterparty.SourceRef, &counterparty.SourceExcerpt, &counterparty.ObservedAt, &counterparty.Revision, &counterparty.CreatedAt, &counterparty.UpdatedAt)
	if err == nil {
		return RelationshipTarget{Kind: "counterparty", Counterparty: &counterparty}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RelationshipTarget{}, err
	}
	var event RelationshipEvent
	err = s.db.QueryRowContext(ctx, `SELECT id,COALESCE(counterparty_id,''),COALESCE(opportunity_id,''),kind,summary,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM relationship_events WHERE id=?`, id).Scan(
		&event.ID, &event.CounterpartyID, &event.OpportunityID, &event.Kind, &event.Summary, &event.SourceKind, &event.SourceRef, &event.SourceExcerpt, &event.ObservedAt, &event.Revision, &event.CreatedAt, &event.UpdatedAt)
	if err == nil {
		return RelationshipTarget{Kind: "event", Event: &event}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RelationshipTarget{}, err
	}
	var route OpportunityRoute
	err = s.db.QueryRowContext(ctx, `SELECT id,opportunity_id,COALESCE(event_id,''),COALESCE(counterparty_id,''),kind,destination_text,source_kind,COALESCE(source_ref,''),source_excerpt,observed_at,revision,created_at,updated_at FROM opportunity_routes WHERE id=?`, id).Scan(
		&route.ID, &route.OpportunityID, &route.EventID, &route.CounterpartyID, &route.Kind, &route.DestinationText, &route.SourceKind, &route.SourceRef, &route.SourceExcerpt, &route.ObservedAt, &route.Revision, &route.CreatedAt, &route.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RelationshipTarget{}, ErrNotFound
	}
	if err != nil {
		return RelationshipTarget{}, err
	}
	return RelationshipTarget{Kind: "route", Route: &route}, nil
}
