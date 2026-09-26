package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Route-dependent application artifacts (A4). Each produced item a route
// calls for persists as its own versioned record with content, basis and
// exact-edit fencing, independent of the pack bundle versions. Form
// values are the exception: they derive from current question answers
// at read time and are never stored, so the store rejects that type.
const (
	ArtifactCV           = "cv"
	ArtifactCoverLetter  = "cover_letter"
	ArtifactEmailSubject = "email_subject"
	ArtifactEmailBody    = "email_body"
	ArtifactFormValues   = "form_values"
)

const (
	artifactMaxContent = 65536
	artifactMaxBasis   = 8192
	artifactMaxRefs    = 200
)

func validRequestKey(key string) bool {
	return strings.TrimSpace(key) == key && key != "" && len(key) <= 200
}

func validArtifactType(value string) bool {
	switch value {
	case ArtifactCV, ArtifactCoverLetter, ArtifactEmailSubject, ArtifactEmailBody:
		return true
	default:
		return false
	}
}

// ArtifactAnswerRef pins one owner answer version behind artifact content.
type ArtifactAnswerRef struct {
	QuestionID    string `json:"questionId"`
	AnswerVersion int64  `json:"answerVersion"`
}

// ArtifactBasis records what an artifact was built from: verified owner
// fact ids, pinned answer versions and check source spans. Writers own
// the truth of the basis; the store only enforces shape and bounds.
type ArtifactBasis struct {
	FactIDs    []string            `json:"factIds"`
	AnswerRefs []ArtifactAnswerRef `json:"answerRefs"`
	CheckSpans []CheckSourceSpan   `json:"checkSpans"`
}

// ArtifactView is one immutable artifact version.
type ArtifactView struct {
	ID            string        `json:"id"`
	OpportunityID string        `json:"opportunityId"`
	Type          string        `json:"type"`
	Version       int64         `json:"version"`
	Content       string        `json:"content"`
	Basis         ArtifactBasis `json:"basis"`
	CreatedAt     string        `json:"createdAt"`
	CreatedBy     Actor         `json:"createdBy"`
}

// ArtifactSaveInput writes one artifact version. ExpectedVersion 0
// creates the first version; otherwise the write fences on the current
// version. RequestKey replays the same write.
type ArtifactSaveInput struct {
	RequestKey      string        `json:"requestKey"`
	ExpectedVersion int64         `json:"expectedVersion"`
	Type            string        `json:"type"`
	Content         string        `json:"content"`
	Basis           ArtifactBasis `json:"basis"`
}

func validArtifactBasis(basis ArtifactBasis) bool {
	if len(basis.FactIDs) > artifactMaxRefs || len(basis.AnswerRefs) > artifactMaxRefs ||
		len(basis.CheckSpans) > artifactMaxRefs {
		return false
	}
	for _, id := range basis.FactIDs {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return false
		}
	}
	for _, ref := range basis.AnswerRefs {
		if strings.TrimSpace(ref.QuestionID) == "" || len(ref.QuestionID) > 64 || ref.AnswerVersion < 0 {
			return false
		}
	}
	for _, span := range basis.CheckSpans {
		if !validCheckSpan(span) {
			return false
		}
	}
	raw, err := json.Marshal(basis)
	return err == nil && len(raw) <= artifactMaxBasis
}

// SaveOpportunityArtifact writes one artifact version. Retried request
// keys replay the stored version; a fenced write against a moved
// version conflicts instead of silently forking.
func (s *Store) SaveOpportunityArtifact(ctx context.Context, actor Actor, opportunityID string, input ArtifactSaveInput) (ArtifactView, bool, error) {
	if opportunityID == "" || !validRequestKey(input.RequestKey) || input.ExpectedVersion < 0 {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact save", ErrInvalid)
	}
	if !validArtifactType(input.Type) {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact type", ErrInvalid)
	}
	if strings.TrimSpace(input.Content) == "" || len([]rune(input.Content)) > artifactMaxContent {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact content", ErrInvalid)
	}
	if !validArtifactBasis(input.Basis) {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact basis", ErrInvalid)
	}
	var out ArtifactView
	created := false
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactView{}, false, err
	}
	defer tx.Rollback()
	err = func(tx *sql.Tx) error {
		var exists string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM opportunities WHERE id=?`, opportunityID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if replay, ok, err := scanArtifact(tx.QueryRowContext(ctx, `SELECT `+artifactColumns+
			` FROM opportunity_artifacts WHERE opportunity_id=? AND artifact_type=? AND request_key=?`,
			opportunityID, input.Type, input.RequestKey)); err == nil && ok {
			out = replay
			return nil
		} else if err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM opportunity_artifacts
			WHERE opportunity_id=? AND artifact_type=?`, opportunityID, input.Type).Scan(&current); err != nil {
			return err
		}
		if current != input.ExpectedVersion {
			return ErrConflict
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		now := utcNow()
		basisJSON, _ := json.Marshal(input.Basis)
		if _, err := tx.ExecContext(ctx, `INSERT INTO opportunity_artifacts
			(id,opportunity_id,artifact_type,version,request_key,content,basis_json,actor_kind,actor_id,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, input.Type, current+1, input.RequestKey,
			input.Content, string(basisJSON), actor.Kind, actor.ID, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrConflict
			}
			return err
		}
		out = ArtifactView{ID: id, OpportunityID: opportunityID, Type: input.Type,
			Version: current + 1, Content: input.Content, Basis: input.Basis,
			CreatedAt: now, CreatedBy: actor}
		created = true
		return nil
	}(tx)
	if err != nil {
		return ArtifactView{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactView{}, false, err
	}
	return out, created, nil
}

const artifactColumns = `id,opportunity_id,artifact_type,version,request_key,content,basis_json,actor_kind,actor_id,created_at`

func scanArtifact(row rowScanner) (ArtifactView, bool, error) {
	var view ArtifactView
	var requestKey, basisJSON, actorKind, actorID string
	if err := row.Scan(&view.ID, &view.OpportunityID, &view.Type, &view.Version,
		&requestKey, &view.Content, &basisJSON, &actorKind, &actorID, &view.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactView{}, false, nil
		}
		return ArtifactView{}, false, err
	}
	view.CreatedBy = Actor{Kind: actorKind, ID: actorID}
	if basisJSON == "" {
		basisJSON = "{}"
	}
	if err := json.Unmarshal([]byte(basisJSON), &view.Basis); err != nil {
		return ArtifactView{}, false, err
	}
	if view.Basis.FactIDs == nil {
		view.Basis.FactIDs = []string{}
	}
	if view.Basis.AnswerRefs == nil {
		view.Basis.AnswerRefs = []ArtifactAnswerRef{}
	}
	if view.Basis.CheckSpans == nil {
		view.Basis.CheckSpans = []CheckSourceSpan{}
	}
	return view, true, nil
}

// GetOpportunityArtifact reads the current version of one artifact type.
// Absent artifacts read as ErrNotFound: the set read maps those to held
// or not-required states instead of fabricating content.
func (s *Store) GetOpportunityArtifact(ctx context.Context, opportunityID, artifactType string) (ArtifactView, error) {
	view, ok, err := scanArtifact(s.db.QueryRowContext(ctx, `SELECT `+artifactColumns+
		` FROM opportunity_artifacts WHERE opportunity_id=? AND artifact_type=?
		ORDER BY version DESC LIMIT 1`, opportunityID, artifactType))
	if err != nil {
		return ArtifactView{}, err
	}
	if !ok {
		return ArtifactView{}, ErrNotFound
	}
	return view, nil
}

// ListOpportunityArtifacts reads the current version of every stored
// artifact type behind one opportunity.
func (s *Store) ListOpportunityArtifacts(ctx context.Context, opportunityID string) ([]ArtifactView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+artifactColumns+` FROM opportunity_artifacts
		WHERE opportunity_id=? AND version=(SELECT MAX(version) FROM opportunity_artifacts inner_rows
		WHERE inner_rows.opportunity_id=opportunity_artifacts.opportunity_id
		AND inner_rows.artifact_type=opportunity_artifacts.artifact_type) ORDER BY artifact_type`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactView{}
	for rows.Next() {
		view, ok, err := scanArtifact(rows)
		if err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, view)
	}
	return out, rows.Err()
}
