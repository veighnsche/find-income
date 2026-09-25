package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MuseRunReport is the durable terminal record of one local CLI session
// run: outcome, validated saves and per-vacancy classification errors. Reads
// serve from this row plus findings, so reloads commission nothing.
type MuseRunReport struct {
	RunRef         string
	RoundID        string
	Tier           string
	Outcome        string
	Detail         string
	SavedRefs      []string
	ClassifyErrors []string
	UpdatedAt      time.Time
}

var validMuseOutcomes = map[string]bool{
	"completed": true, "stopped": true, "failed": true,
	"crashed": true, "expired": true,
}

// SaveMuseRunReport upserts one terminal row.
func (s *Store) SaveMuseRunReport(ctx context.Context, report MuseRunReport) error {
	if report.RunRef == "" || (report.Tier != "contributor" && report.Tier != "standard") ||
		!validMuseOutcomes[report.Outcome] {
		return errors.New("museruns: run ref, tier and outcome required")
	}
	saved, err := json.Marshal(report.SavedRefs)
	if err != nil {
		return err
	}
	if report.SavedRefs == nil {
		saved = []byte("[]")
	}
	classifyErrs, err := json.Marshal(report.ClassifyErrors)
	if err != nil {
		return err
	}
	if report.ClassifyErrors == nil {
		classifyErrs = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO muse_run_reports
		(run_ref,round_id,tier,outcome,detail,saved_refs_json,classify_errors_json,updated_at)
		VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (run_ref) DO UPDATE SET
		round_id=excluded.round_id,tier=excluded.tier,outcome=excluded.outcome,
		detail=excluded.detail,saved_refs_json=excluded.saved_refs_json,
		classify_errors_json=excluded.classify_errors_json,updated_at=excluded.updated_at`,
		report.RunRef, report.RoundID, report.Tier, report.Outcome, report.Detail,
		string(saved), string(classifyErrs), report.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// LoadMuseRunReport reads one terminal row. Unknown runs return ErrNotFound.
func (s *Store) LoadMuseRunReport(ctx context.Context, runRef string) (MuseRunReport, error) {
	var report MuseRunReport
	var savedJSON, errsJSON, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT run_ref,round_id,tier,outcome,detail,
		saved_refs_json,classify_errors_json,updated_at FROM muse_run_reports WHERE run_ref=?`, runRef).
		Scan(&report.RunRef, &report.RoundID, &report.Tier, &report.Outcome, &report.Detail,
			&savedJSON, &errsJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MuseRunReport{}, ErrNotFound
	}
	if err != nil {
		return MuseRunReport{}, err
	}
	if err := json.Unmarshal([]byte(savedJSON), &report.SavedRefs); err != nil {
		return MuseRunReport{}, err
	}
	if err := json.Unmarshal([]byte(errsJSON), &report.ClassifyErrors); err != nil {
		return MuseRunReport{}, err
	}
	report.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return report, err
}
