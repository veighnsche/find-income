package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var collectorSitePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
var collectorErrorPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

const collectorContinuationDelay = time.Minute

type CollectorBoardInput struct {
	Provider        string
	Site            string
	Region          string
	Enabled         bool
	IntervalMinutes int
}

type CollectorBoard struct {
	ID string
	CollectorBoardInput
	NextScanAt    time.Time
	NextOffset    int
	LeaseToken    string
	LeaseUntil    time.Time
	LastRunAt     time.Time
	LastSuccessAt time.Time
	LastErrorCode string
	Revision      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func validateCollectorBoard(input CollectorBoardInput) error {
	if input.Provider != "lever" || !collectorSitePattern.MatchString(input.Site) ||
		(input.Region != "global" && input.Region != "eu") ||
		input.IntervalMinutes < 15 || input.IntervalMinutes > 10080 {
		return fmt.Errorf("%w: supported provider, site, region and interval required", ErrInvalid)
	}
	return nil
}

const collectorBoardColumns = `id,provider,site,region,enabled,interval_minutes,next_scan_at,next_offset,
  lease_token,lease_until,last_run_at,last_success_at,last_error_code,revision,created_at,updated_at`

func scanCollectorBoard(row rowScanner) (CollectorBoard, error) {
	var board CollectorBoard
	var enabled int
	var nextScan, created, updated string
	var token, leaseUntil, lastRun, lastSuccess, lastError sql.NullString
	err := row.Scan(&board.ID, &board.Provider, &board.Site, &board.Region, &enabled,
		&board.IntervalMinutes, &nextScan, &board.NextOffset, &token, &leaseUntil,
		&lastRun, &lastSuccess, &lastError, &board.Revision, &created, &updated)
	if err != nil {
		return CollectorBoard{}, err
	}
	board.Enabled = enabled == 1
	board.LeaseToken, board.LastErrorCode = token.String, lastError.String
	for _, item := range []struct {
		value string
		out   *time.Time
	}{{nextScan, &board.NextScanAt}, {leaseUntil.String, &board.LeaseUntil},
		{lastRun.String, &board.LastRunAt}, {lastSuccess.String, &board.LastSuccessAt},
		{created, &board.CreatedAt}, {updated, &board.UpdatedAt}} {
		if item.value == "" {
			continue
		}
		*item.out, err = parseJobTime(item.value)
		if err != nil {
			return CollectorBoard{}, err
		}
	}
	return board, nil
}

func (s *Store) CreateCollectorBoard(ctx context.Context, actor Actor, input CollectorBoardInput) (CollectorBoard, error) {
	if actor.Kind != "administrator" || !requiredActor(actor) {
		return CollectorBoard{}, ErrInvalid
	}
	if err := validateCollectorBoard(input); err != nil {
		return CollectorBoard{}, err
	}
	id, err := randomID()
	if err != nil {
		return CollectorBoard{}, err
	}
	now := jobTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectorBoard{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO collector_boards
  (id,provider,site,region,enabled,interval_minutes,next_scan_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,site,region) DO NOTHING`,
		id, input.Provider, input.Site, input.Region, input.Enabled,
		input.IntervalMinutes, now, now, now)
	if err != nil {
		return CollectorBoard{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return CollectorBoard{}, err
	}
	if count != 1 {
		return CollectorBoard{}, ErrConflict
	}
	auditID, err := randomID()
	if err != nil {
		return CollectorBoard{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "collector.board.create", "collector_board", id, now)
	if err != nil {
		return CollectorBoard{}, err
	}
	board, err := scanCollectorBoard(tx.QueryRowContext(ctx, `SELECT `+collectorBoardColumns+` FROM collector_boards WHERE id=?`, id))
	if err != nil {
		return CollectorBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return CollectorBoard{}, err
	}
	return board, nil
}

// Board identity is immutable. Disable the old board and create another to
// change provider, site or region without reusing an unrelated scan cursor.
func (s *Store) UpdateCollectorBoard(ctx context.Context, actor Actor, id string, expectedRevision int64, enabled bool, intervalMinutes int) (CollectorBoard, error) {
	if actor.Kind != "administrator" || !requiredActor(actor) || id == "" || expectedRevision < 1 ||
		intervalMinutes < 15 || intervalMinutes > 10080 {
		return CollectorBoard{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectorBoard{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE collector_boards SET enabled=?,interval_minutes=?,revision=revision+1,
  updated_at=? WHERE id=? AND revision=?`, enabled, intervalMinutes, jobTime(time.Now()), id, expectedRevision)
	if err != nil {
		return CollectorBoard{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return CollectorBoard{}, err
	}
	if count != 1 {
		return CollectorBoard{}, ErrConflict
	}
	auditID, err := randomID()
	if err != nil {
		return CollectorBoard{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "collector.board.update", "collector_board", id, utcNow())
	if err != nil {
		return CollectorBoard{}, err
	}
	board, err := scanCollectorBoard(tx.QueryRowContext(ctx, `SELECT `+collectorBoardColumns+` FROM collector_boards WHERE id=?`, id))
	if err != nil {
		return CollectorBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return CollectorBoard{}, err
	}
	return board, nil
}

func (s *Store) ListCollectorBoards(ctx context.Context) ([]CollectorBoard, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+collectorBoardColumns+` FROM collector_boards ORDER BY provider,site,region`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boards []CollectorBoard
	for rows.Next() {
		board, err := scanCollectorBoard(rows)
		if err != nil {
			return nil, err
		}
		boards = append(boards, board)
	}
	return boards, rows.Err()
}

// ClaimDueCollectorBoard leases exactly one configured due board. The update
// is atomic, so overlapping processes cannot scan the same board concurrently.
func (s *Store) ClaimDueCollectorBoard(ctx context.Context, now time.Time, lease time.Duration) (CollectorBoard, bool, error) {
	if lease <= 0 || lease > 10*time.Minute {
		return CollectorBoard{}, false, ErrInvalid
	}
	token, err := randomID()
	if err != nil {
		return CollectorBoard{}, false, err
	}
	board, err := scanCollectorBoard(s.db.QueryRowContext(ctx, `UPDATE collector_boards
  SET lease_token=?,lease_until=?,updated_at=?
  WHERE id=(SELECT id FROM collector_boards WHERE enabled=1 AND next_scan_at<=?
    AND (lease_until IS NULL OR lease_until<=?) ORDER BY next_scan_at,id LIMIT 1)
  RETURNING `+collectorBoardColumns, token, jobTime(now.Add(lease)), jobTime(now), jobTime(now), jobTime(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return CollectorBoard{}, false, nil
	}
	return board, err == nil, err
}

type CollectorBoardResult struct {
	NextOffset  int
	ErrorCode   string // Empty on success; safe machine code on failure.
	WarningCode string // Safe code for skipped invalid postings on an otherwise successful page.
}

func (s *Store) FinishCollectorBoard(ctx context.Context, claim CollectorBoard, result CollectorBoardResult, now time.Time) (bool, error) {
	if claim.ID == "" || claim.LeaseToken == "" || result.NextOffset < 0 ||
		(result.ErrorCode != "" && !collectorErrorPattern.MatchString(result.ErrorCode)) ||
		(result.WarningCode != "" && !collectorErrorPattern.MatchString(result.WarningCode)) {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var nextOffset = result.NextOffset
	if result.ErrorCode != "" {
		nextOffset = claim.NextOffset
	}
	finishedAt := jobTime(now)
	nextScan := now.Add(time.Duration(claim.IntervalMinutes) * time.Minute)
	if result.ErrorCode == "" && nextOffset > 0 {
		nextScan = now.Add(collectorContinuationDelay)
	}
	lastCode := result.ErrorCode
	if lastCode == "" {
		lastCode = result.WarningCode
	}
	var lastSuccess any
	if result.ErrorCode == "" {
		lastSuccess = finishedAt
	}
	res, err := tx.ExecContext(ctx, `UPDATE collector_boards SET next_offset=?,next_scan_at=?,
  lease_token=NULL,lease_until=NULL,last_run_at=?,last_success_at=COALESCE(?,last_success_at),
  last_error_code=?,updated_at=? WHERE id=? AND lease_token=? AND lease_until>?`,
		nextOffset, jobTime(nextScan), finishedAt,
		lastSuccess, optionalText(lastCode), finishedAt, claim.ID, claim.LeaseToken, finishedAt)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	auditID, err := randomID()
	if err != nil {
		return false, err
	}
	operation := "collector.board.scan"
	if result.ErrorCode != "" {
		operation = "collector.board.scan_failed"
	} else if result.WarningCode != "" {
		operation = "collector.board.scan_with_rejections"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, "system", "collector", operation, "collector_board", claim.ID, finishedAt)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
