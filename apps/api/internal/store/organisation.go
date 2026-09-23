package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const OrganisationJobKind = "organisation.evaluate"

type OrganisationCategory struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type OrganisationCategorySet struct {
	Version    int64
	Categories []OrganisationCategory
	CreatedAt  string
	Actor      Actor
}

func validateOrganisationCategories(categories []OrganisationCategory) error {
	if len(categories) > 64 {
		return fmt.Errorf("%w: too many categories", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, category := range categories {
		if !boundedNonempty(category.ID, 80) || !boundedNonempty(category.Description, 1000) ||
			strings.TrimSpace(category.ID) != category.ID || strings.TrimSpace(category.Description) != category.Description ||
			category.ID == "__uncertain__" || seen[category.ID] {
			return fmt.Errorf("%w: invalid category definition", ErrInvalid)
		}
		seen[category.ID] = true
	}
	return nil
}

func scanOrganisationCategorySet(row rowScanner) (OrganisationCategorySet, error) {
	var set OrganisationCategorySet
	var definitions string
	err := row.Scan(&set.Version, &definitions, &set.CreatedAt, &set.Actor.Kind, &set.Actor.ID)
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	if err = json.Unmarshal([]byte(definitions), &set.Categories); err != nil {
		return OrganisationCategorySet{}, err
	}
	return set, nil
}

func (s *Store) CurrentOrganisationCategories(ctx context.Context) (OrganisationCategorySet, error) {
	set, err := scanOrganisationCategorySet(s.db.QueryRowContext(ctx, `SELECT v.version,v.categories_json,v.created_at,v.actor_kind,v.actor_id
  FROM organisation_category_versions v JOIN organisation_categories_current c ON c.version=v.version
  WHERE c.singleton=1`))
	if errors.Is(err, sql.ErrNoRows) {
		return OrganisationCategorySet{}, nil
	}
	return set, err
}

// UpdateOrganisationCategories stores the owner's current caller-defined boxes.
// Version zero creates the first set. An empty set disables new Jev jobs.
func (s *Store) UpdateOrganisationCategories(ctx context.Context, expectedVersion int64, next []OrganisationCategory, actor Actor) (OrganisationCategorySet, error) {
	if actor.Kind != "administrator" || expectedVersion < 0 {
		return OrganisationCategorySet{}, ErrInvalid
	}
	if err := validateOrganisationCategories(next); err != nil {
		return OrganisationCategorySet{}, err
	}
	if next == nil {
		next = []OrganisationCategory{}
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE organisation_categories_current SET version=version WHERE singleton=1`); err != nil {
		return OrganisationCategorySet{}, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM organisation_categories_current WHERE singleton=1`).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return OrganisationCategorySet{}, err
	}
	if current != expectedVersion {
		return OrganisationCategorySet{}, ErrConflict
	}
	set := OrganisationCategorySet{Version: expectedVersion + 1, Categories: next, CreatedAt: utcNow(), Actor: actor}
	_, err = tx.ExecContext(ctx, `INSERT INTO organisation_category_versions
  (version,categories_json,created_at,actor_kind,actor_id) VALUES (?,?,?,?,?)`,
		set.Version, string(encoded), set.CreatedAt, actor.Kind, actor.ID)
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO organisation_categories_current(singleton,version) VALUES (1,?)
  ON CONFLICT(singleton) DO UPDATE SET version=excluded.version`, set.Version)
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	if len(next) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT i.id,o.id,o.company_id,o.kind,COALESCE(o.source_url,''),
  o.original_text,i.source_id FROM ingestion_requests i JOIN opportunities o ON o.id=i.opportunity_id
  WHERE i.status='completed' AND i.source_id IS NOT NULL AND o.archived_at IS NULL
    AND i.id=(SELECT i2.id FROM ingestion_requests i2 WHERE i2.opportunity_id=o.id
      AND i2.status='completed' AND i2.source_id IS NOT NULL
      ORDER BY i2.created_at DESC,i2.id DESC LIMIT 1)`)
		if err != nil {
			return OrganisationCategorySet{}, err
		}
		type candidate struct{ ingestionID, opportunityID, companyID, kind, url, text, sourceID string }
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.ingestionID, &c.opportunityID, &c.companyID, &c.kind, &c.url, &c.text, &c.sourceID); err != nil {
				rows.Close()
				return OrganisationCategorySet{}, err
			}
			candidates = append(candidates, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return OrganisationCategorySet{}, err
		}
		for _, candidate := range candidates {
			fingerprint := organisationSourceFingerprint(candidate.companyID, candidate.kind, candidate.url, candidate.text, candidate.sourceID)
			if _, err := scheduleOrganisationTx(ctx, tx, candidate.ingestionID, candidate.opportunityID, set.Version, fingerprint); err != nil {
				return OrganisationCategorySet{}, err
			}
		}
	}
	auditID, err := randomID()
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "organisation.categories.update", "organisation_categories", "current", utcNow())
	if err != nil {
		return OrganisationCategorySet{}, err
	}
	if err = tx.Commit(); err != nil {
		return OrganisationCategorySet{}, err
	}
	return set, nil
}

func organisationSourceFingerprint(companyID, kind, url, text, sourceID string) string {
	encoded, _ := json.Marshal(struct{ CompanyID, Kind, URL, Text, SourceID string }{companyID, kind, url, text, sourceID})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type organisationJobPayload struct {
	IngestionID       string `json:"ingestionId"`
	OpportunityID     string `json:"opportunityId"`
	CategoryVersion   int64  `json:"categoryVersion"`
	SourceFingerprint string `json:"sourceFingerprint"`
}

func scheduleOrganisationTx(ctx context.Context, tx *sql.Tx, ingestionID, opportunityID string, categoryVersion int64, fingerprint string) (string, error) {
	jobID, err := randomID()
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(organisationJobPayload{ingestionID, opportunityID, categoryVersion, fingerprint})
	payloadHash := sha256.Sum256(payload)
	key := fmt.Sprintf("organisation:%s:%d:%s", opportunityID, categoryVersion, fingerprint)
	requestInput, _ := json.Marshal(struct {
		Kind        string          `json:"kind"`
		Payload     json.RawMessage `json:"payload"`
		MaxAttempts int             `json:"maxAttempts"`
		Scheduled   string          `json:"scheduled"`
	}{OrganisationJobKind, payload, 3, ""})
	requestHash := sha256.Sum256(requestInput)
	actor := Actor{Kind: "system", ID: "organisation"}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs
  (id,kind,payload_json,payload_sha256,actor_kind,actor_id,idempotency_key,request_sha256,
   state,max_attempts,available_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, OrganisationJobKind, string(payload), hex.EncodeToString(payloadHash[:]),
		actor.Kind, actor.ID, key, hex.EncodeToString(requestHash[:]), JobQueued, 3, jobTime(now), jobTime(now), jobTime(now))
	if err != nil {
		return "", err
	}
	if err = writeJobAudit(ctx, tx, actor, "job.enqueue", jobID, now); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET organisation_job_id=?,updated_at=? WHERE id=?`, jobID, utcNow(), ingestionID)
	return jobID, err
}

func maybeScheduleOrganisationTx(ctx context.Context, tx *sql.Tx, ingestionID, opportunityID, companyID, kind, url, text, sourceID string) error {
	set, err := scanOrganisationCategorySet(tx.QueryRowContext(ctx, `SELECT v.version,v.categories_json,v.created_at,v.actor_kind,v.actor_id
  FROM organisation_category_versions v JOIN organisation_categories_current c ON c.version=v.version
  WHERE c.singleton=1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(set.Categories) == 0 {
		return nil
	}
	_, err = scheduleOrganisationTx(ctx, tx, ingestionID, opportunityID, set.Version,
		organisationSourceFingerprint(companyID, kind, url, text, sourceID))
	return err
}
