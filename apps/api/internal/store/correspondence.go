package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type CorrespondenceAccount struct {
	ID                string `json:"id"`
	Provider          string `json:"provider"`
	ExternalAccountID string `json:"externalAccountId"`
	DisplayName       string `json:"displayName"`
	Status            string `json:"status"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type CorrespondenceAccountInput struct {
	Provider          string `json:"provider"`
	ExternalAccountID string `json:"externalAccountId"`
	DisplayName       string `json:"displayName"`
}

type CorrespondenceThread struct {
	ID               string          `json:"id"`
	AccountID        string          `json:"accountId"`
	ProviderThreadID string          `json:"providerThreadId"`
	Subject          string          `json:"subject"`
	OpportunityID    string          `json:"opportunityId,omitempty"`
	LastMessageAt    string          `json:"lastMessageAt"`
	MessageCount     int64           `json:"messageCount"`
	Provenance       json.RawMessage `json:"provenance"`
	CreatedAt        string          `json:"createdAt"`
	UpdatedAt        string          `json:"updatedAt"`
}

type CorrespondenceMessage struct {
	ID                string          `json:"id"`
	ThreadID          string          `json:"threadId"`
	ProviderMessageID string          `json:"providerMessageId"`
	Sender            string          `json:"sender"`
	Recipients        []string        `json:"recipients"`
	SentAt            string          `json:"sentAt"`
	BodySHA256        string          `json:"bodySha256"`
	Body              string          `json:"body"`
	Provenance        json.RawMessage `json:"provenance"`
	CreatedAt         string          `json:"createdAt"`
}

type CorrespondenceMessageSnapshot struct {
	ProviderMessageID string
	Sender            string
	Recipients        []string
	SentAt            string
	Body              string
	Provenance        map[string]any
}

type CorrespondenceThreadSnapshot struct {
	ProviderThreadID string
	Subject          string
	LastMessageAt    string
	Provenance       map[string]any
	Messages         []CorrespondenceMessageSnapshot
}

type CorrespondenceNotificationInput struct {
	Kind     string
	ThreadID string
	DueAt    string
	Payload  map[string]any
}

type CorrespondenceNotification struct {
	ID        string          `json:"id"`
	AccountID string          `json:"accountId"`
	Kind      string          `json:"kind"`
	ThreadID  string          `json:"threadId,omitempty"`
	DueAt     string          `json:"dueAt,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt"`
}

func correspondenceText(value string, limit int) bool {
	return len(value) <= limit
}

func correspondenceSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Store) ConnectCorrespondenceAccount(ctx context.Context, actor Actor, input CorrespondenceAccountInput) (CorrespondenceAccount, bool, error) {
	if actor.Kind != "administrator" || actor.ID == "" || !correspondenceText(input.Provider, 80) || len(input.Provider) == 0 ||
		len(input.ExternalAccountID) == 0 || !correspondenceText(input.ExternalAccountID, 320) || !correspondenceText(input.DisplayName, 200) {
		return CorrespondenceAccount{}, false, ErrInvalid
	}
	var v CorrespondenceAccount
	err := s.db.QueryRowContext(ctx, `SELECT id,provider,external_account_id,display_name,status,created_at,updated_at FROM correspondence_accounts WHERE actor_id=? AND provider=? AND external_account_id=?`, actor.ID, input.Provider, input.ExternalAccountID).
		Scan(&v.ID, &v.Provider, &v.ExternalAccountID, &v.DisplayName, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if err == nil {
		return v, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CorrespondenceAccount{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return CorrespondenceAccount{}, false, err
	}
	now := utcNow()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO correspondence_accounts(id,actor_id,provider,external_account_id,display_name,status,created_at,updated_at) VALUES(?,?,?,?,?, 'active',?,?)`, id, actor.ID, input.Provider, input.ExternalAccountID, input.DisplayName, now, now); err != nil {
		return CorrespondenceAccount{}, false, err
	}
	v = CorrespondenceAccount{ID: id, Provider: input.Provider, ExternalAccountID: input.ExternalAccountID, DisplayName: input.DisplayName, Status: "active", CreatedAt: now, UpdatedAt: now}
	return v, true, nil
}

func (s *Store) SetCorrespondenceAccountStatus(ctx context.Context, actor Actor, accountID, status string) error {
	if actor.Kind != "administrator" || actor.ID == "" || accountID == "" {
		return ErrInvalid
	}
	if status != "active" && status != "auth_lost" && status != "disabled" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE correspondence_accounts SET status=?,updated_at=? WHERE id=? AND actor_id=?`, status, utcNow(), accountID, actor.ID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// SyncCorrespondenceThreads mirrors one bounded provider read into the local
// store. Provider IDs deduplicate: repeated retrieval never duplicates a
// thread or message, and local links survive re-sync.
func (s *Store) SyncCorrespondenceThreads(ctx context.Context, actor Actor, accountID string, snapshots []CorrespondenceThreadSnapshot) (int64, int64, error) {
	if actor.Kind != "administrator" || actor.ID == "" || accountID == "" || len(snapshots) == 0 || len(snapshots) > 50 {
		return 0, 0, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var status, owner string
	if err := tx.QueryRowContext(ctx, `SELECT status,actor_id FROM correspondence_accounts WHERE id=?`, accountID).Scan(&status, &owner); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	} else if err != nil {
		return 0, 0, err
	}
	if owner != actor.ID || status != "active" {
		return 0, 0, ErrFenced
	}
	var threads, messages int64
	now := utcNow()
	for _, snapshot := range snapshots {
		if len(snapshot.ProviderThreadID) == 0 || !correspondenceText(snapshot.ProviderThreadID, 320) || !correspondenceText(snapshot.Subject, 500) || len(snapshot.Messages) > 200 {
			return 0, 0, ErrInvalid
		}
		provenance, _ := json.Marshal(snapshot.Provenance)
		if len(provenance) == 0 {
			provenance = []byte("{}")
		}
		threadID, err := randomID()
		if err != nil {
			return 0, 0, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO correspondence_threads(id,account_id,actor_id,provider_thread_id,subject,last_message_at,message_count,provenance_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,provider_thread_id) DO NOTHING`, threadID, accountID, actor.ID, snapshot.ProviderThreadID, snapshot.Subject, snapshot.LastMessageAt, len(snapshot.Messages), string(provenance), now, now)
		if err != nil {
			return 0, 0, err
		}
		if count, _ := res.RowsAffected(); count == 1 {
			threads++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE correspondence_threads SET subject=?,last_message_at=?,provenance_json=?,updated_at=? WHERE account_id=? AND provider_thread_id=?`, snapshot.Subject, snapshot.LastMessageAt, string(provenance), now, accountID, snapshot.ProviderThreadID); err != nil {
			return 0, 0, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM correspondence_threads WHERE account_id=? AND provider_thread_id=?`, accountID, snapshot.ProviderThreadID).Scan(&threadID); err != nil {
			return 0, 0, err
		}
		for _, message := range snapshot.Messages {
			if len(message.ProviderMessageID) == 0 || !correspondenceText(message.ProviderMessageID, 320) || !correspondenceText(message.Sender, 320) || len(message.SentAt) == 0 || len(message.Body) > 100000 {
				return 0, 0, ErrInvalid
			}
			recipients, _ := json.Marshal(message.Recipients)
			if len(recipients) == 0 {
				recipients = []byte("[]")
			}
			messageProvenance, _ := json.Marshal(message.Provenance)
			if len(messageProvenance) == 0 {
				messageProvenance = []byte("{}")
			}
			messageID, err := randomID()
			if err != nil {
				return 0, 0, err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO correspondence_messages(id,thread_id,actor_id,provider_message_id,sender,recipients_json,sent_at,body_sha256,body,provenance_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(thread_id,provider_message_id) DO NOTHING`, messageID, threadID, actor.ID, message.ProviderMessageID, message.Sender, string(recipients), message.SentAt, correspondenceSHA(message.Body), message.Body, string(messageProvenance), now)
			if err != nil {
				return 0, 0, err
			}
			if count, _ := res.RowsAffected(); count == 1 {
				messages++
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE correspondence_threads SET message_count=(SELECT COUNT(*) FROM correspondence_messages WHERE thread_id=?),updated_at=? WHERE id=?`, threadID, now, threadID); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return threads, messages, nil
}

func (s *Store) OwnerCorrespondenceAccount(ctx context.Context, ownerID, accountID string) (CorrespondenceAccount, error) {
	var v CorrespondenceAccount
	if ownerID == "" || accountID == "" {
		return v, ErrInvalid
	}
	err := s.db.QueryRowContext(ctx, `SELECT id,provider,external_account_id,display_name,status,created_at,updated_at FROM correspondence_accounts WHERE id=? AND actor_id=?`, accountID, ownerID).
		Scan(&v.ID, &v.Provider, &v.ExternalAccountID, &v.DisplayName, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func (s *Store) OwnerCorrespondenceThreads(ctx context.Context, ownerID string) ([]CorrespondenceThread, error) {
	if ownerID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,account_id,provider_thread_id,subject,COALESCE(opportunity_id,''),last_message_at,message_count,provenance_json,created_at,updated_at FROM correspondence_threads WHERE actor_id=? ORDER BY updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []CorrespondenceThread
	for rows.Next() {
		var v CorrespondenceThread
		var provenance string
		if err := rows.Scan(&v.ID, &v.AccountID, &v.ProviderThreadID, &v.Subject, &v.OpportunityID, &v.LastMessageAt, &v.MessageCount, &provenance, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Provenance = json.RawMessage(provenance)
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) OwnerCorrespondenceThread(ctx context.Context, ownerID, threadID string) (CorrespondenceThread, error) {
	var v CorrespondenceThread
	if ownerID == "" || threadID == "" {
		return v, ErrInvalid
	}
	var provenance string
	err := s.db.QueryRowContext(ctx, `SELECT id,account_id,provider_thread_id,subject,COALESCE(opportunity_id,''),last_message_at,message_count,provenance_json,created_at,updated_at FROM correspondence_threads WHERE id=? AND actor_id=?`, threadID, ownerID).
		Scan(&v.ID, &v.AccountID, &v.ProviderThreadID, &v.Subject, &v.OpportunityID, &v.LastMessageAt, &v.MessageCount, &provenance, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Provenance = json.RawMessage(provenance)
	return v, nil
}

func (s *Store) CorrespondenceThreadMessages(ctx context.Context, ownerID, threadID string) ([]CorrespondenceMessage, error) {
	if ownerID == "" || threadID == "" {
		return nil, ErrInvalid
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT actor_id FROM correspondence_threads WHERE id=?`, threadID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if owner != ownerID {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,thread_id,provider_message_id,sender,recipients_json,sent_at,body_sha256,body,provenance_json,created_at FROM correspondence_messages WHERE thread_id=? ORDER BY sent_at ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []CorrespondenceMessage
	for rows.Next() {
		var v CorrespondenceMessage
		var recipients, provenance string
		if err := rows.Scan(&v.ID, &v.ThreadID, &v.ProviderMessageID, &v.Sender, &recipients, &v.SentAt, &v.BodySHA256, &v.Body, &provenance, &v.CreatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(recipients), &v.Recipients) != nil {
			return nil, ErrInvalid
		}
		v.Provenance = json.RawMessage(provenance)
		items = append(items, v)
	}
	return items, rows.Err()
}

// RecordCorrespondenceNotification stores one inert incoming/due record. It
// never starts a round; processing requires an explicit owner commission.
func (s *Store) RecordCorrespondenceNotification(ctx context.Context, actor Actor, accountID string, input CorrespondenceNotificationInput) (CorrespondenceNotification, error) {
	var v CorrespondenceNotification
	if actor.Kind != "administrator" || actor.ID == "" || accountID == "" {
		return v, ErrInvalid
	}
	if input.Kind != "incoming" && input.Kind != "due" {
		return v, ErrInvalid
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT actor_id FROM correspondence_accounts WHERE id=?`, accountID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	} else if err != nil {
		return v, err
	}
	if owner != actor.ID {
		return v, ErrFenced
	}
	if input.ThreadID != "" {
		var threadOwner string
		if err := s.db.QueryRowContext(ctx, `SELECT actor_id FROM correspondence_threads WHERE id=? AND account_id=?`, input.ThreadID, accountID).Scan(&threadOwner); errors.Is(err, sql.ErrNoRows) {
			return v, ErrNotFound
		} else if err != nil {
			return v, err
		}
		if threadOwner != actor.ID {
			return v, ErrFenced
		}
	}
	payload, _ := json.Marshal(input.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	id, err := randomID()
	if err != nil {
		return v, err
	}
	now := utcNow()
	thread := sql.NullString{String: input.ThreadID, Valid: input.ThreadID != ""}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO correspondence_notifications(id,account_id,actor_id,kind,thread_id,due_at,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, accountID, actor.ID, input.Kind, thread, input.DueAt, string(payload), now); err != nil {
		return v, err
	}
	v = CorrespondenceNotification{ID: id, AccountID: accountID, Kind: input.Kind, ThreadID: input.ThreadID, DueAt: input.DueAt, Payload: payload, CreatedAt: now}
	return v, nil
}
