// Package recordsave implements the records_save tool (lane D, T18): atomic
// batch commits of research-sourced companies and opportunities.
//
// The handler verifies byte-dependent bindings before the transaction
// (capture integrity, span ranges, excerpt hashes over stored bytes via the
// CaptureReader) and the store commits authority, replay, assessment,
// identity and allowance rechecks plus all business writes in one
// BEGIN IMMEDIATE transaction. Match output stays advice until commit:
// revisions pinned in identity decisions are re-read inside the write.
package recordsave

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Briefs reports the current brief versions for run-scoped assessment
// binding. Doubles serve tests; the real reader binds at T23.
type Briefs interface {
	CurrentBrief(context.Context, string) (int64, string, error)
}

// Handler commits research record batches. It implements
// researchcontract.RecordSaver.
type Handler struct {
	Store     *store.Store
	Authority researchcontract.Authority
	Actor     store.Actor
	Captures  researchcontract.CaptureReader
	Briefs    Briefs
	Now       func() time.Time
}

var _ researchcontract.RecordSaver = (*Handler)(nil)

// NewHandler binds a saver to its store, authority, actor, capture reader
// and brief reader. The actor is the authenticated caller (never request
// body data); audit rows attribute to it.
func NewHandler(s *store.Store, auth researchcontract.Authority, actor store.Actor,
	caps researchcontract.CaptureReader, briefs Briefs) (*Handler, error) {
	h := &Handler{Store: s, Authority: auth, Actor: actor, Captures: caps, Briefs: briefs}
	if err := h.ready(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Handler) ready() error {
	if h == nil || h.Store == nil || h.Authority == nil || h.Captures == nil || h.Briefs == nil {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "",
			"records saver missing a required dependency")
	}
	if strings.TrimSpace(h.Actor.Kind) == "" || strings.TrimSpace(h.Actor.ID) == "" {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "actor", "records saver actor required")
	}
	return nil
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// batchDigestInput is the canonical replay payload: actor, run and items in
// order. Generation stays out (rotation fences via the authority check);
// credentials never enter (there are none in a save batch), so credential
// rotation can never turn a legitimate replay into a conflict.
type batchDigestInput struct {
	ActorKind string                      `json:"actorKind"`
	ActorID   string                      `json:"actorId"`
	RunID     string                      `json:"runId"`
	Items     []researchcontract.SaveItem `json:"items"`
}

// BatchDigest returns the stable payload digest addressing one batch under
// its idempotency key. Same key + same digest replays; same key with a
// different digest conflicts.
func BatchDigest(actor store.Actor, batch researchcontract.SaveBatch) (string, error) {
	raw, err := json.Marshal(batchDigestInput{
		ActorKind: actor.Kind, ActorID: actor.ID, RunID: batch.RunID, Items: batch.Items,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Save implements researchcontract.RecordSaver.
func (h *Handler) Save(ctx context.Context, batch researchcontract.SaveBatch) (researchcontract.SaveOutput, error) {
	if err := h.ready(); err != nil {
		return researchcontract.SaveOutput{}, err
	}
	if strings.TrimSpace(batch.RunID) == "" || batch.Generation < 1 {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"runId", "runId and generation >= 1 required")
	}
	if batch.IdempotencyKey == "" || len(batch.IdempotencyKey) > 200 ||
		strings.TrimSpace(batch.IdempotencyKey) != batch.IdempotencyKey {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"idempotencyKey", "idempotency key must be 1..200 chars without surrounding space")
	}
	if len(batch.Items) == 0 || len(batch.Items) > store.MaxRecordsSaveItems {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"batch", fmt.Sprintf("batch needs 1..%d items", store.MaxRecordsSaveItems))
	}
	// Fast pre-check without the write lock; the in-transaction check stays
	// authoritative against authority races.
	if err := h.Authority.Check(ctx, researchcontract.CheckInput{RunID: batch.RunID,
		Generation: batch.Generation, Permission: researchcontract.PermissionRecordWrite, Now: h.now()}); err != nil {
		return researchcontract.SaveOutput{}, err
	}
	profile, rubric, err := h.Briefs.CurrentBrief(ctx, batch.RunID)
	if err != nil {
		return researchcontract.SaveOutput{}, err
	}
	if profile < 1 || rubric == "" {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"brief", "current brief versions unavailable")
	}
	proved, conflicts := h.verifyCaptures(ctx, batch)
	if len(conflicts) > 0 {
		return researchcontract.SaveOutput{Outcome: conflicts[0].Code, Items: conflicts}, nil
	}
	digest, err := BatchDigest(h.Actor, batch)
	if err != nil {
		return researchcontract.SaveOutput{}, err
	}
	return h.Store.ApplyRecordsSave(ctx, h.Actor, batch, store.RecordsSaveVerified{
		Digest: digest, ProfileVersion: profile, RubricVersion: rubric, Captures: proved,
	})
}

// verifyCaptures opens every cited capture once and proves the
// byte-dependent bindings: stored-bytes sha, span ranges and excerpt
// hashes. Verified descriptors rebind inside the transaction by content
// sha; failures return item-specific invalid conflicts without opening the
// write transaction (observably identical to a rollback: no writes).
func (h *Handler) verifyCaptures(ctx context.Context, batch researchcontract.SaveBatch) (map[string]store.VerifiedSaveCapture, []researchcontract.SaveItemError) {
	proved := map[string]store.VerifiedSaveCapture{}
	bodies := map[string][]byte{}
	var conflicts []researchcontract.SaveItemError
	byCapture := map[string][]int{}
	for i, item := range batch.Items {
		for _, link := range item.EvidenceLinks {
			byCapture[link.CaptureID] = append(byCapture[link.CaptureID], i)
		}
	}
	fail := func(id string, detail string) {
		for _, i := range byCapture[id] {
			conflicts = append(conflicts, researchcontract.SaveItemError{
				Index: i, Code: researchcontract.OutcomeInvalid, Detail: detail})
		}
	}
	for id := range byCapture {
		desc, rc, err := h.Captures.OpenCapture(ctx, id)
		if err != nil {
			var cerr *researchcontract.Error
			if errors.As(err, &cerr) && (cerr.Code == researchcontract.OutcomeNotFound ||
				cerr.Code == researchcontract.OutcomeInvalid) {
				fail(id, fmt.Sprintf("capture %q unavailable: %s", id, cerr.Detail))
				continue
			}
			return nil, []researchcontract.SaveItemError{{
				Index: byCapture[id][0], Code: researchcontract.OutcomeInvalid,
				Detail: fmt.Sprintf("capture %q unreadable: %v", id, err)}}
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, []researchcontract.SaveItemError{{
				Index: byCapture[id][0], Code: researchcontract.OutcomeInvalid,
				Detail: fmt.Sprintf("capture %q unreadable: %v", id, err)}}
		}
		if int64(len(body)) != desc.Bytes {
			fail(id, fmt.Sprintf("capture %q byte length %d disagrees with descriptor %d",
				id, len(body), desc.Bytes))
			continue
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != desc.SHA256 {
			fail(id, fmt.Sprintf("capture %q bytes fail integrity verification (altered artifact)", id))
			continue
		}
		proved[id] = store.VerifiedSaveCapture{ContentSHA256: desc.SHA256,
			ByteLength: desc.Bytes, IsSnippet: desc.IsSnippet, Completeness: desc.Completeness}
		bodies[id] = body
	}
	if len(conflicts) > 0 {
		return nil, conflicts
	}
	for i, item := range batch.Items {
		for _, link := range item.EvidenceLinks {
			body := bodies[link.CaptureID]
			if link.SpanStart < 0 || link.SpanEnd <= link.SpanStart || link.SpanEnd > int64(len(body)) {
				conflicts = append(conflicts, researchcontract.SaveItemError{Index: i,
					Code: researchcontract.OutcomeInvalid, Detail: fmt.Sprintf(
						"span [%d,%d) exceeds capture %q (%d bytes)",
						link.SpanStart, link.SpanEnd, link.CaptureID, len(body))})
				continue
			}
			if !validSHA256(link.ExcerptSHA256) {
				conflicts = append(conflicts, researchcontract.SaveItemError{Index: i,
					Code: researchcontract.OutcomeInvalid, Detail: fmt.Sprintf(
						"excerpt hash for capture %q must be 64 lowercase hex chars", link.CaptureID)})
				continue
			}
			sum := sha256.Sum256(body[link.SpanStart:link.SpanEnd])
			if hex.EncodeToString(sum[:]) != link.ExcerptSHA256 {
				conflicts = append(conflicts, researchcontract.SaveItemError{Index: i,
					Code: researchcontract.OutcomeInvalid, Detail: fmt.Sprintf(
						"quote over capture %q span [%d,%d) is unsupported by the stored bytes",
						link.CaptureID, link.SpanStart, link.SpanEnd)})
			}
		}
	}
	if len(conflicts) > 0 {
		return nil, conflicts
	}
	return proved, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
