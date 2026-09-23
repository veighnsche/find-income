package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var collectorSitePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)

//go:embed default-collector-boards.json
var defaultCollectorBoardsFS embed.FS

type CollectorBoardInput struct {
	Provider        string
	Site            string
	Region          string
	DisplayName     string
	Enabled         bool
	IntervalMinutes int
}

type CollectorBoard struct {
	ID string
	CollectorBoardInput
	OfficialCareersURL string
	VerifiedAt         string
	NextScanAt         time.Time
	NextOffset         int
	LeaseToken         string
	LeaseUntil         time.Time
	LastRunAt          time.Time
	LastSuccessAt      time.Time
	LastErrorCode      string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func validateCollectorBoard(input CollectorBoardInput) error {
	if input.Provider != "lever" || !collectorSitePattern.MatchString(input.Site) ||
		(input.Region != "global" && input.Region != "eu") ||
		(input.DisplayName != "" && (!boundedNonempty(input.DisplayName, 200) || strings.TrimSpace(input.DisplayName) != input.DisplayName)) ||
		input.IntervalMinutes < 15 || input.IntervalMinutes > 10080 {
		return fmt.Errorf("%w: supported provider, site, region and interval required", ErrInvalid)
	}
	return nil
}

func seedCollectorBoards(ctx context.Context, tx *sql.Tx) error {
	data, err := defaultCollectorBoardsFS.ReadFile("default-collector-boards.json")
	if err != nil {
		return err
	}
	var seed struct {
		VerifiedAt string `json:"verifiedAt"`
		Boards     []struct {
			Company            string `json:"company"`
			Provider           string `json:"provider"`
			Site               string `json:"site"`
			Region             string `json:"region"`
			OfficialCareersURL string `json:"officialCareersUrl"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(data, &seed); err != nil {
		return err
	}
	if !validInstant(seed.VerifiedAt) || len(seed.Boards) == 0 {
		return fmt.Errorf("%w: verified initial collector boards required", ErrInvalid)
	}
	now := jobTime(time.Now())
	for _, item := range seed.Boards {
		input := CollectorBoardInput{Provider: item.Provider, Site: item.Site, Region: item.Region,
			DisplayName: item.Company, Enabled: true, IntervalMinutes: 60}
		if err := validateCollectorBoard(input); err != nil {
			return err
		}
		if item.OfficialCareersURL == "" {
			return fmt.Errorf("%w: official careers URL required", ErrInvalid)
		}
		if err := validateWebURL(item.OfficialCareersURL); err != nil {
			return err
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO collector_boards
  (id,provider,site,region,display_name,official_careers_url,verified_at,
   enabled,interval_minutes,next_scan_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,1,60,?,?,?)`, id, input.Provider, input.Site, input.Region,
			input.DisplayName, item.OfficialCareersURL, seed.VerifiedAt, now, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

const collectorBoardColumns = `id,provider,site,region,enabled,interval_minutes,next_scan_at,next_offset,
  lease_token,lease_until,last_run_at,last_success_at,last_error_code,revision,created_at,updated_at,
  display_name,official_careers_url,verified_at`

func scanCollectorBoard(row rowScanner) (CollectorBoard, error) {
	var board CollectorBoard
	var enabled int
	var nextScan, created, updated string
	var token, leaseUntil, lastRun, lastSuccess, lastError, careersURL, verifiedAt sql.NullString
	err := row.Scan(&board.ID, &board.Provider, &board.Site, &board.Region, &enabled,
		&board.IntervalMinutes, &nextScan, &board.NextOffset, &token, &leaseUntil,
		&lastRun, &lastSuccess, &lastError, &board.Revision, &created, &updated,
		&board.DisplayName, &careersURL, &verifiedAt)
	if err != nil {
		return CollectorBoard{}, err
	}
	board.Enabled = enabled == 1
	board.LeaseToken, board.LastErrorCode = token.String, lastError.String
	board.OfficialCareersURL, board.VerifiedAt = careersURL.String, verifiedAt.String
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
	if input.DisplayName == "" {
		input.DisplayName = input.Site
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
	  (id,provider,site,region,display_name,enabled,interval_minutes,next_scan_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,site,region) DO NOTHING`,
		id, input.Provider, input.Site, input.Region, input.DisplayName, input.Enabled,
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
