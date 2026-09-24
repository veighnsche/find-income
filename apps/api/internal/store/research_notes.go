package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ResearchNote is investigation memory: model-authored, never evidence. Reads
// are not run-scoped; later runs see earlier briefs' notes.
type ResearchNote struct {
	ID                  string
	ActorKind           string
	ActorID             string
	RoundID             string
	BriefProfileVersion int64
	BriefRubricVersion  string
	Intent              string
	UsefulnessJSON      string
	CoverageJSON        string
	OverlapRefsJSON     string
	Conclusion          string
	EvidenceRefsJSON    string
	OutstandingJSON     string
	SupersedesID        string
	CreatedAt           string
}

// ResearchNoteInput carries the caller-supplied note fields. JSON fields left
// empty default to '{}' (usefulness, coverage) or '[]' (overlap, evidence,
// outstanding).
type ResearchNoteInput struct {
	RoundID             string
	BriefProfileVersion int64
	BriefRubricVersion  string
	Intent              string
	UsefulnessJSON      string
	CoverageJSON        string
	OverlapRefsJSON     string
	Conclusion          string
	EvidenceRefsJSON    string
	OutstandingJSON     string
	SupersedesID        string
}

const researchNoteColumns = `id,actor_kind,actor_id,round_id,brief_profile_version,` +
	`brief_rubric_version,intent,usefulness_json,coverage_json,overlap_refs_json,` +
	`conclusion,evidence_refs_json,outstanding_json,supersedes_id,created_at`

func defaultJSON(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// InsertResearchNote appends one note. Notes never share a transaction with
// business writes (T03 §4 T-note).
func InsertResearchNote(ctx context.Context, db ResearchDB, actor Actor, in ResearchNoteInput) (ResearchNote, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return ResearchNote{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if in.RoundID == "" || strings.TrimSpace(in.Intent) == "" {
		return ResearchNote{}, fmt.Errorf("%w: round/intent required", ErrInvalid)
	}
	if in.BriefProfileVersion <= 0 || in.BriefRubricVersion == "" {
		return ResearchNote{}, fmt.Errorf("%w: brief profile/rubric version required", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return ResearchNote{}, err
	}
	now := recordNow()
	n := ResearchNote{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID, RoundID: in.RoundID,
		BriefProfileVersion: in.BriefProfileVersion, BriefRubricVersion: in.BriefRubricVersion,
		Intent: in.Intent, UsefulnessJSON: defaultJSON(in.UsefulnessJSON, "{}"),
		CoverageJSON:    defaultJSON(in.CoverageJSON, "{}"),
		OverlapRefsJSON: defaultJSON(in.OverlapRefsJSON, "[]"),
		Conclusion:      in.Conclusion, EvidenceRefsJSON: defaultJSON(in.EvidenceRefsJSON, "[]"),
		OutstandingJSON: defaultJSON(in.OutstandingJSON, "[]"),
		SupersedesID:    in.SupersedesID, CreatedAt: now,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO research_notes
  (id,actor_kind,actor_id,round_id,brief_profile_version,brief_rubric_version,intent,
   usefulness_json,coverage_json,overlap_refs_json,conclusion,evidence_refs_json,
   outstanding_json,supersedes_id,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ActorKind, n.ActorID, n.RoundID, n.BriefProfileVersion, n.BriefRubricVersion,
		n.Intent, n.UsefulnessJSON, n.CoverageJSON, n.OverlapRefsJSON, n.Conclusion,
		n.EvidenceRefsJSON, n.OutstandingJSON, nullString(n.SupersedesID), n.CreatedAt)
	if err != nil {
		return ResearchNote{}, err
	}
	return n, nil
}

// GetResearchNote loads one note by id.
func GetResearchNote(ctx context.Context, r Reader, id string) (ResearchNote, error) {
	row := r.QueryRowContext(ctx, `SELECT `+researchNoteColumns+`
  FROM research_notes WHERE id=?`, id)
	n, err := scanResearchNoteRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchNote{}, ErrNotFound
	}
	return n, err
}

// ListResearchNotesByRound returns a round's notes in creation order.
func ListResearchNotesByRound(ctx context.Context, r Reader, roundID string) ([]ResearchNote, error) {
	rows, err := r.QueryContext(ctx, `SELECT `+researchNoteColumns+`
  FROM research_notes WHERE round_id=? ORDER BY created_at,id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchNote
	for rows.Next() {
		var n ResearchNote
		if err := scanResearchNoteInto(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SearchResearchNotes runs an FTS5 overlap query over (intent, coverage,
// conclusion). Matches carry uncertainty: the UI must not claim perfect
// equivalence between notes (T03 §1.3).
func SearchResearchNotes(ctx context.Context, r Reader, query string, limit int) ([]ResearchNote, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: search query required", ErrInvalid)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.QueryContext(ctx, `SELECT n.`+
		strings.ReplaceAll(researchNoteColumns, ",", ",n.")+`
  FROM research_notes n JOIN research_notes_fts f ON n.rowid = f.rowid
  WHERE research_notes_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchNote
	for rows.Next() {
		var n ResearchNote
		if err := scanResearchNoteInto(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func scanResearchNoteRow(row *sql.Row) (ResearchNote, error) {
	var n ResearchNote
	return n, scanResearchNoteInto(row, &n)
}

func scanResearchNoteInto(s researchRequestScanner, n *ResearchNote) error {
	var supersedes sql.NullString
	if err := s.Scan(&n.ID, &n.ActorKind, &n.ActorID, &n.RoundID,
		&n.BriefProfileVersion, &n.BriefRubricVersion, &n.Intent,
		&n.UsefulnessJSON, &n.CoverageJSON, &n.OverlapRefsJSON, &n.Conclusion,
		&n.EvidenceRefsJSON, &n.OutstandingJSON, &supersedes, &n.CreatedAt); err != nil {
		return err
	}
	n.SupersedesID = supersedes.String
	return nil
}
