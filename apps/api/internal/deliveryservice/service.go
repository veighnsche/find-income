// Package deliveryservice binds owner approval and a finite round to exact
// stored application messages. It never runs during startup or preparation.
package deliveryservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/delivery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var ErrUnavailable = errors.New("delivery sender unavailable")
var ErrUnsupportedRoute = errors.New("no evidenced email application route")

type Sender interface {
	Send(context.Context, delivery.Material, string) delivery.Outcome
}

type Service struct {
	Store   *store.Store
	Sender  Sender
	Advisor NextActionAdvisor
	From    string
	mu      sync.Mutex
	active  map[string]context.CancelFunc
}

// DeliveryAdviceFacts contains only recorded state counts and the review ID.
// It never includes a recipient, subject, body, MIME bytes, or raw offer text.
type DeliveryAdviceFacts struct {
	ReviewID  string
	Recorded  int
	Failed    int
	Uncertain int
}

type NextActionAdvisor interface {
	RecommendDelivery(context.Context, store.Round, DeliveryAdviceFacts) json.RawMessage
}

type SendResult struct {
	Review store.DeliveryReview `json:"review"`
	Round  store.Round          `json:"round"`
}

// PrepareReview takes only pack identities. Everything the owner reviews is
// derived from cited saved records and canonical MIME created here.
func (s *Service) PrepareReview(ctx context.Context, owner store.Actor, requestKey string, packIDs []string) (store.DeliveryReview, error) {
	if s == nil || s.Store == nil || s.From == "" {
		return store.DeliveryReview{}, ErrUnavailable
	}
	if len(packIDs) < 1 || len(packIDs) > 3 {
		return store.DeliveryReview{}, store.ErrInvalid
	}
	if previous, err := s.Store.DeliveryReviewByRequest(ctx, owner, requestKey, packIDs); err == nil {
		return previous, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.DeliveryReview{}, err
	}
	drafts := make([]store.DeliveryDraft, 0, len(packIDs))
	for _, packID := range packIDs {
		pack, err := s.Store.ApplicationPack(ctx, packID)
		if err != nil {
			return store.DeliveryReview{}, err
		}
		opportunity, err := s.Store.Opportunity(ctx, pack.OpportunityID)
		if err != nil {
			return store.DeliveryReview{}, err
		}
		company, err := s.Store.Company(ctx, opportunity.CompanyID)
		if err != nil {
			return store.DeliveryReview{}, err
		}
		selection, err := s.Store.OwnerOpportunityDecision(ctx, opportunity.ID)
		if err != nil || selection.Decision != "selected" || selection.OpportunityRevision != opportunity.Revision {
			return store.DeliveryReview{}, store.ErrFenced
		}
		routes, err := s.Store.ListOpportunityRoutes(ctx, opportunity.ID)
		if err != nil {
			return store.DeliveryReview{}, err
		}
		var selected *store.OpportunityRoute
		for i := range routes {
			if store.DeliveryRouteCandidate(routes[i], opportunity.OriginalText) {
				assessment, assessmentErr := s.Store.CurrentDeliveryRouteAssessment(ctx, routes[i], opportunity)
				if assessmentErr != nil || assessment.Choice == "unresolved" {
					return store.DeliveryReview{}, ErrUnsupportedRoute
				}
				if assessment.Choice == "other_contact" {
					continue
				}
				if assessment.Choice != "application_mailbox" {
					return store.DeliveryReview{}, ErrUnsupportedRoute
				}
				if selected != nil {
					return store.DeliveryReview{}, ErrUnsupportedRoute // Ambiguous destinations require a corrected route.
				}
				selected = &routes[i]
			}
		}
		if selected == nil {
			return store.DeliveryReview{}, ErrUnsupportedRoute
		}
		var manifest struct {
			Role struct {
				Destination string `json:"destination"`
			} `json:"role"`
			Draft struct {
				Cover []struct {
					Text string `json:"text"`
				} `json:"cover"`
				MaterialUnknowns []string `json:"materialUnknowns"`
			} `json:"draft"`
		}
		if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil || len(manifest.Draft.Cover) == 0 ||
			len(manifest.Draft.MaterialUnknowns) != 0 ||
			manifest.Role.Destination != "" && manifest.Role.Destination != selected.DestinationText {
			return store.DeliveryReview{}, store.ErrInvalid
		}
		lines := make([]string, 0, len(manifest.Draft.Cover))
		for _, line := range manifest.Draft.Cover {
			if strings.TrimSpace(line.Text) == "" {
				return store.DeliveryReview{}, store.ErrInvalid
			}
			lines = append(lines, line.Text)
		}
		body := strings.Join(lines, "\n\n")
		subject := "Application: " + opportunity.Title
		if len(subject) > 512 {
			return store.DeliveryReview{}, store.ErrInvalid
		}
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return store.DeliveryReview{}, err
		}
		messageID := fmt.Sprintf("<%s@find-income.local>", hex.EncodeToString(random))
		pdfDigest := sha256.Sum256(pack.PDF)
		attachmentSHA := hex.EncodeToString(pdfDigest[:])
		material, err := delivery.Prepare(delivery.Input{From: s.From, To: selected.DestinationText, MessageID: messageID,
			Date: time.Now().UTC(), Subject: subject, Body: body, Attachments: []delivery.Attachment{
				{Filename: "application.pdf", ContentType: "application/pdf", Data: pack.PDF, SHA256: attachmentSHA},
			}})
		if err != nil {
			return store.DeliveryReview{}, err
		}
		drafts = append(drafts, store.DeliveryDraft{PackID: pack.ID, OpportunityID: opportunity.ID,
			OpportunityRevision: pack.OpportunityRevision, SourceSHA256: store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText),
			ProfileRevision: pack.ProfileRevision, PackContentSHA256: pack.ContentSHA256,
			RouteID: selected.ID, RouteRevision: selected.Revision, RouteSHA256: store.DeliveryRouteHash(*selected),
			Title: opportunity.Title, CompanyName: company.Name, RouteExcerpt: selected.SourceExcerpt,
			Recipient: selected.DestinationText, Sender: s.From, Subject: subject, Body: body,
			AttachmentSHA256: attachmentSHA, MIMESHA256: material.Digest(), MIMEBytes: material.Bytes(), MessageID: material.MessageID()})
	}
	return s.Store.CreateDeliveryReview(ctx, owner, requestKey, drafts)
}

func (s *Service) ApproveReview(ctx context.Context, owner store.Actor, reviewID, digest string) (store.DeliveryReview, error) {
	if s == nil || s.Store == nil {
		return store.DeliveryReview{}, ErrUnavailable
	}
	return s.Store.ApproveDeliveryReview(ctx, owner, reviewID, digest)
}

// SendReview is owner-triggered and consumes one finite round. An identical
// review cannot start a second sender run, regardless of HTTP request key.
func (s *Service) SendReview(ctx context.Context, owner store.Actor, reviewID string) (SendResult, error) {
	if s == nil || s.Store == nil {
		return SendResult{}, ErrUnavailable
	}
	review, err := s.Store.DeliveryReview(ctx, reviewID)
	if err != nil {
		return SendResult{}, err
	}
	if review.ApprovedSHA256 == "" || review.ApprovedSHA256 != review.MaterialSHA256 || len(review.Items) < 1 || len(review.Items) > 3 {
		return SendResult{}, store.ErrFenced
	}
	requestKey := "delivery:" + reviewID
	if prior, err := s.Store.RoundByRequest(ctx, owner, requestKey); err == nil {
		latest, readErr := s.Store.DeliveryReview(ctx, reviewID)
		return SendResult{Review: latest, Round: prior}, readErr
	} else if !errors.Is(err, store.ErrNotFound) {
		return SendResult{}, err
	}
	if s.Sender == nil || s.From == "" {
		return SendResult{}, ErrUnavailable
	}
	profileVersion := review.Items[0].ProfileRevision
	resources := make([]string, len(review.Items))
	for i, item := range review.Items {
		if item.State != "prepared" || item.ProfileRevision != profileVersion {
			return SendResult{}, store.ErrFenced
		}
		resources[i] = "delivery:" + item.ID
	}
	resources = append(resources, "campaign:active")
	input := store.StartRoundInput{RequestKey: requestKey, Intent: "Deliver only the exact approved application review " + reviewID,
		Outcome: "deliver", ProfileVersion: profileVersion, Scope: store.RoundScope{InputRefs: []string{"delivery_review:" + reviewID},
			Resources: resources, Operations: []string{store.RoundDeliverApplication, store.RoundJevRequest}},
		Limits:   store.RoundAllowance{Requests: int64(len(review.Items)) + 1, Items: int64(len(review.Items)), Tools: int64(len(review.Items))},
		Deadline: time.Now().Add(10 * time.Minute).UTC()}
	round, created, err := s.Store.StartRound(ctx, owner, input)
	if errors.Is(err, store.ErrRoundIdempotencyConflict) {
		prior, readErr := s.Store.RoundByRequest(ctx, owner, requestKey)
		if readErr == nil {
			latest, reviewErr := s.Store.DeliveryReview(ctx, reviewID)
			return SendResult{Review: latest, Round: prior}, reviewErr
		}
	}
	if err != nil {
		return SendResult{}, err
	}
	if !created {
		return SendResult{Review: review, Round: round}, nil
	}
	round, err = s.Store.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		return SendResult{}, err
	}
	// Continue after browser disconnection. A committed send intent must receive
	// one durable outcome or stay conservatively uncertain on restart.
	work, cancel := context.WithDeadline(context.WithoutCancel(ctx), round.Deadline)
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]context.CancelFunc{}
	}
	s.active[round.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, round.ID)
		s.mu.Unlock()
		cancel()
	}()
	workFailure := ""
	for _, item := range review.Items {
		attempt, err := s.Store.ReserveDeliveryAttempt(work, owner, reviewID, item.ID, round.ID)
		if err != nil {
			workFailure = "reservation_failed"
			break
		}
		claimed, err := s.Store.ClaimDeliveryItem(work, owner, reviewID, item.ID, round.ID, attempt.ID)
		if err != nil {
			workFailure = "preflight_or_claim_failed"
			break
		}
		if _, err := s.Store.MarkRoundDispatched(work, round.ID, attempt.ID); err != nil {
			_ = s.Store.SaveDeliveryOutcome(context.Background(), item.ID, attempt.ID, "uncertain", "dispatch", 0, "intent committed; dispatch fence failed")
			_ = s.Store.PauseUncertainDelivery(context.Background(), round.ID, attempt.ID, round.Generation)
			workFailure = "delivery_intent_unknown"
			break
		}
		// Stop fences the round in SQLite and cancels this context. A stop
		// between this check and Send is still observed by SMTP before dial.
		currentRound, stateErr := s.Store.Round(work, round.ID)
		if stateErr != nil || currentRound.State != store.RoundRunning || work.Err() != nil {
			_ = s.Store.SaveDeliveryOutcome(context.Background(), item.ID, attempt.ID, "failed", "stopped_before_network", 0, "round stopped before SMTP started")
			workFailure = "stopped_before_network"
			break
		}
		material, err := delivery.RestoreMaterial(claimed.Sender, claimed.Recipient, claimed.MessageID, claimed.MIMESHA256, claimed.MIMEBytes)
		if err != nil {
			_ = s.Store.SaveDeliveryOutcome(context.Background(), item.ID, attempt.ID, "uncertain", "material", 0, "stored material validation failed after intent")
			_ = s.Store.PauseUncertainDelivery(context.Background(), round.ID, attempt.ID, round.Generation)
			workFailure = "stored_material_invalid"
			break
		}
		outcome := s.Sender.Send(work, material, claimed.MIMESHA256)
		state := "failed"
		if outcome.State == delivery.AcceptedBySMTP {
			state = "accepted_by_smtp"
		} else if outcome.State == delivery.Uncertain {
			state = "uncertain"
		}
		detail := string(outcome.State)
		if err := s.Store.SaveDeliveryOutcome(context.Background(), item.ID, attempt.ID, state, outcome.Stage, outcome.SMTPCode, detail); err != nil {
			_ = s.Store.PauseUncertainDelivery(context.Background(), round.ID, attempt.ID, round.Generation)
			workFailure = "outcome_persistence_failed"
			break
		}
		if state == "uncertain" {
			_ = s.Store.PauseUncertainDelivery(context.Background(), round.ID, attempt.ID, round.Generation)
			workFailure = "delivery_outcome_unknown"
			break
		}
		result, _ := json.Marshal(map[string]any{"submissionState": state, "smtpCode": outcome.SMTPCode, "receiptVerified": false})
		if _, err := s.Store.FinishRoundAttempt(context.Background(), owner, round.ID, attempt.ID, state == "accepted_by_smtp", result, "smtp_submission_failed"); err != nil {
			_ = s.Store.PauseUncertainDelivery(context.Background(), round.ID, attempt.ID, round.Generation)
			workFailure = "round_result_persistence_failed"
			break
		}
	}
	current, readErr := s.Store.Round(context.Background(), round.ID)
	if readErr == nil && current.State == store.RoundRunning {
		adviceFacts := DeliveryAdviceFacts{ReviewID: reviewID}
		if latestReview, reviewErr := s.Store.DeliveryReview(context.Background(), reviewID); reviewErr == nil {
			for _, item := range latestReview.Items {
				if item.RoundID != round.ID {
					continue
				}
				switch item.State {
				case "accepted_by_smtp":
					adviceFacts.Recorded++
				case "failed":
					adviceFacts.Recorded++
					adviceFacts.Failed++
				case "uncertain":
					adviceFacts.Recorded++
					adviceFacts.Uncertain++
				}
			}
		}
		report := struct {
			EmployerReceiptVerified bool            `json:"employerReceiptVerified"`
			Recommendation          json.RawMessage `json:"recommendation,omitempty"`
		}{}
		if adviceFacts.Recorded > 0 && s.Advisor != nil && work.Err() == nil {
			report.Recommendation = s.Advisor.RecommendDelivery(work, current, adviceFacts)
		}
		encoded, _ := json.Marshal(report)
		terminal, reason := store.RoundCompleted, "delivery_attempts_recorded"
		if workFailure != "" {
			terminal, reason = store.RoundFailed, workFailure
		}
		_, _ = s.Store.FinishRound(context.Background(), owner, round.ID, terminal, reason, "submission_unverified", encoded)
	}
	latest, err := s.Store.DeliveryReview(context.Background(), reviewID)
	if err != nil {
		return SendResult{}, err
	}
	final, err := s.Store.Round(context.Background(), round.ID)
	return SendResult{Review: latest, Round: final}, err
}

// StopRound fences database authority first, then interrupts the active SMTP
// context. A server may still have accepted DATA; the eventual result remains
// recorded and is never retried from this review.
func (s *Service) StopRound(ctx context.Context, owner store.Actor, roundID string) (store.Round, error) {
	if s == nil || s.Store == nil {
		return store.Round{}, ErrUnavailable
	}
	stopped, _, err := s.Store.StopRound(ctx, owner, roundID)
	if err != nil {
		return store.Round{}, err
	}
	s.mu.Lock()
	cancel := s.active[roundID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopped.State == store.RoundPaused {
		return stopped, nil
	}
	return s.Store.PauseStoppedRound(ctx, owner, roundID)
}
