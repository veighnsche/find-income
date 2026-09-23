package store

import (
	"context"
	"database/sql"
)

type RoundCard struct {
	OpportunityID       string `json:"opportunityId"`
	OpportunityRevision int64  `json:"opportunityRevision"`
	CompanyID           string `json:"companyId"`
	CompanyName         string `json:"companyName"`
	Title               string `json:"title"`
	Kind                string `json:"kind"`
	SourceURL           string `json:"sourceUrl"`
	SourceText          string `json:"sourceText"`
	SourceAuditID       string `json:"sourceAuditId"`
	SourceRevision      int64  `json:"sourceRevision"`
	SourceStale         bool   `json:"sourceStale"`
	Decision            string `json:"decision"`
	DecisionRevision    int64  `json:"decisionRevision"`
	CreatedAt           string `json:"createdAt"`
}

type RoundHistoryEvent struct {
	AuditID        string `json:"auditId"`
	AttemptID      string `json:"attemptId"`
	Operation      string `json:"operation"`
	EntityKind     string `json:"entityKind"`
	EntityID       string `json:"entityId"`
	RevisionBefore *int64 `json:"revisionBefore,omitempty"`
	RevisionAfter  *int64 `json:"revisionAfter,omitempty"`
	OccurredAt     string `json:"occurredAt"`
}

func (s *Store) RoundCards(ctx context.Context, roundID string) ([]RoundCard, error) {
	if _, err := s.Round(ctx, roundID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,o.revision,c.id,c.name,o.title,o.kind,
	  COALESCE(o.source_url,''),o.original_text,ac.id,ac.revision_after,
	  COALESCE(d.decision,''),COALESCE(d.revision,0),rc.attached_at
	  FROM round_record_changes rc JOIN audit_changes ac ON ac.id=rc.audit_id
	  JOIN opportunities o ON ac.entity_kind='opportunity' AND o.id=ac.entity_id
	  JOIN companies c ON c.id=o.company_id
	  LEFT JOIN owner_opportunity_decisions d ON d.opportunity_id=o.id AND d.revision=(
	    SELECT MAX(d2.revision) FROM owner_opportunity_decisions d2 WHERE d2.opportunity_id=o.id)
	  WHERE rc.round_id=? AND ac.id=(SELECT ac2.id FROM round_record_changes rc2
	    JOIN audit_changes ac2 ON ac2.id=rc2.audit_id
	    WHERE rc2.round_id=rc.round_id AND ac2.entity_kind='opportunity' AND ac2.entity_id=o.id
	    ORDER BY CASE WHEN ac2.operation IN ('opportunity.create','opportunity.source_create','opportunity.source_refresh')
	      THEN 0 ELSE 1 END,ac2.occurred_at DESC,ac2.id DESC LIMIT 1)
	  ORDER BY rc.attached_at,ac.id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RoundCard{}
	for rows.Next() {
		var card RoundCard
		var sourceRevision sql.NullInt64
		if err := rows.Scan(&card.OpportunityID, &card.OpportunityRevision, &card.CompanyID, &card.CompanyName,
			&card.Title, &card.Kind, &card.SourceURL, &card.SourceText, &card.SourceAuditID, &sourceRevision,
			&card.Decision, &card.DecisionRevision, &card.CreatedAt); err != nil {
			return nil, err
		}
		card.SourceRevision = sourceRevision.Int64
		card.SourceStale = !sourceRevision.Valid || sourceRevision.Int64 != card.OpportunityRevision
		items = append(items, card)
	}
	return items, rows.Err()
}

func (s *Store) RoundHistory(ctx context.Context, roundID string) ([]RoundHistoryEvent, error) {
	if _, err := s.Round(ctx, roundID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ac.id,rc.attempt_id,ac.operation,ac.entity_kind,ac.entity_id,
	  ac.revision_before,ac.revision_after,ac.occurred_at FROM round_record_changes rc
	  JOIN audit_changes ac ON ac.id=rc.audit_id WHERE rc.round_id=?
	  ORDER BY rc.attached_at,ac.id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RoundHistoryEvent{}
	for rows.Next() {
		var item RoundHistoryEvent
		var before, after sql.NullInt64
		if err := rows.Scan(&item.AuditID, &item.AttemptID, &item.Operation, &item.EntityKind, &item.EntityID,
			&before, &after, &item.OccurredAt); err != nil {
			return nil, err
		}
		if before.Valid {
			item.RevisionBefore = &before.Int64
		}
		if after.Valid {
			item.RevisionAfter = &after.Int64
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
