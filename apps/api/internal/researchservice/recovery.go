// Server run-recovery reads (C1/D3): relevant-run lookup across states,
// latest terminal run, and newest-first history for server-backed
// restoration and stable run deep links. Every method is a pure durable
// read: no commissions, no resumes, no journal writes, zero model calls.
//
// The queries below read the rounds table through the store's query-only
// Reader because the list/history query itself is I-owned shared surface
// (store/rounds.go): the coordinator should relocate this exact selection
// into a store ListRounds method when integrating GET /api/v1/rounds, and
// these methods can delegate to it without changing behavior.
package researchservice

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var _ httpapi.ResearchRecoveryService = (*Service)(nil)

// Known run states for the recovery list filter, byte-identical to the
// stored round states. There is no stored "stopped" or "empty" state: a
// stopped run rests paused, and emptiness derives from the run's findings
// read (zero findings), not from this list.
var recoveryStates = map[string]bool{
	"queued": true, "running": true, "awaiting_input": true, "stopping": true,
	"paused": true, "completed": true, "failed": true,
}

const (
	recoveryDefaultLimit = 25
	recoveryMaxLimit     = 100
)

// ListResearchRuns implements httpapi.ResearchRecoveryService.
func (s *Service) ListResearchRuns(ctx context.Context, actor store.Actor, opts httpapi.RunHistoryOptions) (httpapi.RunHistoryPage, error) {
	var page httpapi.RunHistoryPage
	if actor.Kind == "" || actor.ID == "" {
		return page, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "listing runs requires an authenticated actor")
	}
	if len(opts.Outcome) > 100 {
		return page, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"outcome", "outcome filter limited to 100 chars")
	}
	for _, state := range opts.States {
		if !recoveryStates[state] {
			return page, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"state", "unknown run state "+state)
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = recoveryDefaultLimit
	}
	if limit > recoveryMaxLimit {
		limit = recoveryMaxLimit
	}
	var cursorAt, cursorID string
	if opts.Cursor != "" {
		parts := strings.SplitN(opts.Cursor, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
			len(parts[0]) > 64 || len(parts[1]) > 128 {
			return page, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"cursor", "unknown run-history cursor; re-page from the head")
		}
		cursorAt, cursorID = parts[0], parts[1]
	}
	query := `SELECT id,request_key,intent,outcome,state,stop_reason,created_at,updated_at,completed_at
  FROM rounds WHERE (updated_at < ? OR (updated_at = ? AND id < ?))`
	args := []any{dirtyFuture(), dirtyFuture(), dirtyFuture()}
	if opts.Cursor != "" {
		args = []any{cursorAt, cursorAt, cursorID}
	}
	if opts.Outcome != "" {
		query += ` AND outcome=?`
		args = append(args, opts.Outcome)
	}
	if len(opts.States) > 0 {
		query += ` AND state IN (?` + strings.Repeat(",?", len(opts.States)-1) + `)`
		for _, state := range opts.States {
			args = append(args, state)
		}
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	page.Items = []httpapi.RunHistoryItem{}
	if err := s.db.Read(ctx, func(r store.Reader) error {
		rows, err := r.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRunHistoryItem(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	}); err != nil {
		return httpapi.RunHistoryPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = last.UpdatedAt + "|" + last.RunID
	}
	return page, nil
}

// LatestTerminalResearchRun implements httpapi.ResearchRecoveryService: the
// newest completed/failed run for one outcome (or every outcome when
// outcome is empty). No terminal run yet is not_found, never an empty or
// invented item.
func (s *Service) LatestTerminalResearchRun(ctx context.Context, actor store.Actor, outcome string) (httpapi.RunHistoryItem, error) {
	var item httpapi.RunHistoryItem
	if actor.Kind == "" || actor.ID == "" {
		return item, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "reading the latest run requires an authenticated actor")
	}
	if len(outcome) > 100 {
		return item, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"outcome", "outcome filter limited to 100 chars")
	}
	query := `SELECT id,request_key,intent,outcome,state,stop_reason,created_at,updated_at,completed_at
  FROM rounds WHERE state IN ('completed','failed')`
	var args []any
	if outcome != "" {
		query += ` AND outcome=?`
		args = append(args, outcome)
	}
	query += ` ORDER BY completed_at DESC, created_at DESC, id DESC LIMIT 1`
	if err := s.db.Read(ctx, func(r store.Reader) error {
		var err error
		item, err = scanRunHistoryItem(r.QueryRowContext(ctx, query, args...))
		return err
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return item, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"run", "no terminal run recorded")
		}
		return item, err
	}
	return item, nil
}

// dirtyFuture sorts after every real timestamp so the unfiltered head page
// needs no separate query shape: without a cursor every row is older.
func dirtyFuture() string { return "9999-12-31T23:59:59.999999999Z" }

func scanRunHistoryItem(row interface {
	Scan(...any) error
}) (httpapi.RunHistoryItem, error) {
	var item httpapi.RunHistoryItem
	var completed sql.NullString
	err := row.Scan(&item.RunID, &item.RequestKey, &item.Intent, &item.Outcome,
		&item.State, &item.StopReason, &item.CreatedAt, &item.UpdatedAt, &completed)
	if err != nil {
		return httpapi.RunHistoryItem{}, err
	}
	item.CompletedAt = completed.String
	return item, nil
}
