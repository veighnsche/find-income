package store

import (
	"context"
	"database/sql"
	"errors"
)

type ownerSourceFieldLocks struct {
	title, kind, workPattern, location, posted, deadline, compensation bool
}

// Owner edits to extracted fields keep their authority across later source
// revisions. Stage, notes and company assignment are always owner-controlled.
func ownerSourceFieldLocksTx(ctx context.Context, tx *sql.Tx, opportunityID string) (ownerSourceFieldLocks, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
  json_extract(r.snapshot_json,'$.title') IS NOT json_extract(p.snapshot_json,'$.title'),
  json_extract(r.snapshot_json,'$.kind') IS NOT json_extract(p.snapshot_json,'$.kind'),
  json_extract(r.snapshot_json,'$.workPattern') IS NOT json_extract(p.snapshot_json,'$.workPattern'),
  json_extract(r.snapshot_json,'$.locationText') IS NOT json_extract(p.snapshot_json,'$.locationText'),
  json_extract(r.snapshot_json,'$.postedOn') IS NOT json_extract(p.snapshot_json,'$.postedOn'),
  json_extract(r.snapshot_json,'$.deadlineOn') IS NOT json_extract(p.snapshot_json,'$.deadlineOn'),
  json_extract(r.snapshot_json,'$.compensation') IS NOT json_extract(p.snapshot_json,'$.compensation')
  FROM record_changes r JOIN record_changes p ON p.sequence=(
    SELECT MAX(previous.sequence) FROM record_changes previous
    WHERE previous.entity_kind='opportunity' AND previous.entity_id=r.entity_id
      AND previous.sequence<r.sequence)
  LEFT JOIN owner_instruction_applications app ON app.audit_id=r.audit_id
  WHERE r.entity_kind='opportunity' AND r.entity_id=? AND (
    (r.operation='opportunity.patch' AND r.actor_kind='administrator') OR
    (r.operation='opportunity.owner_correction' AND r.actor_kind='agent' AND app.instruction_id IS NOT NULL))
  ORDER BY r.sequence`, opportunityID)
	if err != nil {
		return ownerSourceFieldLocks{}, err
	}
	defer rows.Close()
	var locks ownerSourceFieldLocks
	for rows.Next() {
		var title, kind, pattern, location, posted, deadline, compensation int
		if err := rows.Scan(&title, &kind, &pattern, &location, &posted, &deadline, &compensation); err != nil {
			return ownerSourceFieldLocks{}, err
		}
		locks.title = locks.title || title == 1
		locks.kind = locks.kind || kind == 1
		locks.workPattern = locks.workPattern || pattern == 1
		locks.location = locks.location || location == 1
		locks.posted = locks.posted || posted == 1
		locks.deadline = locks.deadline || deadline == 1
		locks.compensation = locks.compensation || compensation == 1
	}
	return locks, rows.Err()
}

func insertSourcedSnapshotTx(ctx context.Context, tx *sql.Tx, actor Actor, ingestionID, openingID, opportunityID, changeID, companyID, kind, sourceURL, sourceText string) error {
	var contextVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT context_version FROM qualification_input_versions WHERE opportunity_id=?`, opportunityID).Scan(&contextVersion); err != nil {
		return err
	}
	sourceID, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO evidence_sources
  (id,opportunity_id,company_id,opportunity_kind,context_version,source_kind,
   record_change_audit_id,source_url,original_text,content_sha256,recorded_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,'vacancy_snapshot',?,?,?,?,?,?,?)`, sourceID, opportunityID, companyID,
		kind, contextVersion, changeID, optionalText(sourceURL), sourceText, sourceDigest(sourceText), utcNow(), actor.Kind, actor.ID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_requests SET status='processing',opportunity_id=?,
  record_change_id=?,source_id=?,safe_error_code=NULL,updated_at=? WHERE id=? AND source_opening_id=?
  AND opportunity_id IS NULL AND source_id IS NULL`, opportunityID, changeID, sourceID, utcNow(), ingestionID, openingID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	return bindSourceOpeningTx(ctx, tx, IngestionRequest{ID: ingestionID, SourceOpeningID: openingID}, opportunityID)
}

// saveSourcedOpportunityTx is called only after the shared round mutation
// boundary has checked capability, scope, generation, sighting and allowance.
// It forces current source bytes and commits the record audit and immutable
// vacancy snapshot in that same transaction.
func saveSourcedOpportunityTx(ctx context.Context, tx *sql.Tx, actor Actor, input SourceOpportunityMutationInput) (entityID string, revision int64, auditID string, err error) {
	if !requiredActor(actor) || input.SourceOpeningID == "" || input.CompanyID == "" || input.ExpectedRevision < 1 {
		return "", 0, "", ErrInvalid
	}
	var ingestionID, sourceText string
	var sourceURL, sourceID, mappedOpportunityID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.source_url,i.original_text,i.source_id,s.opportunity_id
  FROM source_openings s JOIN ingestion_requests i ON i.id=s.current_ingestion_id
  WHERE s.id=? AND i.origin='collector' AND i.source_opening_id=s.id`, input.SourceOpeningID).
		Scan(&ingestionID, &sourceURL, &sourceText, &sourceID, &mappedOpportunityID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", ErrNotFound
	}
	if err != nil {
		return "", 0, "", err
	}
	if sourceID.Valid || sourceText == "" {
		return "", 0, "", ErrConflict
	}
	var companyRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM companies WHERE id=? AND archived_at IS NULL`, input.CompanyID).Scan(&companyRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", ErrNotFound
	}
	if err != nil {
		return "", 0, "", err
	}
	proposed := input.Opportunity
	proposed.CompanyID = input.CompanyID
	proposed.SourceURL = sourceURL.String
	proposed.OriginalText = sourceText
	if !mappedOpportunityID.Valid {
		if input.ExpectedRevision != companyRevision {
			return "", 0, "", ErrConflict
		}
		proposed.Stage, proposed.Notes = "discovered", ""
		proposed, err = validateOpportunity(proposed)
		if err != nil {
			return "", 0, "", err
		}
		entityID, err = randomID()
		if err != nil {
			return "", 0, "", err
		}
		now := recordNow()
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,source_url,original_text,notes,stage,work_pattern,location_text,
   posted_on,deadline_on,revision,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, entityID, proposed.CompanyID, proposed.Title,
			proposed.Kind, optionalText(proposed.SourceURL), proposed.OriginalText, proposed.Notes,
			proposed.Stage, proposed.WorkPattern, proposed.LocationText, optionalText(proposed.PostedOn),
			optionalText(proposed.DeadlineOn), now, now)
		if err != nil {
			return "", 0, "", err
		}
		if err = writeAdvertisedCompensation(ctx, tx, entityID, proposed.Compensation); err != nil {
			return "", 0, "", err
		}
		auditID, err = writeIngestedRecordAudit(ctx, tx, actor, "opportunity.source_create", "opportunity", entityID)
		if err != nil {
			return "", 0, "", err
		}
		revision = 1
	} else {
		entityID = mappedOpportunityID.String
		current, readErr := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, entityID))
		if readErr != nil || current.ArchivedAt != "" || current.CompanyID != input.CompanyID ||
			current.Revision != input.ExpectedRevision {
			return "", 0, "", ErrConflict
		}
		locks, lockErr := ownerSourceFieldLocksTx(ctx, tx, entityID)
		if lockErr != nil {
			return "", 0, "", lockErr
		}
		proposed.Stage, proposed.Notes = current.Stage, current.Notes
		if locks.title {
			proposed.Title = current.Title
		}
		if locks.kind {
			proposed.Kind = current.Kind
		}
		if locks.workPattern {
			proposed.WorkPattern = current.WorkPattern
		}
		if locks.location {
			proposed.LocationText = current.LocationText
		}
		if locks.posted {
			proposed.PostedOn = current.PostedOn
		}
		if locks.deadline {
			proposed.DeadlineOn = current.DeadlineOn
		}
		if locks.compensation {
			proposed.Compensation = current.Compensation
			// A span in old source text cannot attest a conversion in new text.
			proposed.Compensation.AnnualConversion = ""
			proposed.Compensation.AnnualConversionSpanStart = nil
			proposed.Compensation.AnnualConversionSpanEnd = nil
			proposed.Compensation.AnnualConversionExcerpt = ""
			proposed.Compensation.AnnualConversionSHA256 = ""
		}
		proposed, err = validateOpportunity(proposed)
		if err != nil {
			return "", 0, "", err
		}
		revision = current.Revision + 1
		now := recordNow()
		result, updateErr := tx.ExecContext(ctx, `UPDATE opportunities SET title=?,kind=?,source_url=?,original_text=?,
  work_pattern=?,location_text=?,posted_on=?,deadline_on=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, proposed.Title, proposed.Kind,
			optionalText(proposed.SourceURL), proposed.OriginalText, proposed.WorkPattern,
			proposed.LocationText, optionalText(proposed.PostedOn), optionalText(proposed.DeadlineOn),
			revision, now, entityID, current.Revision)
		if updateErr != nil {
			return "", 0, "", updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil || count != 1 {
			return "", 0, "", ErrConflict
		}
		if err = writeAdvertisedCompensation(ctx, tx, entityID, proposed.Compensation); err != nil {
			return "", 0, "", err
		}
		auditID, err = randomID()
		if err != nil {
			return "", 0, "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "opportunity.source_refresh", "opportunity",
			entityID, current.Revision, revision, now)
		if err != nil {
			return "", 0, "", err
		}
		// Historical assessments stay immutable. Only this role's current Jev
		// pointer is invalidated; a later commissioned assessment may replace it.
		if _, err = tx.ExecContext(ctx, `DELETE FROM organisation_current WHERE opportunity_id=?`, entityID); err != nil {
			return "", 0, "", err
		}
	}
	if err = insertSourcedSnapshotTx(ctx, tx, actor, ingestionID, input.SourceOpeningID,
		entityID, auditID, proposed.CompanyID, proposed.Kind, proposed.SourceURL, proposed.OriginalText); err != nil {
		return "", 0, "", err
	}
	return entityID, revision, auditID, nil
}
