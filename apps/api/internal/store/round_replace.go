package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ReplacePausedRound is one owner action with two named outcomes. It retains
// the former round's attempts and spent allowance and closes its authority
// before inserting the replacement under the same active-slot transaction.
func (s *Store) ReplacePausedRound(ctx context.Context, actor Actor, pausedID string, expectedRevision int64, input StartRoundInput) (Round, bool, error) {
	now := time.Now().UTC()
	if !ownerRoundActor(actor) || pausedID == "" || expectedRevision < 1 ||
		!ownerRequestKey(input.RequestKey) || strings.TrimSpace(input.Intent) == "" || len(input.Intent) > 2000 ||
		input.ProfileVersion < 1 || !validScope(input.Scope) || !input.Limits.valid() || !input.Limits.nonzero() ||
		!input.Deadline.After(now) || input.Deadline.After(now.Add(24*time.Hour)) {
		return Round{}, false, ErrInvalid
	}
	startDigest, err := roundDigest(input)
	if err != nil {
		return Round{}, false, err
	}
	digest := ownerDigest(struct {
		StartDigest      string
		PausedID         string
		ExpectedRevision int64
	}{startDigest, pausedID, expectedRevision})
	scopeJSON, _ := json.Marshal(input.Scope)
	newID, err := randomID()
	if err != nil {
		return Round{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Round{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET revision=revision WHERE id=?`, pausedID); err != nil {
		return Round{}, false, err
	}
	var oldID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM rounds WHERE actor_kind=? AND actor_id=? AND request_key=?`, actor.Kind, actor.ID, input.RequestKey).Scan(&oldID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return Round{}, false, ErrRoundIdempotencyConflict
		}
		r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, oldID))
		if err != nil {
			return Round{}, false, err
		}
		return r, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Round{}, false, err
	}
	paused, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, pausedID))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, false, ErrNotFound
	}
	if err != nil {
		return Round{}, false, err
	}
	if paused.Actor != actor || paused.State != RoundPaused || paused.Revision != expectedRevision {
		return Round{}, false, ErrFenced
	}
	var currentProfile int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return Round{}, false, err
	}
	if currentProfile != input.ProfileVersion {
		return Round{}, false, ErrConflict
	}
	report, _ := json.Marshal(map[string]string{"code": "replaced_by_owner_input", "replacementRoundId": newID})
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='failed',revision=revision+1,generation=generation+1,
	  stop_reason='replaced_by_owner_input',deliverable_status='partial',report_json=?,completed_at=?,updated_at=?
	  WHERE id=? AND state='paused' AND revision=?`, string(report), utcNow(), utcNow(), pausedID, expectedRevision); err != nil {
		return Round{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_reconciliation_checks SET state='fenced',finished_at=? WHERE round_id=? AND state='pending'`, utcNow(), pausedID); err != nil {
		return Round{}, false, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.replace_end", pausedID); err != nil {
		return Round{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rounds
	  (id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,initial_profile_version,profile_version,scope_json,
	   state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,'queued',1,1,?,?,?,?,?,?,?)`, newID, actor.Kind, actor.ID,
		input.RequestKey, digest, input.Intent, input.Outcome, input.ProfileVersion, input.ProfileVersion, string(scopeJSON),
		input.Deadline.UTC().Format(time.RFC3339Nano), input.Limits.Requests, input.Limits.Items,
		input.Limits.Tools, input.Limits.Turns, utcNow(), utcNow())
	if err != nil {
		return Round{}, false, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.replace_start", newID); err != nil {
		return Round{}, false, err
	}
	r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, newID))
	if err != nil {
		return Round{}, false, err
	}
	return r, true, tx.Commit()
}
