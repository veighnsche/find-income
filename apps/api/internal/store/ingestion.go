package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const IngestionJobKind = "opportunity.ingest"

type IngestionInput struct {
	Origin         string // owner, agent or collector
	SourceURL      string
	OriginalText   string
	ConnectorID    string
	ExternalID     string
	DiscoveredAt   string
	IdempotencyKey string
}

type IngestionRequest struct {
	ID                string
	Origin            string
	Actor             Actor
	IdempotencyKey    string
	SubmissionSHA256  string
	SourceURL         string
	OriginalText      string
	ConnectorID       string
	ExternalID        string
	DiscoveredAt      string
	Status            string // pending, processing, completed, needs_text, failed
	JobID             string
	JobState          JobState
	AttemptsStarted   int64
	DispatchStarted   bool
	CodexThreadID     string
	CodexTurnID       string
	OpportunityID     string
	RecordChangeID    string
	SourceID          string
	SourceOpeningID   string
	OrganisationJobID string
	SafeErrorCode     string
	CreatedAt         string
	UpdatedAt         string
}

type IngestionPage struct {
	Items      []IngestionRequest
	NextCursor string
}

func validateIngestionInput(actor Actor, input *IngestionInput) error {
	input.SourceURL = strings.TrimSpace(input.SourceURL)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if !requiredActor(actor) || len(input.IdempotencyKey) < 1 || len(input.IdempotencyKey) > 200 ||
		!utf8.ValidString(input.OriginalText) || len(input.OriginalText) > 200000 ||
		(input.SourceURL == "" && strings.TrimSpace(input.OriginalText) == "") {
		return fmt.Errorf("%w: bounded source and idempotency key required", ErrInvalid)
	}
	if input.SourceURL != "" {
		if err := validateWebURL(input.SourceURL); err != nil {
			return err
		}
	}
	switch input.Origin {
	case "owner":
		if actor.Kind != "administrator" || input.ConnectorID != "" || input.ExternalID != "" || input.DiscoveredAt != "" {
			return fmt.Errorf("%w: owner submission fields", ErrInvalid)
		}
	case "agent":
		if actor.Kind != "agent" || input.ConnectorID != "" || input.ExternalID != "" || input.DiscoveredAt != "" {
			return fmt.Errorf("%w: agent submission fields", ErrInvalid)
		}
	case "collector":
		if actor.Kind != "system" || !boundedNonempty(input.ConnectorID, 80) ||
			!boundedNonempty(input.ExternalID, 300) ||
			(input.DiscoveredAt != "" && !validInstant(input.DiscoveredAt)) {
			return fmt.Errorf("%w: collector identity and source required", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown ingestion origin", ErrInvalid)
	}
	return nil
}

func ingestionDigest(input IngestionInput) string {
	encoded, _ := json.Marshal(struct {
		Origin, SourceURL, OriginalText, ConnectorID, ExternalID string
	}{input.Origin, input.SourceURL, input.OriginalText, input.ConnectorID, input.ExternalID})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func canonicalIngestionURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func ingestionIdentity(input IngestionInput) (string, string) {
	canonical := canonicalIngestionURL(input.SourceURL)
	if input.Origin == "collector" && input.ExternalID != "" {
		return "collector:" + input.ConnectorID + ":" + input.ExternalID, canonical
	}
	if canonical != "" {
		return "url:" + canonical, canonical
	}
	return "", ""
}

func validCollectorExternalID(value string) bool {
	if len(value) == 0 || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if char != '-' && char != '_' && (char < '0' || char > '9') &&
			(char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
			return false
		}
	}
	return true
}

type collectorSightingRef struct {
	AttemptID string
	Index     int
}

func insertSourceSighting(ctx context.Context, tx *sql.Tx, openingID, ingestionID string, input IngestionInput, digest, observedAt, recordedAt string, actor Actor, decision string, ref collectorSightingRef) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	var postingIndex any
	if ref.AttemptID != "" {
		postingIndex = ref.Index
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_sightings
  (id,source_opening_id,ingestion_id,source_url,original_text,content_sha256,
   observed_at,recorded_at,actor_kind,actor_id,decision,collector_attempt_id,posting_index)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, openingID, ingestionID, optionalText(input.SourceURL),
		input.OriginalText, digest, observedAt, recordedAt, actor.Kind, actor.ID, decision,
		optionalText(ref.AttemptID), postingIndex)
	return err
}

func insertIngestionJob(ctx context.Context, tx *sql.Tx, actor Actor, intakeID string, attempt int64, now time.Time) (string, error) {
	jobID, err := randomID()
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(struct {
		IngestionID string `json:"ingestionId"`
	}{intakeID})
	payloadHash := sha256.Sum256(payload)
	jobKey := fmt.Sprintf("ingestion:%s:%d", intakeID, attempt)
	fingerprintInput, _ := json.Marshal(struct {
		Kind        string          `json:"kind"`
		Payload     json.RawMessage `json:"payload"`
		MaxAttempts int             `json:"maxAttempts"`
		Scheduled   string          `json:"scheduled"`
	}{IngestionJobKind, payload, 3, ""})
	requestHash := sha256.Sum256(fingerprintInput)
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs
  (id,kind,payload_json,payload_sha256,actor_kind,actor_id,idempotency_key,request_sha256,
   state,max_attempts,available_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, IngestionJobKind, string(payload), hex.EncodeToString(payloadHash[:]),
		actor.Kind, actor.ID, jobKey, hex.EncodeToString(requestHash[:]), JobQueued, 3, jobTime(now), jobTime(now), jobTime(now))
	if err != nil {
		return "", err
	}
	if err := writeJobAudit(ctx, tx, actor, "job.enqueue", jobID, now); err != nil {
		return "", err
	}
	return jobID, nil
}

func writeIngestionAudit(ctx context.Context, tx *sql.Tx, actor Actor, operation, id string) error {
	auditID, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, operation, "ingestion", id, utcNow())
	return err
}

// SubmitIngestion persists the exact submitted source and the queue item in
// one transaction. Repeating the same actor/key/input returns the original ID.
func (s *Store) SubmitIngestion(ctx context.Context, actor Actor, input IngestionInput) (IngestionRequest, bool, error) {
	if err := validateIngestionInput(actor, &input); err != nil {
		return IngestionRequest{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestionRequest{}, false, err
	}
	defer tx.Rollback()
	item, created, err := submitIngestionTx(ctx, tx, actor, input, collectorSightingRef{})
	if err != nil {
		return IngestionRequest{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return IngestionRequest{}, false, err
	}
	return item, created, nil
}

// submitIngestionTx also serves atomically staged collector pages. The caller
// owns authorization and the encompassing transaction.
func submitIngestionTx(ctx context.Context, tx *sql.Tx, actor Actor, input IngestionInput, ref collectorSightingRef) (IngestionRequest, bool, error) {
	if err := validateIngestionInput(actor, &input); err != nil {
		return IngestionRequest{}, false, err
	}
	digest := ingestionDigest(input)
	id, err := randomID()
	if err != nil {
		return IngestionRequest{}, false, err
	}
	// Reserve the SQLite writer before the idempotency read across Store handles.
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE id=?`, id); err != nil {
		return IngestionRequest{}, false, err
	}
	var existingID, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,submission_sha256 FROM ingestion_requests
  WHERE actor_kind=? AND actor_id=? AND idempotency_key=?`, actor.Kind, actor.ID, input.IdempotencyKey).
		Scan(&existingID, &existingDigest)
	if err == nil {
		if existingDigest != digest {
			return IngestionRequest{}, false, ErrJobIdempotencyConflict
		}
		item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, existingID))
		return item, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IngestionRequest{}, false, err
	}
	now := time.Now().UTC()
	identity, canonical := ingestionIdentity(input)
	if input.Origin == "owner" && identity == "" && strings.TrimSpace(input.OriginalText) != "" {
		identity = "owner-paste:" + actor.ID + ":" + input.IdempotencyKey
	}
	contentSHA := sourceDigest(input.OriginalText)
	observedAt := jobTime(now)
	if input.DiscoveredAt != "" {
		observed, parseErr := time.Parse(time.RFC3339Nano, input.DiscoveredAt)
		if parseErr != nil {
			return IngestionRequest{}, false, ErrInvalid
		}
		observedAt = jobTime(observed)
	}
	var openingID, previousSHA, previousIngestionID, latestObserved string
	if identity != "" {
		err = tx.QueryRowContext(ctx, `SELECT id,current_sha256,current_ingestion_id,latest_observed_at
  FROM source_openings WHERE identity_key=?`, identity).Scan(&openingID, &previousSHA, &previousIngestionID, &latestObserved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return IngestionRequest{}, false, err
		}
		if err == nil && (previousSHA == contentSHA || observedAt < latestObserved) {
			decision := "unchanged"
			if observedAt < latestObserved && previousSHA != contentSHA {
				decision = "older"
			}
			if err = insertSourceSighting(ctx, tx, openingID, previousIngestionID, input, contentSHA, observedAt, jobTime(now), actor, decision, ref); err != nil {
				return IngestionRequest{}, false, err
			}
			if observedAt > latestObserved {
				if _, err = tx.ExecContext(ctx, `UPDATE source_openings SET latest_observed_at=?,updated_at=? WHERE id=?`, observedAt, jobTime(now), openingID); err != nil {
					return IngestionRequest{}, false, err
				}
			}
			item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, previousIngestionID))
			if err != nil {
				return IngestionRequest{}, false, err
			}
			return item, false, nil
		}
	}
	jobID, err := insertIngestionJob(ctx, tx, actor, id, 1, now)
	if err != nil {
		return IngestionRequest{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ingestion_requests
  (id,origin,actor_kind,actor_id,idempotency_key,submission_sha256,source_url,original_text,
   connector_id,external_id,discovered_at,status,job_id,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, input.Origin, actor.Kind, actor.ID, input.IdempotencyKey, digest,
		optionalText(input.SourceURL), input.OriginalText, optionalText(input.ConnectorID), optionalText(input.ExternalID),
		optionalText(input.DiscoveredAt), "pending", jobID, jobTime(now), jobTime(now))
	if err != nil {
		return IngestionRequest{}, false, err
	}
	if identity != "" {
		decision := "changed"
		if openingID == "" {
			decision = "new"
			openingID, err = randomID()
			if err != nil {
				return IngestionRequest{}, false, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO source_openings
  (id,identity_key,canonical_url,current_sha256,current_ingestion_id,latest_observed_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?)`, openingID, identity, optionalText(canonical), contentSHA, id, observedAt, jobTime(now), jobTime(now))
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE source_openings SET current_sha256=?,current_ingestion_id=?,
  latest_observed_at=?,updated_at=? WHERE id=?`, contentSHA, id, observedAt, jobTime(now), openingID)
		}
		if err != nil {
			return IngestionRequest{}, false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET source_opening_id=? WHERE id=?`, openingID, id); err != nil {
			return IngestionRequest{}, false, err
		}
		if err = insertSourceSighting(ctx, tx, openingID, id, input, contentSHA, observedAt, jobTime(now), actor, decision, ref); err != nil {
			return IngestionRequest{}, false, err
		}
	}
	if err = writeIngestionAudit(ctx, tx, actor, "ingestion.submit", id); err != nil {
		return IngestionRequest{}, false, err
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, id))
	if err != nil {
		return IngestionRequest{}, false, err
	}
	return item, true, nil
}

// publishCollectorBatchTx runs inside SaveRoundCollectorBatch's fenced writer
// transaction. A batch cannot advance its durable cursor unless all accepted
// postings have exact source identities and immutable sightings committed.
func publishCollectorBatchTx(ctx context.Context, tx *sql.Tx, actor Actor, roundID, attemptID string, payload json.RawMessage) error {
	var batch struct {
		Postings []struct {
			Provider      string `json:"provider"`
			BoardID       string `json:"boardId"`
			ExternalID    string `json:"externalId"`
			SourceURL     string `json:"sourceUrl"`
			OriginalText  []byte `json:"originalText"`
			ContentSHA256 string `json:"contentSha256"`
			ObservedAt    string `json:"observedAt"`
		} `json:"postings"`
	}
	if err := json.Unmarshal(payload, &batch); err != nil {
		return ErrInvalid
	}
	if len(batch.Postings) == 0 {
		return nil
	}
	var boardID, site, region, provider string
	err := tx.QueryRowContext(ctx, `SELECT a.resource_id,b.provider,b.site,b.region FROM round_attempts a
  JOIN collector_boards b ON a.resource_id='board:'||b.id WHERE a.id=? AND a.round_id=?`,
		attemptID, roundID).Scan(&boardID, &provider, &site, &region)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	boardID = strings.TrimPrefix(boardID, "board:")
	if provider != "lever" || (region != "global" && region != "eu") || !requiredActor(actor) {
		return ErrInvalid
	}
	collectorActor := Actor{Kind: "system", ID: "collector:" + boardID}
	for index, posting := range batch.Postings {
		if posting.Provider != "lever" || posting.BoardID != boardID || !validCollectorExternalID(posting.ExternalID) ||
			len(posting.OriginalText) == 0 || len(posting.OriginalText) > 200000 || !json.Valid(posting.OriginalText) ||
			!validInstant(posting.ObservedAt) || sourceDigest(string(posting.OriginalText)) != posting.ContentSHA256 {
			return ErrInvalid
		}
		var raw struct {
			ID               string `json:"id"`
			Text             string `json:"text"`
			HostedURL        string `json:"hostedUrl"`
			DescriptionPlain string `json:"descriptionPlain"`
			OpeningPlain     string `json:"openingPlain"`
		}
		if err := json.Unmarshal(posting.OriginalText, &raw); err != nil ||
			raw.ID != posting.ExternalID || raw.HostedURL != posting.SourceURL ||
			strings.TrimSpace(raw.Text) == "" ||
			(strings.TrimSpace(raw.DescriptionPlain) == "" && strings.TrimSpace(raw.OpeningPlain) == "") {
			return ErrInvalid
		}
		u, err := url.Parse(posting.SourceURL)
		expectedHost := "jobs.lever.co"
		if region == "eu" {
			expectedHost = "jobs.eu.lever.co"
		}
		if err != nil || u.Scheme != "https" || u.Host != expectedHost || u.User != nil ||
			u.Path != "/"+site+"/"+raw.ID || u.RawQuery != "" || u.Fragment != "" {
			return ErrInvalid
		}
		input := IngestionInput{Origin: "collector", SourceURL: posting.SourceURL,
			OriginalText: string(posting.OriginalText), ConnectorID: "lever:" + boardID,
			ExternalID: posting.ExternalID, DiscoveredAt: posting.ObservedAt,
			IdempotencyKey: fmt.Sprintf("round:%s:%d", attemptID, index)}
		item, created, err := submitIngestionTx(ctx, tx, collectorActor, input, collectorSightingRef{AttemptID: attemptID, Index: index})
		if err != nil {
			return err
		}
		if created {
			var opportunityID sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT opportunity_id FROM source_openings WHERE id=?`, item.SourceOpeningID).Scan(&opportunityID); err != nil {
				return err
			}
			if opportunityID.Valid {
				// The role still shows its last extracted source until a current
				// round saves the revision. Its old assessments cannot stay current.
				if _, err := tx.ExecContext(ctx, `DELETE FROM organisation_current WHERE opportunity_id=?`, opportunityID.String); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM qualification_current WHERE opportunity_id=?`, opportunityID.String); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (?,?, 'source.revision_observed') ON CONFLICT(opportunity_id) DO UPDATE SET
  requested_at=excluded.requested_at,reason=excluded.reason`, opportunityID.String, utcNow()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

const ingestionColumns = `i.id,i.origin,i.actor_kind,i.actor_id,i.idempotency_key,i.submission_sha256,
  i.source_url,i.original_text,i.connector_id,i.external_id,i.discovered_at,i.status,i.job_id,
  i.attempts_started,i.dispatch_started,i.codex_thread_id,i.codex_turn_id,i.opportunity_id,i.record_change_id,i.source_id,i.organisation_job_id,
  i.safe_error_code,i.created_at,i.updated_at,j.state,j.last_error_code,i.source_opening_id`

func scanIngestion(row rowScanner) (IngestionRequest, error) {
	var item IngestionRequest
	var sourceURL, connectorID, externalID, discoveredAt, threadID, turnID, opportunityID, changeID, sourceID, organisationJobID, errorCode, jobError, sourceOpeningID sql.NullString
	err := row.Scan(&item.ID, &item.Origin, &item.Actor.Kind, &item.Actor.ID, &item.IdempotencyKey, &item.SubmissionSHA256,
		&sourceURL, &item.OriginalText, &connectorID, &externalID, &discoveredAt, &item.Status, &item.JobID,
		&item.AttemptsStarted, &item.DispatchStarted, &threadID, &turnID, &opportunityID, &changeID, &sourceID, &organisationJobID, &errorCode, &item.CreatedAt, &item.UpdatedAt,
		&item.JobState, &jobError, &sourceOpeningID)
	if err != nil {
		return IngestionRequest{}, err
	}
	item.SourceURL, item.ConnectorID, item.ExternalID, item.DiscoveredAt = sourceURL.String, connectorID.String, externalID.String, discoveredAt.String
	item.CodexThreadID, item.CodexTurnID, item.OpportunityID, item.RecordChangeID = threadID.String, turnID.String, opportunityID.String, changeID.String
	item.SourceID = sourceID.String
	item.SourceOpeningID = sourceOpeningID.String
	item.OrganisationJobID = organisationJobID.String
	if item.Status != "completed" && item.Status != "needs_text" {
		switch item.JobState {
		case JobRunning:
			item.Status = "processing"
		case JobFailed, JobCancelled:
			item.Status = "failed"
		default:
			item.Status = "pending"
		}
	}
	item.SafeErrorCode = errorCode.String
	if item.Status == "failed" && jobError.Valid {
		item.SafeErrorCode = jobError.String
	}
	return item, nil
}

func (s *Store) Ingestion(ctx context.Context, id string) (IngestionRequest, error) {
	if id == "" {
		return IngestionRequest{}, ErrInvalid
	}
	item, err := scanIngestion(s.db.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionRequest{}, ErrNotFound
	}
	return item, err
}

func (s *Store) ListIngestions(ctx context.Context, cursorValue string, requestedLimit int) (IngestionPage, error) {
	limit, err := boundedListLimit(requestedLimit)
	if err != nil {
		return IngestionPage{}, err
	}
	scope := recordListScope("ingestions")
	cursor, err := decodeEvidenceCursor(cursorValue, scope)
	if err != nil {
		return IngestionPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id
  WHERE i.created_at>? OR (i.created_at=? AND i.id>?)
  ORDER BY i.created_at,i.id LIMIT ?`, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return IngestionPage{}, err
	}
	defer rows.Close()
	page := IngestionPage{Items: make([]IngestionRequest, 0, limit)}
	for rows.Next() {
		item, err := scanIngestion(rows)
		if err != nil {
			return IngestionPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return IngestionPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeEvidenceCursor(evidenceCursor{Version: 1, CreatedAt: last.CreatedAt, ID: last.ID, Scope: scope})
	}
	return page, nil
}

// RetryIngestion starts a new job for an existing terminal request. A URL-only
// request may gain pasted full text after needs_text. If record persistence
// succeeded but later processing failed, the saved mapping remains intact.
func (s *Store) RetryIngestion(ctx context.Context, actor Actor, id string, fullText *string) (IngestionRequest, error) {
	if id == "" || !requiredActor(actor) {
		return IngestionRequest{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestionRequest{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE id=?`, id); err != nil {
		return IngestionRequest{}, err
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionRequest{}, ErrNotFound
	}
	if err != nil {
		return IngestionRequest{}, err
	}
	if actor != item.Actor && actor.Kind != "administrator" {
		return IngestionRequest{}, ErrInvalid
	}
	// A remote dispatch may still exist when its local job is terminal. Only a
	// correlated terminal observation permits another attempt; its identity is
	// retained in ingestion_dispatch_history before the current slot is reset.
	if item.DispatchStarted {
		var terminal string
		err = tx.QueryRowContext(ctx, `SELECT terminal_status FROM ingestion_dispatch_history
  WHERE ingestion_id=? AND job_id=? AND codex_thread_id=? AND codex_turn_id=?`,
			id, item.JobID, item.CodexThreadID, item.CodexTurnID).Scan(&terminal)
		if errors.Is(err, sql.ErrNoRows) {
			return IngestionRequest{}, ErrUncertain
		}
		if err != nil {
			return IngestionRequest{}, err
		}
	}
	partialSave := item.JobState == JobFailed &&
		item.OpportunityID != "" && item.RecordChangeID != "" && item.SourceID != ""
	if !partialSave && item.Status != "failed" && item.Status != "needs_text" ||
		!partialSave && item.JobState != JobFailed && item.JobState != JobSucceeded && item.JobState != JobCancelled {
		return IngestionRequest{}, ErrConflict
	}
	text := item.OriginalText
	if fullText != nil {
		if partialSave || item.SourceURL == "" || item.OriginalText != "" && item.OriginalText != *fullText ||
			!boundedNonempty(*fullText, 200000) {
			return IngestionRequest{}, ErrInvalid
		}
		text = *fullText
	}
	if item.Status == "needs_text" && strings.TrimSpace(text) == "" {
		return IngestionRequest{}, ErrInvalid
	}
	now := time.Now().UTC()
	jobID, err := insertIngestionJob(ctx, tx, item.Actor, id, item.AttemptsStarted+1, now)
	if err != nil {
		return IngestionRequest{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET original_text=?,status='pending',job_id=?,
  attempts_started=attempts_started+1,dispatch_started=0,codex_thread_id=NULL,codex_turn_id=NULL,
  dispatch_terminal_status=NULL,
  safe_error_code=NULL,updated_at=? WHERE id=?`, text, jobID, jobTime(now), id)
	if err != nil {
		return IngestionRequest{}, err
	}
	if err = writeIngestionAudit(ctx, tx, actor, "ingestion.retry", id); err != nil {
		return IngestionRequest{}, err
	}
	item, err = scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, id))
	if err != nil {
		return IngestionRequest{}, err
	}
	if err = tx.Commit(); err != nil {
		return IngestionRequest{}, err
	}
	return item, nil
}

// A running job may bind dispatch identifiers only while its lease is active.
// Repeated binds with the same IDs are idempotent; different IDs conflict.
func (s *Store) BeginIngestionDispatch(ctx context.Context, claim Job) error {
	if claim.ID == "" || claim.LeaseToken == "" {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET dispatch_started=1,status='processing',updated_at=?
  WHERE job_id=? AND dispatch_started=0 AND status='pending'
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		utcNow(), claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) BindIngestionThread(ctx context.Context, claim Job, threadID string) error {
	if claim.ID == "" || claim.LeaseToken == "" || !boundedNonempty(threadID, 200) {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET codex_thread_id=?,status='processing',updated_at=?
  WHERE job_id=? AND status='processing' AND dispatch_started=1
    AND (codex_thread_id IS NULL OR codex_thread_id=?)
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		threadID, utcNow(), claim.ID, threadID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) BindIngestionTurn(ctx context.Context, claim Job, threadID, turnID string) error {
	if claim.ID == "" || claim.LeaseToken == "" || !boundedNonempty(threadID, 200) || !boundedNonempty(turnID, 200) {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET codex_turn_id=?,status='processing',updated_at=?
  WHERE job_id=? AND status='processing' AND dispatch_started=1 AND codex_thread_id=?
    AND (codex_turn_id IS NULL OR codex_turn_id=?)
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		turnID, utcNow(), claim.ID, threadID, turnID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

// MarkIngestionNeedsText records an honest terminal intake outcome when a URL
// cannot yield full vacancy text. The worker should then complete its job.
func (s *Store) MarkIngestionNeedsText(ctx context.Context, claim Job) error {
	if claim.ID == "" || claim.LeaseToken == "" {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET status='needs_text',safe_error_code='needs_text',updated_at=?
  WHERE job_id=? AND source_url IS NOT NULL AND length(trim(original_text))=0
    AND status IN ('pending','processing')
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		utcNow(), claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

// AttachIngestionText persists a fetched vacancy body for a URL-only intake.
// A pasted body is immutable, and a repeated identical fetch is harmless.
func (s *Store) AttachIngestionText(ctx context.Context, claim Job, fullText string) error {
	if claim.ID == "" || claim.LeaseToken == "" || !boundedNonempty(fullText, 200000) {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET original_text=?,updated_at=?
  WHERE job_id=? AND source_url IS NOT NULL AND status IN ('pending','processing')
    AND (original_text='' OR original_text=?)
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		fullText, utcNow(), claim.ID, fullText, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

// RecordIngestionResult links a pre-existing opportunity change whose captured
// source matches the request. New records should use SaveIngestionOpportunity
// so record creation and intake mapping are atomic.
func (s *Store) RecordIngestionResult(ctx context.Context, claim Job, opportunityID, recordChangeID string) error {
	if claim.ID == "" || claim.LeaseToken == "" || opportunityID == "" || recordChangeID == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE job_id=?`, claim.ID); err != nil {
		return err
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.job_id=?`, claim.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := recordIngestionResultTx(ctx, tx, claim, item, opportunityID, recordChangeID); err != nil {
		return err
	}
	return tx.Commit()
}

func recordIngestionResultTx(ctx context.Context, tx *sql.Tx, claim Job, item IngestionRequest, opportunityID, recordChangeID string) error {
	var err error
	if item.OpportunityID != "" {
		if item.OpportunityID == opportunityID && item.RecordChangeID == recordChangeID {
			return nil
		}
		return ErrConflict
	}
	if item.Status == "needs_text" {
		return ErrConflict
	}
	var snapshotJSON string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM record_changes
  WHERE audit_id=? AND entity_kind='opportunity' AND entity_id=? AND snapshot_state='captured'`,
		recordChangeID, opportunityID).Scan(&snapshotJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	var snapshot struct {
		CompanyID    string `json:"companyId"`
		Kind         string `json:"kind"`
		SourceURL    string `json:"sourceUrl"`
		OriginalText string `json:"originalText"`
	}
	if err = json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return err
	}
	if strings.TrimSpace(snapshot.OriginalText) == "" ||
		item.SourceURL != "" && snapshot.SourceURL != item.SourceURL ||
		item.OriginalText != "" && snapshot.OriginalText != item.OriginalText {
		return ErrInvalid
	}
	var companyID, opportunityKind, currentURL, currentText string
	var contextVersion int64
	err = tx.QueryRowContext(ctx, `SELECT o.company_id,o.kind,COALESCE(o.source_url,''),o.original_text,
  v.context_version FROM opportunities o JOIN qualification_input_versions v ON v.opportunity_id=o.id
  WHERE o.id=? AND o.archived_at IS NULL`, opportunityID).Scan(&companyID, &opportunityKind, &currentURL, &currentText, &contextVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if currentURL != snapshot.SourceURL || currentText != snapshot.OriginalText ||
		companyID != snapshot.CompanyID || opportunityKind != snapshot.Kind {
		return ErrConflict
	}
	var sourceID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM evidence_sources WHERE record_change_audit_id=? AND
  opportunity_id=? AND context_version=?`, recordChangeID, opportunityID, contextVersion).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		sourceID, err = randomID()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO evidence_sources
  (id,opportunity_id,company_id,opportunity_kind,context_version,source_kind,
   record_change_audit_id,source_url,original_text,content_sha256,recorded_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,'vacancy_snapshot',?,?,?,?,?,?,?)`, sourceID, opportunityID, companyID,
			opportunityKind, contextVersion, recordChangeID, optionalText(snapshot.SourceURL),
			snapshot.OriginalText, sourceDigest(snapshot.OriginalText), utcNow(), item.Actor.Kind, item.Actor.ID)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_requests SET status='processing',original_text=?,opportunity_id=?,
  record_change_id=?,source_id=?,safe_error_code=NULL,updated_at=? WHERE id=? AND job_id=?
  AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
    AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		snapshot.OriginalText, opportunityID, recordChangeID, sourceID, utcNow(), item.ID, claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	if err = writeIngestionAudit(ctx, tx, item.Actor, "ingestion.complete", item.ID); err != nil {
		return err
	}
	if err = maybeScheduleOrganisationTx(ctx, tx, item.ID, opportunityID, companyID, opportunityKind,
		currentURL, currentText, sourceID); err != nil {
		return err
	}
	if item.SourceOpeningID != "" {
		if err = bindSourceOpeningTx(ctx, tx, item, opportunityID); err != nil {
			return err
		}
	}
	return nil
}

func bindSourceOpeningTx(ctx context.Context, tx *sql.Tx, item IngestionRequest, opportunityID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE source_openings SET opportunity_id=?,updated_at=?
  WHERE id=? AND current_ingestion_id=? AND (opportunity_id IS NULL OR opportunity_id=?)`,
		opportunityID, utcNow(), item.SourceOpeningID, item.ID, opportunityID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

type IngestionRecordInput struct {
	ExistingCompanyID string
	NewCompany        *CompanyInput
	Opportunity       OpportunityInput
}

func writeIngestedRecordAudit(ctx context.Context, tx *sql.Tx, actor Actor, operation, kind, id string) (string, error) {
	auditID, err := randomID()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,1,?)`, auditID, actor.Kind, actor.ID, operation, kind, id, utcNow())
	return auditID, err
}

// A source revision changes the vacancy snapshot on the mapped record while
// retaining its owner-edited fields, stage, notes, evidence and application
// history. The subsequent extraction may add claims from the new snapshot.
func (s *Store) refreshIngestionOpportunityTx(ctx context.Context, tx *sql.Tx, claim Job, item IngestionRequest, opportunityID string) (Opportunity, string, error) {
	current, err := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) || current.ArchivedAt != "" {
		return Opportunity{}, "", ErrConflict
	}
	if err != nil {
		return Opportunity{}, "", err
	}
	if current.SourceURL == item.SourceURL && current.OriginalText == item.OriginalText {
		match, found, err := findMappedOpportunitySource(ctx, tx, opportunityID, item.SourceURL, item.OriginalText)
		if err != nil || !found || match.OpportunityID != opportunityID {
			return Opportunity{}, "", ErrConflict
		}
		if err = recordIngestionResultTx(ctx, tx, claim, item, opportunityID, match.RecordChangeID); err != nil {
			return Opportunity{}, "", err
		}
		if err = tx.Commit(); err != nil {
			return Opportunity{}, "", err
		}
		return current, match.RecordChangeID, nil
	}
	updatedAt := recordNow()
	_, err = tx.ExecContext(ctx, `UPDATE opportunities SET source_url=?,original_text=?,revision=revision+1,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, optionalText(item.SourceURL), item.OriginalText,
		updatedAt, opportunityID, current.Revision)
	if err != nil {
		return Opportunity{}, "", err
	}
	changeID, err := randomID()
	if err != nil {
		return Opportunity{}, "", err
	}
	actor := Actor{Kind: "system", ID: "ingestion:" + item.ID}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,?,?,?)`, changeID, actor.Kind, actor.ID, "opportunity.source_refresh", "opportunity",
		opportunityID, current.Revision, current.Revision+1, updatedAt)
	if err != nil {
		return Opportunity{}, "", err
	}
	if err = recordIngestionResultTx(ctx, tx, claim, item, opportunityID, changeID); err != nil {
		return Opportunity{}, "", err
	}
	updated, err := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, opportunityID))
	if err != nil {
		return Opportunity{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return Opportunity{}, "", err
	}
	return updated, changeID, nil
}

// SaveIngestionOpportunity is the trusted record-writing bridge for one
// extracted vacancy. Company/opportunity creation, source snapshot and intake
// mapping commit together; a crash before commit cannot orphan a duplicate.
// The submitted/fetched source is forced onto the opportunity and stage is
// discovered, regardless of model-provided values.
func (s *Store) SaveIngestionOpportunity(ctx context.Context, claim Job, input IngestionRecordInput) (Opportunity, string, error) {
	if claim.ID == "" || claim.LeaseToken == "" || (input.ExistingCompanyID == "") == (input.NewCompany == nil) {
		return Opportunity{}, "", ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Opportunity{}, "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE job_id=?`, claim.ID); err != nil {
		return Opportunity{}, "", err
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.job_id=?`, claim.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return Opportunity{}, "", ErrNotFound
	}
	if err != nil {
		return Opportunity{}, "", err
	}
	if item.OpportunityID != "" {
		record, err := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, item.OpportunityID))
		return record, item.RecordChangeID, err
	}
	if item.Status == "needs_text" || strings.TrimSpace(item.OriginalText) == "" {
		return Opportunity{}, "", ErrConflict
	}
	var live int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id=? AND state='running' AND lease_token=?
  AND attempt_count=? AND lease_until>?`, claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return Opportunity{}, "", ErrConflict
	}
	if err != nil {
		return Opportunity{}, "", err
	}
	if item.SourceOpeningID != "" {
		var mappedID, currentIngestionID sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT opportunity_id,current_ingestion_id FROM source_openings WHERE id=?`, item.SourceOpeningID).
			Scan(&mappedID, &currentIngestionID)
		if err != nil {
			return Opportunity{}, "", err
		}
		if currentIngestionID.String != item.ID {
			return Opportunity{}, "", ErrConflict
		}
		if mappedID.Valid {
			return s.refreshIngestionOpportunityTx(ctx, tx, claim, item, mappedID.String)
		}
	}
	var match IngestionOpportunityMatch
	var found bool
	if item.SourceOpeningID == "" {
		match, found, err = findMatchingOpportunity(ctx, tx, item.SourceURL, item.OriginalText)
		if err != nil {
			return Opportunity{}, "", err
		}
	}
	if found {
		if err := recordIngestionResultTx(ctx, tx, claim, item, match.OpportunityID, match.RecordChangeID); err != nil {
			return Opportunity{}, "", err
		}
		record, err := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, match.OpportunityID))
		if err != nil {
			return Opportunity{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return Opportunity{}, "", err
		}
		return record, match.RecordChangeID, nil
	}
	actor := Actor{Kind: "system", ID: "ingestion:" + item.ID}
	companyID := input.ExistingCompanyID
	if input.NewCompany != nil {
		companyInput := *input.NewCompany
		companyInput.Notes = ""
		companyInput, err = validateCompany(companyInput)
		if err != nil {
			return Opportunity{}, "", err
		}
		companyID, err = randomID()
		if err != nil {
			return Opportunity{}, "", err
		}
		now := recordNow()
		_, err = tx.ExecContext(ctx, `INSERT INTO companies
  (id,name,website,notes,revision,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
			companyID, companyInput.Name, optionalText(companyInput.Website), "", now, now)
		if err != nil {
			return Opportunity{}, "", err
		}
		if _, err = writeIngestedRecordAudit(ctx, tx, actor, "company.create", "company", companyID); err != nil {
			return Opportunity{}, "", err
		}
	} else {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM companies WHERE id=? AND archived_at IS NULL`, companyID).Scan(&live)
		if errors.Is(err, sql.ErrNoRows) {
			return Opportunity{}, "", ErrInvalid
		}
		if err != nil {
			return Opportunity{}, "", err
		}
	}
	opportunityInput := input.Opportunity
	opportunityInput.CompanyID = companyID
	opportunityInput.SourceURL = item.SourceURL
	opportunityInput.OriginalText = item.OriginalText
	opportunityInput.Stage = "discovered"
	opportunityInput.Notes = ""
	opportunityInput, err = validateOpportunity(opportunityInput)
	if err != nil {
		return Opportunity{}, "", err
	}
	opportunityID, err := randomID()
	if err != nil {
		return Opportunity{}, "", err
	}
	now := recordNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,source_url,original_text,notes,stage,work_pattern,location_text,
   posted_on,deadline_on,revision,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, opportunityID, companyID, opportunityInput.Title,
		opportunityInput.Kind, optionalText(opportunityInput.SourceURL), opportunityInput.OriginalText, "",
		opportunityInput.Stage, opportunityInput.WorkPattern, opportunityInput.LocationText,
		optionalText(opportunityInput.PostedOn), optionalText(opportunityInput.DeadlineOn), now, now)
	if err != nil {
		return Opportunity{}, "", err
	}
	if err = writeAdvertisedCompensation(ctx, tx, opportunityID, opportunityInput.Compensation); err != nil {
		return Opportunity{}, "", err
	}
	changeID, err := writeIngestedRecordAudit(ctx, tx, actor, "opportunity.create", "opportunity", opportunityID)
	if err != nil {
		return Opportunity{}, "", err
	}
	var contextVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT context_version FROM qualification_input_versions WHERE opportunity_id=?`, opportunityID).Scan(&contextVersion); err != nil {
		return Opportunity{}, "", err
	}
	sourceID, err := randomID()
	if err != nil {
		return Opportunity{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO evidence_sources
  (id,opportunity_id,company_id,opportunity_kind,context_version,source_kind,
   record_change_audit_id,source_url,original_text,content_sha256,recorded_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,'vacancy_snapshot',?,?,?,?,?,?,?)`, sourceID, opportunityID, companyID,
		opportunityInput.Kind, contextVersion, changeID, optionalText(opportunityInput.SourceURL),
		opportunityInput.OriginalText, sourceDigest(opportunityInput.OriginalText), utcNow(), actor.Kind, actor.ID)
	if err != nil {
		return Opportunity{}, "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_requests SET status='processing',opportunity_id=?,
  record_change_id=?,source_id=?,safe_error_code=NULL,updated_at=? WHERE id=? AND job_id=? AND opportunity_id IS NULL
  AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
    AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		opportunityID, changeID, sourceID, utcNow(), item.ID, claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return Opportunity{}, "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Opportunity{}, "", err
	}
	if count != 1 {
		return Opportunity{}, "", ErrConflict
	}
	if err = writeIngestionAudit(ctx, tx, actor, "ingestion.complete", item.ID); err != nil {
		return Opportunity{}, "", err
	}
	if err = maybeScheduleOrganisationTx(ctx, tx, item.ID, opportunityID, companyID, opportunityInput.Kind,
		opportunityInput.SourceURL, opportunityInput.OriginalText, sourceID); err != nil {
		return Opportunity{}, "", err
	}
	if item.SourceOpeningID != "" {
		if err = bindSourceOpeningTx(ctx, tx, item, opportunityID); err != nil {
			return Opportunity{}, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return Opportunity{}, "", err
	}
	record := Opportunity{ID: opportunityID, CompanyID: companyID, Title: opportunityInput.Title,
		Kind: opportunityInput.Kind, SourceURL: opportunityInput.SourceURL, OriginalText: opportunityInput.OriginalText,
		Stage: opportunityInput.Stage, WorkPattern: opportunityInput.WorkPattern, LocationText: opportunityInput.LocationText,
		PostedOn: opportunityInput.PostedOn, DeadlineOn: opportunityInput.DeadlineOn, Revision: 1,
		CreatedAt: now, UpdatedAt: now, Compensation: opportunityInput.Compensation}
	return record, changeID, nil
}

// CompleteIngestionProcessing records that the active Codex turn finished its
// work after a sourced record was saved. The worker still settles the leased
// job separately; a saved opportunity alone never proves processing success.
func (s *Store) CompleteIngestionProcessing(ctx context.Context, claim Job) error {
	if claim.Kind != IngestionJobKind || claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ingestion_requests SET status='completed',safe_error_code=NULL,updated_at=?
  WHERE job_id=? AND opportunity_id IS NOT NULL AND record_change_id IS NOT NULL
    AND source_id IS NOT NULL AND status IN ('pending','processing','completed')
    AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=ingestion_requests.job_id
      AND j.kind=? AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?)`,
		utcNow(), claim.ID, IngestionJobKind, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
