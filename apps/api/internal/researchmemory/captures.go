package researchmemory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Captures resolves trusted receipts and opens immutable captures. It
// implements researchcontract.CaptureReader; lanes B/D/A consume it. A model
// assertion is never a receipt: resolution requires both the executor-issued
// receipt file and a referencing observation row, cross-checked.
type Captures struct {
	db        *store.Store
	artifacts *ArtifactStore
}

var _ researchcontract.CaptureReader = (*Captures)(nil)

// NewCaptures binds a capture reader to its store and artifact root.
func NewCaptures(db *store.Store, artifacts *ArtifactStore) (*Captures, error) {
	if db == nil || artifacts == nil {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"deps", "captures requires a store and artifact root")
	}
	return &Captures{db: db, artifacts: artifacts}, nil
}

// ResolveReceipt returns the backend-issued receipt for an opaque receipt
// id. Unknown or forged ids fail with outcome not_found; a receipt file
// that disagrees with its recorded observation fails with outcome invalid.
func (c *Captures) ResolveReceipt(ctx context.Context, receiptID string) (researchcontract.ExecutionReceipt, error) {
	var rec researchcontract.ExecutionReceipt
	rec, err := c.artifacts.OpenReceipt(receiptID)
	if err != nil {
		return rec, err
	}
	var obs store.ResearchObservation
	var req store.ResearchRequest
	err = c.db.Read(ctx, func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservationByReceipt(ctx, r, receiptID)
		if errors.Is(err, store.ErrNotFound) {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"receipt", "receipt "+receiptID+" was never recorded")
		}
		if err != nil {
			return err
		}
		req, err = store.GetResearchRequestByID(ctx, r, obs.RequestID)
		return err
	})
	if err != nil {
		return researchcontract.ExecutionReceipt{}, err
	}
	if rec.Fingerprint != "" && rec.Fingerprint != req.Fingerprint {
		return researchcontract.ExecutionReceipt{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "receipt",
			"receipt fingerprint disagrees with the recorded request")
	}
	if rec.CaptureID != "" && obs.CaptureID != "" {
		var contentSHA string
		err := c.db.Read(ctx, func(r store.Reader) error {
			capt, err := store.GetSourceCapture(ctx, r, obs.CaptureID)
			if err != nil {
				return err
			}
			contentSHA = capt.ContentSHA256
			return nil
		})
		if err != nil {
			return researchcontract.ExecutionReceipt{}, err
		}
		if rec.CaptureID != contentSHA {
			return researchcontract.ExecutionReceipt{}, researchcontract.NewError(
				researchcontract.OutcomeInvalid, "receipt",
				"receipt captureId disagrees with the recorded capture")
		}
	}
	return rec, nil
}

// OpenCapture returns the capture descriptor and a reader over the stored
// bytes. The id accepts either the retrieval row id or the content sha256
// (storage is row-per-retrieval; the contract view is content-addressed).
// Integrity failures fail with outcome invalid; the caller closes the reader.
// Historical captures open without any live lease.
func (c *Captures) OpenCapture(ctx context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	var empty researchcontract.Capture
	capt, err := c.resolveCapture(ctx, captureID)
	if err != nil {
		return empty, nil, err
	}
	data, err := c.artifacts.OpenBlob(capt.ArtifactRef)
	if err != nil {
		return empty, nil, err
	}
	if len(data) != int(capt.ByteLength) {
		return empty, nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"capture", "capture byte length disagrees with the recorded row")
	}
	var redirects []string
	if err := json.Unmarshal([]byte(capt.RedirectChainJSON), &redirects); err != nil {
		return empty, nil, err
	}
	var extra map[string]string
	if capt.ExtentJSON != "" && capt.ExtentJSON != "{}" {
		extra = map[string]string{"extent": capt.ExtentJSON}
	}
	observedAt, _ := parseTime(capt.RetrievedAt)
	desc := researchcontract.Capture{
		ID:           capt.ContentSHA256,
		Status:       httpStatusText(capt.HTTPStatus),
		FinalURL:     capt.FinalURL,
		Redirects:    redirects,
		ContentType:  capt.MediaType,
		Complete:     capt.Completeness == store.CaptureComplete,
		ObservedAt:   observedAt,
		SHA256:       capt.ContentSHA256,
		Bytes:        capt.ByteLength,
		Provenance:   capt.Provenance,
		IsSnippet:    capt.IsSnippet,
		Completeness: capt.Completeness,
		Executor:     capt.Executor,
		Extra:        extra,
	}
	// Bind the retrieval behind the content view: the operation/fingerprint
	// come from the newest observation citing this retrieval, when any.
	_ = c.db.Read(ctx, func(r store.Reader) error {
		rows, err := r.QueryContext(ctx, `SELECT request_id FROM research_observations
  WHERE capture_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, capt.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			return err
		}
		req, err := store.GetResearchRequestByID(ctx, r, requestID)
		if err != nil {
			return err
		}
		desc.Operation = req.Request.Operation
		desc.Fingerprint = req.Fingerprint
		desc.Request = req.Request
		return rows.Err()
	})
	return desc, io.NopCloser(bytes.NewReader(data)), nil
}

// resolveCapture loads a capture row by retrieval id or content sha256
// (newest retrieval wins for content lookups). Note and record ids never
// resolve here: only source_captures rows are evidence (provenance firewall).
func (c *Captures) resolveCapture(ctx context.Context, captureID string) (store.SourceCapture, error) {
	if captureID == "" {
		return store.SourceCapture{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "captureId", "unknown capture id")
	}
	var capt store.SourceCapture
	err := c.db.Read(ctx, func(r store.Reader) error {
		row, err := store.GetSourceCapture(ctx, r, captureID)
		if err == nil {
			capt = row
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if !hexSHA256Shape.MatchString(captureID) {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"captureId", "unknown capture "+captureID)
		}
		rows, err := store.ListSourceCapturesByContent(ctx, r, captureID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"captureId", "unknown capture "+captureID)
		}
		capt = rows[0]
		return nil
	})
	return capt, err
}

// Info returns the stored row behind a capture id (retrieval id or content
// sha256), for metadata responses. It performs no byte verification; pair
// it with OpenCapture when integrity matters.
func (c *Captures) Info(ctx context.Context, captureID string) (store.SourceCapture, error) {
	return c.resolveCapture(ctx, captureID)
}

// ObservationForReceipt returns the observation recorded under a receipt id.
func (c *Captures) ObservationForReceipt(ctx context.Context, receiptID string) (store.ResearchObservation, error) {
	var obs store.ResearchObservation
	err := c.db.Read(ctx, func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservationByReceipt(ctx, r, receiptID)
		if errors.Is(err, store.ErrNotFound) {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"receipt", "receipt "+receiptID+" was never recorded")
		}
		return err
	})
	return obs, err
}

// VerifyExcerpt binds an excerpt to its immutable version: it opens the
// capture, bounds-checks the span and verifies the excerpt sha256 over the
// exact stored bytes. Redaction/display projections never alter the binding.
func (c *Captures) VerifyExcerpt(ctx context.Context, captureID string, spanStart, spanEnd int64, excerptSHA256 string) error {
	if spanStart < 0 || spanEnd <= spanStart {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"excerpt", "excerpt span is out of bounds")
	}
	if !hexSHA256Shape.MatchString(excerptSHA256) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"excerptSHA256", "excerpt sha256 must be 64 lowercase hex chars")
	}
	capt, err := c.resolveCapture(ctx, captureID)
	if err != nil {
		return err
	}
	if spanEnd > capt.ByteLength {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"excerpt", "excerpt span exceeds the captured bytes")
	}
	data, err := c.artifacts.OpenBlob(capt.ArtifactRef)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data[spanStart:spanEnd])
	if hex.EncodeToString(sum[:]) != excerptSHA256 {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"excerpt", "excerpt bytes do not match the quoted hash (unsupported quote)")
	}
	return nil
}

// RequireFullCapture rejects snippet or explicitly incomplete captures for
// complete-vacancy claims (field check supplied for D's save validation;
// enforced here for evidence_capture with assertComplete).
func (c *Captures) RequireFullCapture(ctx context.Context, captureID string) error {
	capt, err := c.resolveCapture(ctx, captureID)
	if err != nil {
		return err
	}
	if capt.IsSnippet {
		return researchcontract.NewError(researchcontract.OutcomeCaptureIncomplete,
			"captureId", "a search snippet cannot back a complete vacancy description")
	}
	if capt.Completeness != store.CaptureComplete {
		return researchcontract.NewError(researchcontract.OutcomeCaptureIncomplete,
			"captureId", "an explicitly "+capt.Completeness+" capture cannot back a complete claim")
	}
	return nil
}

func httpStatusText(status *int64) string {
	if status == nil {
		return ""
	}
	return strconv.FormatInt(*status, 10)
}
