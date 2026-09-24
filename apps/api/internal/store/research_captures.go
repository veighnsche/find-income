package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Capture completeness values (T03 §1.4). Truncation is explicit, never silent.
const (
	CaptureComplete  = "complete"
	CaptureTruncated = "truncated"
	CapturePaginated = "paginated"
	CapturePartial   = "partial"
)

// SourceCapture is one immutable artifact row. IDs are opaque per retrieval
// (row-per-retrieval, T03 P3 default); content_sha256 is the dedup/index key
// at the blob layer, not a unique constraint.
type SourceCapture struct {
	ID                string
	ContentSHA256     string
	ArtifactRef       string
	ByteLength        int64
	MediaType         string
	HTTPStatus        *int64
	OriginalURL       string
	FinalURL          string
	RedirectChainJSON string
	RetrievedAt       string
	Provenance        researchcontract.ProvenanceKind
	Completeness      string
	ExtentJSON        string
	Executor          researchcontract.ExecutorIdentity
	RoleID            string
	IsSnippet         bool
	CreatedAt         string
}

// SourceCaptureInput carries the caller-supplied capture fields. Artifact
// bytes must already be persisted by the executor before the row commits
// (T03 §4 T-observe); paths stay outside the model-writable workspace and
// web root (T06 §1).
type SourceCaptureInput struct {
	ContentSHA256     string
	ArtifactRef       string
	ByteLength        int64
	MediaType         string
	HTTPStatus        *int64
	OriginalURL       string
	FinalURL          string
	RedirectChainJSON string
	RetrievedAt       string
	Provenance        researchcontract.ProvenanceKind
	Completeness      string
	ExtentJSON        string
	Executor          researchcontract.ExecutorIdentity
	RoleID            string
	IsSnippet         bool
}

const sourceCaptureColumns = `id,content_sha256,artifact_ref,byte_length,media_type,` +
	`http_status,original_url,final_url,redirect_chain_json,retrieved_at,` +
	`provenance_kind,completeness,extent_json,executor_identity_json,role_id,` +
	`is_snippet,created_at`

func validCaptureProvenance(p researchcontract.ProvenanceKind) bool {
	switch p {
	case researchcontract.ProvenanceFetchedResponse, researchcontract.ProvenanceRenderedDOM,
		researchcontract.ProvenanceSearchResult, researchcontract.ProvenanceOwnerStatement:
		return true
	}
	return false
}

// InsertSourceCapture appends one immutable artifact row.
func InsertSourceCapture(ctx context.Context, db ResearchDB, in SourceCaptureInput) (SourceCapture, error) {
	if len(in.ContentSHA256) != 64 || in.ArtifactRef == "" {
		return SourceCapture{}, fmt.Errorf("%w: content sha256/artifact_ref required", ErrInvalid)
	}
	if in.ByteLength < 0 {
		return SourceCapture{}, fmt.Errorf("%w: negative byte length", ErrInvalid)
	}
	if !validCaptureProvenance(in.Provenance) {
		return SourceCapture{}, fmt.Errorf("%w: unknown capture provenance %q", ErrInvalid, in.Provenance)
	}
	if !validEnum(in.Completeness, CaptureComplete, CaptureTruncated, CapturePaginated, CapturePartial) {
		return SourceCapture{}, fmt.Errorf("%w: unknown completeness %q", ErrInvalid, in.Completeness)
	}
	if in.Executor.Backend == "" {
		return SourceCapture{}, fmt.Errorf("%w: executor backend required", ErrInvalid)
	}
	executorRaw, err := json.Marshal(in.Executor)
	if err != nil {
		return SourceCapture{}, err
	}
	id, err := randomID()
	if err != nil {
		return SourceCapture{}, err
	}
	now := recordNow()
	retrieved := in.RetrievedAt
	if retrieved == "" {
		retrieved = now
	}
	var status sql.NullInt64
	if in.HTTPStatus != nil {
		status = sql.NullInt64{Int64: *in.HTTPStatus, Valid: true}
	}
	c := SourceCapture{
		ID: id, ContentSHA256: in.ContentSHA256, ArtifactRef: in.ArtifactRef,
		ByteLength: in.ByteLength, MediaType: in.MediaType, HTTPStatus: in.HTTPStatus,
		OriginalURL: in.OriginalURL, FinalURL: in.FinalURL,
		RedirectChainJSON: defaultJSON(in.RedirectChainJSON, "[]"), RetrievedAt: retrieved,
		Provenance: in.Provenance, Completeness: in.Completeness,
		ExtentJSON: defaultJSON(in.ExtentJSON, "{}"), Executor: in.Executor,
		RoleID: in.RoleID, IsSnippet: in.IsSnippet, CreatedAt: now,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO source_captures
  (id,content_sha256,artifact_ref,byte_length,media_type,http_status,original_url,
   final_url,redirect_chain_json,retrieved_at,provenance_kind,completeness,
   extent_json,executor_identity_json,role_id,is_snippet,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ContentSHA256, c.ArtifactRef, c.ByteLength, c.MediaType, status,
		nullString(c.OriginalURL), nullString(c.FinalURL), c.RedirectChainJSON,
		c.RetrievedAt, string(c.Provenance), c.Completeness, c.ExtentJSON,
		string(executorRaw), nullString(c.RoleID), boolInt(c.IsSnippet), c.CreatedAt)
	if err != nil {
		return SourceCapture{}, err
	}
	return c, nil
}

// GetSourceCapture loads one capture by id.
func GetSourceCapture(ctx context.Context, r Reader, id string) (SourceCapture, error) {
	row := r.QueryRowContext(ctx, `SELECT `+sourceCaptureColumns+`
  FROM source_captures WHERE id=?`, id)
	c, err := scanSourceCaptureRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceCapture{}, ErrNotFound
	}
	return c, err
}

// ListSourceCapturesByContent returns every retrieval of the same bytes,
// newest first (historical-reuse lookup, D §3.2).
func ListSourceCapturesByContent(ctx context.Context, r Reader, contentSHA256 string) ([]SourceCapture, error) {
	return listSourceCaptures(ctx, r, `WHERE content_sha256=? ORDER BY retrieved_at DESC,id DESC`, contentSHA256)
}

// ListSourceCapturesByURL returns the retrieval history of one URL, newest
// first (reused-URL history, D §3.2 R03).
func ListSourceCapturesByURL(ctx context.Context, r Reader, originalURL string) ([]SourceCapture, error) {
	return listSourceCaptures(ctx, r, `WHERE original_url=? ORDER BY retrieved_at DESC,id DESC`, originalURL)
}

func listSourceCaptures(ctx context.Context, r Reader, suffix string, args ...any) ([]SourceCapture, error) {
	rows, err := r.QueryContext(ctx, `SELECT `+sourceCaptureColumns+
		` FROM source_captures `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceCapture
	for rows.Next() {
		var c SourceCapture
		if err := scanSourceCaptureInto(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanSourceCaptureRow(row *sql.Row) (SourceCapture, error) {
	var c SourceCapture
	return c, scanSourceCaptureInto(row, &c)
}

func scanSourceCaptureInto(s researchRequestScanner, c *SourceCapture) error {
	var provenance string
	var status sql.NullInt64
	var original, final, role sql.NullString
	var executorRaw string
	var isSnippet int64
	if err := s.Scan(&c.ID, &c.ContentSHA256, &c.ArtifactRef, &c.ByteLength,
		&c.MediaType, &status, &original, &final, &c.RedirectChainJSON,
		&c.RetrievedAt, &provenance, &c.Completeness, &c.ExtentJSON,
		&executorRaw, &role, &isSnippet, &c.CreatedAt); err != nil {
		return err
	}
	if status.Valid {
		c.HTTPStatus = &status.Int64
	}
	c.OriginalURL = original.String
	c.FinalURL = final.String
	c.Provenance = researchcontract.ProvenanceKind(provenance)
	if err := json.Unmarshal([]byte(executorRaw), &c.Executor); err != nil {
		return err
	}
	c.RoleID = role.String
	c.IsSnippet = isSnippet == 1
	return nil
}
