// Material-bound delivery authorization (F2). Delivery review creation and
// approval resolve every pack to its exact immutable material version
// (D3/D4): the pack must be the role's current material version, that
// version must be ready, its pinned answers must still match live owner
// state, and its manifest must carry no held unknowns. Changed answers,
// material, or recipient therefore require a fresh review; stale or held
// material is rejected by the service, never merely disabled in the UI.
//
// The wrappers keep the Service.PrepareReview/ApproveReview signatures so
// the HTTP layer swaps one call each with no contract change. Request-key
// idempotency, digest-bound approval, destination/channel and revision
// binding, and per-item results all stay in the existing store path, which
// these wrappers delegate to after the material check.
package deliveryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// MaterialBinding pins one delivery review item to an exact immutable
// material version: the role, the material version number, and the pack
// identity plus content digest the review authorizes.
type MaterialBinding struct {
	OpportunityID string
	Version       int64
	PackID        string
	ContentSHA256 string
}

// materialBlankTextSHA is sha256(""): the textSha256 the material store pins
// for blank refs. Answered live values always carry a real digest; blank
// live values store NULL, so blank refs compare by state, not by digest.
var materialBlankTextSHA = func() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}()

// PrepareMaterialReview verifies that every pack is its role's current
// ready material version with unchanged answers and no held unknowns, then
// delegates to PrepareReview. The delegated store path keeps request-key
// idempotency, route/destination binding, and the review digest.
func (s *Service) PrepareMaterialReview(ctx context.Context, owner store.Actor, requestKey string, packIDs []string) (store.DeliveryReview, error) {
	if s == nil || s.Store == nil || s.From == "" {
		return store.DeliveryReview{}, ErrUnavailable
	}
	if len(packIDs) < 1 || len(packIDs) > 3 {
		return store.DeliveryReview{}, store.ErrInvalid
	}
	for _, packID := range packIDs {
		pack, err := s.Store.ApplicationPack(ctx, packID)
		if err != nil {
			return store.DeliveryReview{}, err
		}
		if _, err := s.checkPackMaterialBinding(ctx, pack, ""); err != nil {
			return store.DeliveryReview{}, err
		}
	}
	return s.PrepareReview(ctx, owner, requestKey, packIDs)
}

// ApproveMaterialReview re-verifies every review item's material binding
// against live state, then delegates to ApproveReview. A digest mismatch,
// a superseded version, changed answers, or held material all reject here,
// so an approval granted earlier can never authorize changed material.
func (s *Service) ApproveMaterialReview(ctx context.Context, owner store.Actor, reviewID, digest string) (store.DeliveryReview, error) {
	if s == nil || s.Store == nil {
		return store.DeliveryReview{}, ErrUnavailable
	}
	review, err := s.Store.DeliveryReview(ctx, reviewID)
	if err != nil {
		return store.DeliveryReview{}, err
	}
	if err := s.VerifyReviewMaterials(ctx, review); err != nil {
		return store.DeliveryReview{}, err
	}
	return s.ApproveReview(ctx, owner, reviewID, digest)
}

// VerifyReviewMaterials re-checks the material binding of every item in an
// already-loaded review: pack identity plus content digest, version
// currency, readiness, held unknowns, and pinned answers. It is the
// send-time pre-check and the eligibility read behind the review UI.
func (s *Service) VerifyReviewMaterials(ctx context.Context, review store.DeliveryReview) error {
	if s == nil || s.Store == nil {
		return ErrUnavailable
	}
	if len(review.Items) < 1 || len(review.Items) > 3 {
		return store.ErrInvalid
	}
	for _, item := range review.Items {
		pack, err := s.Store.ApplicationPack(ctx, item.PackID)
		if err != nil {
			return err
		}
		if pack.OpportunityID != item.OpportunityID {
			return store.ErrConflict
		}
		if _, err := s.checkPackMaterialBinding(ctx, pack, item.PackContentSHA256); err != nil {
			return err
		}
	}
	return nil
}

// checkPackMaterialBinding verifies that pack is its role's current ready
// material version. wantContentSHA pins the digest a review item authorized;
// empty skips that pin (review creation, where the digest is minted next).
func (s *Service) checkPackMaterialBinding(ctx context.Context, pack store.ApplicationPack, wantContentSHA string) (MaterialBinding, error) {
	binding := MaterialBinding{OpportunityID: pack.OpportunityID, PackID: pack.ID, ContentSHA256: pack.ContentSHA256}
	status, err := s.Store.CurrentOpportunityMaterials(ctx, pack.OpportunityID)
	if err != nil {
		return MaterialBinding{}, materialReadErr(err)
	}
	if err := materialStatusErr(status.Status); err != nil {
		return MaterialBinding{}, err
	}
	current := status.Current
	if current == nil || current.PackID != pack.ID {
		return MaterialBinding{}, store.ErrConflict
	}
	binding.Version = current.Version
	if wantContentSHA != "" && pack.ContentSHA256 != wantContentSHA {
		return MaterialBinding{}, store.ErrConflict
	}
	unknowns, err := packMaterialUnknowns(pack.ManifestJSON)
	if err != nil || len(unknowns) != 0 {
		return MaterialBinding{}, store.ErrInvalid
	}
	answers, err := s.Store.CurrentQuestionAnswers(ctx, pack.OpportunityID)
	if err != nil {
		return MaterialBinding{}, materialReadErr(err)
	}
	if answers.CheckID != current.CheckID || answers.QuestionSetSHA256 != current.QuestionSetSHA256 {
		return MaterialBinding{}, store.ErrConflict
	}
	if err := checkMaterialAnswerRefs(current.Answers, answers.Values); err != nil {
		return MaterialBinding{}, err
	}
	return binding, nil
}

// materialStatusErr maps a live material status to its delivery verdict:
// only a prepared version is authorizable. Held and missing material are
// invalid review material (matching the manifest-unknowns gate); anything
// else is stale (matching the stale-pack verdict).
func materialStatusErr(status string) error {
	switch status {
	case store.MaterialStatusPrepared:
		return nil
	case store.MaterialStatusHeld, store.MaterialStatusNotPrepared:
		return store.ErrInvalid
	default:
		return store.ErrConflict
	}
}

// materialReadErr maps material/answer read failures onto the delivery
// verdicts the handler already reports: selection loss fences like the
// existing selection guard, and a vanished role or check reads as stale.
func materialReadErr(err error) error {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected):
		return store.ErrFenced
	case errors.Is(err, store.ErrNotFound):
		return store.ErrConflict
	default:
		return err
	}
}

// packMaterialUnknowns reads the draft.materialUnknowns gate the preparation
// services maintain: one entry per held or missing required question. A
// missing or unreadable draft fails closed.
func packMaterialUnknowns(manifest []byte) ([]string, error) {
	var parsed struct {
		Draft *struct {
			MaterialUnknowns []string `json:"materialUnknowns"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(manifest, &parsed); err != nil || parsed.Draft == nil {
		return nil, store.ErrInvalid
	}
	return parsed.Draft.MaterialUnknowns, nil
}

// checkMaterialAnswerRefs verifies that every answer the version pinned
// still matches live owner state. Versioned refs must match version plus
// state (plus text digest for answered refs); version-zero refs pin unset
// or drafted questions, so any saved live value is a drift. Any question
// outside the pinned set holding a live value is a drift as well.
func checkMaterialAnswerRefs(refs []store.MaterialAnswerRef, values []store.QuestionAnswerValue) error {
	live := make(map[string]store.QuestionAnswerValue, len(values))
	for _, value := range values {
		live[value.QuestionID] = value
	}
	pinned := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		pinned[ref.QuestionID] = struct{}{}
		value, ok := live[ref.QuestionID]
		if ref.AnswerVersion == 0 {
			if ok {
				return store.ErrConflict
			}
			continue
		}
		if !ok || value.Version != ref.AnswerVersion {
			return store.ErrConflict
		}
		if ref.TextSHA256 == materialBlankTextSHA {
			if value.State != store.AnswerValueStateBlank {
				return store.ErrConflict
			}
			continue
		}
		if value.State != store.AnswerValueStateAnswered || value.TextSHA256 != ref.TextSHA256 {
			return store.ErrConflict
		}
	}
	for _, value := range values {
		if _, ok := pinned[value.QuestionID]; !ok {
			return store.ErrConflict
		}
	}
	return nil
}
