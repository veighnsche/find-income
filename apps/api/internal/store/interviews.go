package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type InterviewCommissionInput struct {
	RequestKey    string `json:"requestKey"`
	OpportunityID string `json:"opportunityId"`
	Context       string `json:"context"`
}

type Interview struct {
	ID                  string          `json:"id"`
	OpportunityID       string          `json:"opportunityId"`
	OpportunityRevision int64           `json:"opportunityRevision"`
	ProfileVersion      int64           `json:"profileVersion"`
	Context             string          `json:"context"`
	ContextSHA256       string          `json:"contextSha256"`
	RoundID             string          `json:"roundId,omitempty"`
	Brief               json.RawMessage `json:"brief,omitempty"`
	Focus               json.RawMessage `json:"focus,omitempty"`
	FocusJevAttemptID   string          `json:"focusJevAttemptId,omitempty"`
	Current             bool            `json:"current"`
	CreatedAt           string          `json:"createdAt"`
	UpdatedAt           string          `json:"updatedAt"`
}

type InterviewDebriefCommissionInput struct {
	RequestKey  string `json:"requestKey"`
	InterviewID string `json:"interviewId"`
	Notes       string `json:"notes"`
}

type InterviewDebrief struct {
	ID          string          `json:"id"`
	InterviewID string          `json:"interviewId"`
	Notes       string          `json:"notes"`
	RoundID     string          `json:"roundId,omitempty"`
	Debrief     json.RawMessage `json:"debrief,omitempty"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
}

type InterviewDebriefMutation struct {
	DebriefID   string          `json:"debriefId"`
	InterviewID string          `json:"interviewId"`
	DebriefJSON json.RawMessage `json:"debrief"`
}

type InterviewBriefMutation struct {
	InterviewID   string          `json:"interviewId"`
	OpportunityID string          `json:"opportunityId"`
	InputSHA256   string          `json:"inputSha256"`
	BriefJSON     json.RawMessage `json:"brief"`
}

func interviewDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func interviewText(v string, max int) bool {
	return len(v) > 0 && len(v) <= max && strings.TrimSpace(v) != "" && !strings.ContainsRune(v, 0)
}

func (s *Store) CommissionInterview(ctx context.Context, actor Actor, input InterviewCommissionInput) (Interview, bool, error) {
	if actor.Kind != "administrator" || actor.ID == "" || !ownerRequestKey(input.RequestKey) || input.OpportunityID == "" || len(input.OpportunityID) > 128 || !interviewText(input.Context, 30000) {
		return Interview{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Interview{}, false, err
	}
	defer tx.Rollback()
	digest := interviewDigest(input)
	var priorID, priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM interviews WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&priorID, &priorDigest)
	if err == nil {
		if digest != priorDigest {
			return Interview{}, false, ErrRoundIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return Interview{}, false, err
		}
		v, err := s.Interview(ctx, priorID)
		return v, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Interview{}, false, err
	}
	var revision, profile int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, input.OpportunityID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Interview{}, false, ErrNotFound
	}
	if err != nil {
		return Interview{}, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profile); err != nil {
		return Interview{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return Interview{}, false, err
	}
	contextSHA := interviewDigestBytes([]byte(input.Context))
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO interviews(id,opportunity_id,opportunity_revision,profile_version,actor_id,request_key,request_sha256,context_text,context_sha256,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, input.OpportunityID, revision, profile, actor.ID, input.RequestKey, digest, input.Context, contextSHA, now, now)
	if err != nil {
		return Interview{}, false, err
	}
	if err = interviewAuditTx(ctx, tx, actor, "interview.commission", "interview", id, 1); err != nil {
		return Interview{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Interview{}, false, err
	}
	v, err := s.Interview(ctx, id)
	return v, true, err
}

func (s *Store) CommissionInterviewDebrief(ctx context.Context, actor Actor, input InterviewDebriefCommissionInput) (InterviewDebrief, bool, error) {
	if actor.Kind != "administrator" || actor.ID == "" || !ownerRequestKey(input.RequestKey) || input.InterviewID == "" || !interviewText(input.Notes, 30000) {
		return InterviewDebrief{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InterviewDebrief{}, false, err
	}
	defer tx.Rollback()
	digest := interviewDigest(input)
	var id, priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM interview_debriefs WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&id, &priorDigest)
	if err == nil {
		if digest != priorDigest {
			return InterviewDebrief{}, false, ErrRoundIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return InterviewDebrief{}, false, err
		}
		v, err := s.InterviewDebrief(ctx, id)
		return v, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return InterviewDebrief{}, false, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM interviews WHERE id=? AND actor_id=? AND brief_json IS NOT NULL`, input.InterviewID, actor.ID).Scan(&exists); err != nil {
		return InterviewDebrief{}, false, err
	}
	if exists != 1 {
		return InterviewDebrief{}, false, ErrNotFound
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM interviews i
 JOIN opportunities o ON o.id=i.opportunity_id
 JOIN preferences_current p ON p.singleton=1
 WHERE i.id=? AND i.actor_id=? AND o.archived_at IS NULL
 AND i.opportunity_revision=o.revision AND i.profile_version=p.version`, input.InterviewID, actor.ID).Scan(&current); err != nil {
		return InterviewDebrief{}, false, err
	}
	if current != 1 {
		return InterviewDebrief{}, false, ErrConflict
	}
	id, err = randomID()
	if err != nil {
		return InterviewDebrief{}, false, err
	}
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO interview_debriefs(id,interview_id,actor_id,request_key,request_sha256,owner_notes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, input.InterviewID, actor.ID, input.RequestKey, digest, input.Notes, now, now)
	if err != nil {
		return InterviewDebrief{}, false, err
	}
	if err = interviewAuditTx(ctx, tx, actor, "interview.debrief_commission", "interview_debrief", id, 1); err != nil {
		return InterviewDebrief{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return InterviewDebrief{}, false, err
	}
	v, err := s.InterviewDebrief(ctx, id)
	return v, true, err
}

func interviewAuditTx(ctx context.Context, tx *sql.Tx, actor Actor, operation, kind, id string, revision int64) error {
	audit, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes(id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at) VALUES(?,?,?,?,?,?,?,?)`, audit, actor.Kind, actor.ID, operation, kind, id, revision, utcNow())
	return err
}

func (s *Store) BindInterviewRound(ctx context.Context, actor Actor, interviewID, roundID string, debrief bool) error {
	if actor.Kind != "administrator" || actor.ID == "" || interviewID == "" || roundID == "" {
		return ErrInvalid
	}
	table, ref, outcome := "interviews", "interview:", "interview_prepare"
	if debrief {
		table, ref, outcome = "interview_debriefs", "debrief:", "interview_debrief"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if err != nil {
		return err
	}
	if r.Actor != actor || r.Outcome != outcome || !scopeHas(r.Scope.InputRefs, ref+interviewID) {
		return ErrFenced
	}
	var prior sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT round_id FROM `+table+` WHERE id=? AND actor_id=?`, interviewID, actor.ID).Scan(&prior)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if prior.Valid && prior.String != roundID {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE `+table+` SET round_id=?,updated_at=? WHERE id=? AND actor_id=?`, roundID, utcNow(), interviewID, actor.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Interview(ctx context.Context, id string) (Interview, error) {
	var v Interview
	var round, brief, focus, jevID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,opportunity_id,opportunity_revision,profile_version,context_text,context_sha256,round_id,brief_json,focus_json,focus_jev_attempt_id,created_at,updated_at FROM interviews WHERE id=?`, id).Scan(&v.ID, &v.OpportunityID, &v.OpportunityRevision, &v.ProfileVersion, &v.Context, &v.ContextSHA256, &round, &brief, &focus, &jevID, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Interview{}, ErrNotFound
	}
	if err != nil {
		return Interview{}, err
	}
	v.RoundID, v.FocusJevAttemptID = round.String, jevID.String
	if brief.Valid {
		v.Brief = json.RawMessage(brief.String)
	}
	if focus.Valid {
		v.Focus = json.RawMessage(focus.String)
	}
	var currentRevision, currentProfile int64
	err = s.db.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, v.OpportunityID).Scan(&currentRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Interview{}, err
	}
	if e := s.db.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); e != nil {
		return Interview{}, e
	}
	v.Current = err == nil && currentRevision == v.OpportunityRevision && currentProfile == v.ProfileVersion
	return v, nil
}

func (s *Store) Interviews(ctx context.Context) ([]Interview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM interviews ORDER BY created_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]Interview, 0, len(ids))
	for _, id := range ids {
		v, err := s.Interview(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

func (s *Store) OwnerInterview(ctx context.Context, ownerID, id string) (Interview, error) {
	if ownerID == "" || id == "" {
		return Interview{}, ErrInvalid
	}
	var found int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM interviews WHERE id=? AND actor_id=?`, id, ownerID).Scan(&found); err != nil {
		return Interview{}, err
	}
	if found != 1 {
		return Interview{}, ErrNotFound
	}
	return s.Interview(ctx, id)
}

func (s *Store) OwnerInterviews(ctx context.Context, ownerID string) ([]Interview, error) {
	if ownerID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM interviews WHERE actor_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]Interview, 0, len(ids))
	for _, id := range ids {
		v, err := s.OwnerInterview(ctx, ownerID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

func (s *Store) OwnerInterviewDebriefs(ctx context.Context, ownerID, interviewID string) ([]InterviewDebrief, error) {
	if _, err := s.OwnerInterview(ctx, ownerID, interviewID); err != nil {
		return nil, err
	}
	return s.InterviewDebriefs(ctx, interviewID)
}

func (s *Store) InterviewDebrief(ctx context.Context, id string) (InterviewDebrief, error) {
	var v InterviewDebrief
	var round, data sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,interview_id,owner_notes,round_id,debrief_json,created_at,updated_at FROM interview_debriefs WHERE id=?`, id).Scan(&v.ID, &v.InterviewID, &v.Notes, &round, &data, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InterviewDebrief{}, ErrNotFound
	}
	if err != nil {
		return InterviewDebrief{}, err
	}
	v.RoundID = round.String
	if data.Valid {
		v.Debrief = json.RawMessage(data.String)
	}
	return v, nil
}

func (s *Store) InterviewDebriefs(ctx context.Context, interviewID string) ([]InterviewDebrief, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM interview_debriefs WHERE interview_id=? ORDER BY created_at,id LIMIT 100`, interviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]InterviewDebrief, 0, len(ids))
	for _, id := range ids {
		v, err := s.InterviewDebrief(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

func writeInterviewBriefTx(ctx context.Context, tx *sql.Tx, round Round, expectedRevision int64, brief InterviewBriefMutation) (string, string, int64, error) {
	var snapshot struct {
		Input       json.RawMessage `json:"input"`
		InputSHA256 string          `json:"inputSha256"`
	}
	if len(brief.BriefJSON) == 0 || len(brief.BriefJSON) > 500000 || json.Unmarshal(brief.BriefJSON, &snapshot) != nil || len(snapshot.Input) == 0 || snapshot.InputSHA256 != brief.InputSHA256 || interviewDigestBytes(snapshot.Input) != brief.InputSHA256 {
		return "", "", 0, ErrInvalid
	}
	var input struct {
		InterviewID   string `json:"interviewId"`
		OpportunityID string `json:"opportunityId"`
		RoleTitle     string `json:"roleTitle"`
		EmployerName  string `json:"employerName"`
		Context       []struct {
			ID     string `json:"id"`
			Body   string `json:"body"`
			SHA256 string `json:"sha256"`
		} `json:"context"`
	}
	if json.Unmarshal(snapshot.Input, &input) != nil || input.InterviewID != brief.InterviewID || input.OpportunityID != brief.OpportunityID {
		return "", "", 0, ErrInvalid
	}
	id := brief.InterviewID
	var opp string
	var revision, profile int64
	var old sql.NullString
	var contextText, contextSHA, title, company, roleText string
	err := tx.QueryRowContext(ctx, `SELECT i.opportunity_id,i.opportunity_revision,i.profile_version,i.brief_json,i.context_text,i.context_sha256,o.title,c.name,o.original_text FROM interviews i JOIN opportunities o ON o.id=i.opportunity_id JOIN companies c ON c.id=o.company_id WHERE i.id=? AND i.round_id=?`, id, round.ID).Scan(&opp, &revision, &profile, &old, &contextText, &contextSHA, &title, &company, &roleText)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrFenced
	}
	if err != nil {
		return "", "", 0, err
	}
	if old.Valid || expectedRevision != revision || round.ProfileVersion != profile || opp != brief.OpportunityID || input.RoleTitle != title || input.EmployerName != company {
		return "", "", 0, ErrConflict
	}
	found, roleFound := false, false
	for _, source := range input.Context {
		if source.ID == "owner-input" && source.Body == contextText && source.SHA256 == contextSHA {
			found = true
		}
		if source.ID == "role" && source.Body == roleText && source.SHA256 == interviewDigestBytes([]byte(roleText)) {
			roleFound = true
		}
	}
	if !found || !roleFound {
		return "", "", 0, ErrInvalid
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opp).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrNotFound
	}
	if err != nil {
		return "", "", 0, err
	}
	if current != revision {
		return "", "", 0, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE interviews SET brief_json=?,brief_sha256=?,updated_at=? WHERE id=? AND brief_json IS NULL`, string(brief.BriefJSON), brief.InputSHA256, utcNow(), id)
	return id, "interview_brief", 1, err
}

func writeInterviewDebriefTx(ctx context.Context, tx *sql.Tx, round Round, debrief InterviewDebriefMutation) (string, string, int64, error) {
	var snapshot struct {
		Input       json.RawMessage `json:"input"`
		InputSHA256 string          `json:"inputSha256"`
	}
	if len(debrief.DebriefJSON) == 0 || len(debrief.DebriefJSON) > 60000 || json.Unmarshal(debrief.DebriefJSON, &snapshot) != nil || interviewDigestBytes(snapshot.Input) != snapshot.InputSHA256 {
		return "", "", 0, ErrInvalid
	}
	var input struct {
		InterviewID string `json:"interviewId"`
		OwnerNotes  string `json:"ownerNotes"`
	}
	if json.Unmarshal(snapshot.Input, &input) != nil || input.InterviewID != debrief.InterviewID {
		return "", "", 0, ErrInvalid
	}
	var interviewID, notes string
	var old sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT interview_id,owner_notes,debrief_json FROM interview_debriefs WHERE id=? AND round_id=?`, debrief.DebriefID, round.ID).Scan(&interviewID, &notes, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrFenced
	}
	if err != nil {
		return "", "", 0, err
	}
	if old.Valid || notes != input.OwnerNotes || interviewID != input.InterviewID {
		return "", "", 0, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE interview_debriefs SET debrief_json=?,updated_at=? WHERE id=? AND debrief_json IS NULL`, string(debrief.DebriefJSON), utcNow(), debrief.DebriefID)
	return debrief.DebriefID, "interview_debrief", 1, err
}

func interviewDigestBytes(v []byte) string {
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:])
}

func (s *Store) ApplyInterviewFocus(ctx context.Context, actor Actor, roundID string, expectedGeneration int64, interviewID, jevAttemptID string, preparedJSON json.RawMessage) (Interview, error) {
	if actor.Kind != "agent" || actor.ID == "" || expectedGeneration < 1 || interviewID == "" || jevAttemptID == "" {
		return Interview{}, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return Interview{}, err
	}
	defer tx.Rollback()
	if !scopeAllowsActor(round.Scope, actor) || round.State != RoundRunning || round.Generation != expectedGeneration || round.Outcome != "interview_prepare" ||
		!scopeHas(round.Scope.InputRefs, "interview:"+interviewID) || !scopeHas(round.Scope.Operations, RoundJevRequest) {
		return Interview{}, ErrFenced
	}
	if !time.Now().Before(round.Deadline) {
		return Interview{}, ErrExpired
	}
	var briefJSON string
	var oldFocus sql.NullString
	var opp string
	var revision, profile int64
	err = tx.QueryRowContext(ctx, `SELECT brief_json,focus_json,opportunity_id,opportunity_revision,profile_version FROM interviews WHERE id=? AND round_id=?`, interviewID, roundID).Scan(&briefJSON, &oldFocus, &opp, &revision, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return Interview{}, ErrFenced
	}
	if err != nil {
		return Interview{}, err
	}
	if oldFocus.Valid {
		return Interview{}, ErrConflict
	}
	var brief struct {
		InputSHA256 string `json:"inputSha256"`
	}
	if err = json.Unmarshal([]byte(briefJSON), &brief); err != nil {
		return Interview{}, err
	}
	var prepared struct {
		Brief struct {
			InputSHA256 string `json:"inputSha256"`
		} `json:"brief"`
		Selection struct {
			InputSHA256     string          `json:"input_sha256"`
			RequestSnapshot json.RawMessage `json:"request_snapshot"`
		} `json:"selection"`
	}
	if len(preparedJSON) == 0 || len(preparedJSON) > 700000 || json.Unmarshal(preparedJSON, &prepared) != nil || brief.InputSHA256 != prepared.Brief.InputSHA256 || len(prepared.Selection.InputSHA256) != 64 {
		return Interview{}, ErrInvalid
	}
	var state, attemptRound, operation, attemptResource string
	var logicalRequest []byte
	var attemptState RoundAttemptState
	err = tx.QueryRowContext(ctx, `SELECT j.status,j.round_id,j.logical_request_json,a.operation,a.resource_id,a.state
 FROM jev_attempts j JOIN round_attempts a ON a.id=j.round_attempt_id
 WHERE j.id=? AND j.purpose='interview_focus'`, jevAttemptID).
		Scan(&state, &attemptRound, &logicalRequest, &operation, &attemptResource, &attemptState)
	if errors.Is(err, sql.ErrNoRows) {
		return Interview{}, ErrNotFound
	}
	if err != nil {
		return Interview{}, err
	}
	if state != "succeeded" || attemptRound != roundID || profile != round.ProfileVersion ||
		operation != RoundJevRequest || attemptResource != "opportunity:"+opp ||
		(attemptState != AttemptSucceeded && attemptState != AttemptObservedSuccess) ||
		!scopeHas(round.Scope.Resources, "opportunity:"+opp) || !bytes.Equal(logicalRequest, prepared.Selection.RequestSnapshot) {
		return Interview{}, ErrFenced
	}
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opp).Scan(&current); err != nil {
		return Interview{}, err
	}
	if current != revision {
		return Interview{}, ErrConflict
	}
	var currentProfile int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return Interview{}, err
	}
	if currentProfile != profile {
		return Interview{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE interviews SET focus_json=?,focus_jev_attempt_id=?,updated_at=? WHERE id=? AND focus_json IS NULL`, string(preparedJSON), jevAttemptID, utcNow(), interviewID)
	if err != nil {
		return Interview{}, err
	}
	if err = interviewAuditTx(ctx, tx, actor, "interview.focus_apply", "interview", interviewID, 2); err != nil {
		return Interview{}, err
	}
	if err = tx.Commit(); err != nil {
		return Interview{}, err
	}
	return s.Interview(ctx, interviewID)
}
