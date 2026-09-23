package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/offercomparison"
)

type OfferIntake struct {
	ID        string                   `json:"id"`
	Sources   []offercomparison.Source `json:"sources"`
	CreatedAt string                   `json:"createdAt"`
}

const OfferTradeoffMaxReportedTokens int64 = 20000

type SavedOfferComparison struct {
	ID             string                              `json:"id"`
	IntakeID       string                              `json:"intakeId"`
	RoundID        string                              `json:"roundId"`
	Comparison     offercomparison.Comparison          `json:"comparison"`
	TradeoffStatus string                              `json:"tradeoffStatus"`
	Tradeoff       *offercomparison.PreparedComparison `json:"tradeoff,omitempty"`
	Current        bool                                `json:"current"`
	CreatedAt      string                              `json:"createdAt"`
}

// The owner supplies complete text only. Source IDs, revisions and hashes are
// minted here and can never be supplied by the model or browser.
func (s *Store) CreateOfferIntake(ctx context.Context, actor Actor, key string, offers []string, priorities string) (OfferIntake, bool, error) {
	if !ownerRoundActor(actor) || key == "" || len(key) > 180 || strings.TrimSpace(key) != key || len(offers) < 1 || len(offers) > 5 || len(priorities) > 5000 || !utf8.ValidString(priorities) || priorities != "" && strings.TrimSpace(priorities) == "" {
		return OfferIntake{}, false, ErrInvalid
	}
	request, _ := json.Marshal(struct {
		Offers     []string `json:"offers"`
		Priorities string   `json:"prioritiesText"`
	}{offers, priorities})
	sum := sha256.Sum256(request)
	requestSHA := hex.EncodeToString(sum[:])
	var sources []offercomparison.Source
	total := len(priorities)
	for i, body := range offers {
		if strings.TrimSpace(body) == "" || len(body) > 20000 || !utf8.ValidString(body) {
			return OfferIntake{}, false, ErrInvalid
		}
		total += len(body)
		id := "offer-" + string(rune('1'+i))
		hash := sha256.Sum256([]byte(body))
		sources = append(sources, offercomparison.Source{ID: id, OfferID: id, Kind: "owner_paste", Revision: "1", SHA256: hex.EncodeToString(hash[:]), Body: body})
	}
	if total > 28000 {
		return OfferIntake{}, false, ErrInvalid
	}
	if priorities != "" {
		hash := sha256.Sum256([]byte(priorities))
		sources = append(sources, offercomparison.Source{ID: "owner-priorities", Kind: "owner_priorities", Revision: "1", SHA256: hex.EncodeToString(hash[:]), Body: priorities})
	}
	bounded, _ := json.Marshal(sources)
	if len(bounded) > 30000 {
		return OfferIntake{}, false, ErrInvalid
	}
	read := func() (OfferIntake, string, error) {
		var value OfferIntake
		var raw, digest string
		err := s.db.QueryRowContext(ctx, `SELECT id,request_sha256,sources_json,created_at FROM offer_comparison_intakes WHERE owner_id=? AND request_key=?`, actor.ID, key).Scan(&value.ID, &digest, &raw, &value.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return OfferIntake{}, "", ErrNotFound
		}
		if err != nil {
			return OfferIntake{}, "", err
		}
		if err := json.Unmarshal([]byte(raw), &value.Sources); err != nil {
			return OfferIntake{}, "", err
		}
		return value, digest, nil
	}
	if prior, digest, err := read(); err == nil {
		if digest != requestSHA {
			return OfferIntake{}, false, ErrRoundIdempotencyConflict
		}
		return prior, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return OfferIntake{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return OfferIntake{}, false, err
	}
	now := utcNow()
	raw, _ := json.Marshal(sources)
	_, err = s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO offer_comparison_intakes(id,owner_id,request_key,request_sha256,sources_json,created_at) VALUES (?,?,?,?,?,?)`, id, actor.ID, key, requestSHA, string(raw), now)
		return Change{Operation: "offer_comparison.intake", EntityKind: "offer_intake", EntityID: id}, err
	})
	if err != nil {
		if prior, digest, readErr := read(); readErr == nil {
			if digest != requestSHA {
				return OfferIntake{}, false, ErrRoundIdempotencyConflict
			}
			return prior, false, nil
		}
		return OfferIntake{}, false, err
	}
	return OfferIntake{ID: id, Sources: sources, CreatedAt: now}, true, nil
}

func (s *Store) OfferIntake(ctx context.Context, id string) (OfferIntake, error) {
	var value OfferIntake
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT id,sources_json,created_at FROM offer_comparison_intakes WHERE id=?`, id).Scan(&value.ID, &raw, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OfferIntake{}, ErrNotFound
	}
	if err != nil {
		return OfferIntake{}, err
	}
	if err := json.Unmarshal([]byte(raw), &value.Sources); err != nil {
		return OfferIntake{}, err
	}
	return value, nil
}

func createOfferComparisonTx(ctx context.Context, tx *sql.Tx, round Round, input OfferComparisonMutationInput) (string, int64, error) {
	var owner, raw string
	err := tx.QueryRowContext(ctx, `SELECT owner_id,sources_json FROM offer_comparison_intakes WHERE id=?`, input.IntakeID).Scan(&owner, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	if owner != round.Actor.ID || round.Outcome != "compare_offers" {
		return "", 0, ErrFenced
	}
	var sources []offercomparison.Source
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return "", 0, err
	}
	if !reflect.DeepEqual(sources, input.Comparison.Input.Sources) {
		return "", 0, ErrFenced
	}
	if len(input.Comparison.Input.Offers) != len(sources) && len(input.Comparison.Input.Offers) != len(sources)-1 {
		return "", 0, ErrInvalid
	}
	for _, source := range sources {
		if source.OfferID == "" {
			continue
		}
		found := false
		for _, offer := range input.Comparison.Input.Offers {
			if offer.ID == source.OfferID {
				found = true
				break
			}
		}
		if !found {
			return "", 0, ErrInvalid
		}
	}
	validated, err := offercomparison.Prepare(input.Comparison.Input)
	if err != nil || !reflect.DeepEqual(validated, input.Comparison) {
		return "", 0, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return "", 0, err
	}
	data, _ := json.Marshal(validated)
	_, err = tx.ExecContext(ctx, `INSERT INTO offer_comparisons(id,intake_id,round_id,input_sha256,comparison_json,created_at) VALUES (?,?,?,?,?,?)`, id, input.IntakeID, round.ID, validated.InputSHA256, string(data), utcNow())
	return id, 1, err
}

func (s *Store) OfferComparison(ctx context.Context, id string) (SavedOfferComparison, error) {
	var value SavedOfferComparison
	var raw, digest string
	err := s.db.QueryRowContext(ctx, `SELECT id,intake_id,round_id,input_sha256,comparison_json,created_at FROM offer_comparisons WHERE id=?`, id).Scan(&value.ID, &value.IntakeID, &value.RoundID, &digest, &raw, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedOfferComparison{}, ErrNotFound
	}
	if err != nil {
		return SavedOfferComparison{}, err
	}
	if err := json.Unmarshal([]byte(raw), &value.Comparison); err != nil {
		return SavedOfferComparison{}, err
	}
	intake, err := s.OfferIntake(ctx, value.IntakeID)
	if err != nil {
		return SavedOfferComparison{}, err
	}
	check, err := offercomparison.Prepare(value.Comparison.Input)
	value.Current = err == nil && check.InputSHA256 == digest && reflect.DeepEqual(check, value.Comparison) && reflect.DeepEqual(intake.Sources, value.Comparison.Input.Sources)
	value.TradeoffStatus = "not_requested"
	var resultRaw string
	err = s.db.QueryRowContext(ctx, `SELECT result_json FROM offer_tradeoff_assessments WHERE comparison_id=?`, id).Scan(&resultRaw)
	if err == nil {
		var selection jev.OfferTradeoffResult
		if json.Unmarshal([]byte(resultRaw), &selection) != nil {
			return SavedOfferComparison{}, ErrConflict
		}
		prepared, bindErr := value.Comparison.BindTradeoffSelection(selection, OfferTradeoffMaxReportedTokens)
		if bindErr != nil {
			value.Current = false
			value.TradeoffStatus = "invalid"
		} else {
			value.Tradeoff = &prepared
			value.TradeoffStatus = string(selection.Disposition)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SavedOfferComparison{}, err
	} else if len(value.Comparison.Input.Alternatives) > 0 {
		value.TradeoffStatus = "pending"
		var attemptStatus string
		attemptErr := s.db.QueryRowContext(ctx, `SELECT status FROM jev_attempts WHERE round_id=? AND purpose='offer_tradeoff' ORDER BY created_at DESC LIMIT 1`, value.RoundID).Scan(&attemptStatus)
		if attemptErr == nil {
			switch attemptStatus {
			case "failed", "budget_exceeded":
				value.TradeoffStatus = "failed"
			case "invalid_response":
				value.TradeoffStatus = "invalid"
			case "uncertain", "dispatched":
				value.TradeoffStatus = "uncertain"
			case "succeeded":
				value.TradeoffStatus = "pending"
			}
		} else if !errors.Is(attemptErr, sql.ErrNoRows) {
			return SavedOfferComparison{}, attemptErr
		}
		if value.TradeoffStatus == "pending" {
			round, roundErr := s.Round(ctx, value.RoundID)
			if roundErr != nil {
				return SavedOfferComparison{}, roundErr
			}
			if round.State == RoundPaused {
				value.TradeoffStatus = "uncertain"
			} else if round.State == RoundCompleted || round.State == RoundFailed {
				value.TradeoffStatus = "unavailable"
			}
		}
	}
	return value, nil
}

func (s *Store) OfferComparisonByRound(ctx context.Context, roundID string) (SavedOfferComparison, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM offer_comparisons WHERE round_id=?`, roundID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedOfferComparison{}, ErrNotFound
	}
	if err != nil {
		return SavedOfferComparison{}, err
	}
	return s.OfferComparison(ctx, id)
}

func (s *Store) OfferComparisonForOwner(ctx context.Context, actor Actor, id string) (SavedOfferComparison, error) {
	if !ownerRoundActor(actor) {
		return SavedOfferComparison{}, ErrFenced
	}
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT i.owner_id FROM offer_comparison_intakes i JOIN offer_comparisons c ON c.intake_id=i.id WHERE c.id=?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || owner != actor.ID {
		return SavedOfferComparison{}, ErrNotFound
	}
	if err != nil {
		return SavedOfferComparison{}, err
	}
	return s.OfferComparison(ctx, id)
}

func (s *Store) OfferComparisonByRoundForOwner(ctx context.Context, actor Actor, roundID string) (SavedOfferComparison, error) {
	if !ownerRoundActor(actor) {
		return SavedOfferComparison{}, ErrFenced
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT c.id FROM offer_comparisons c JOIN offer_comparison_intakes i ON i.id=c.intake_id WHERE c.round_id=? AND i.owner_id=?`, roundID, actor.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedOfferComparison{}, ErrNotFound
	}
	if err != nil {
		return SavedOfferComparison{}, err
	}
	return s.OfferComparison(ctx, id)
}

func (s *Store) SaveOfferTradeoff(ctx context.Context, actor Actor, comparisonID, attemptID string, result jev.OfferTradeoffResult) error {
	if actor.Kind != "agent" || actor.ID != "codex-runner" || comparisonID == "" || attemptID == "" {
		return ErrInvalid
	}
	comparison, err := s.OfferComparison(ctx, comparisonID)
	if err != nil || !comparison.Current {
		return ErrFenced
	}
	if _, err := comparison.Comparison.BindTradeoffSelection(result, OfferTradeoffMaxReportedTokens); err != nil {
		return ErrInvalid
	}
	attempt, err := s.JevAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	if attempt.RoundID != comparison.RoundID || attempt.Purpose != "offer_tradeoff" || attempt.Status != "succeeded" || attempt.ResponseTruncated || attempt.ResponseReadError {
		return ErrFenced
	}
	input, err := comparison.Comparison.TradeoffInput(OfferTradeoffMaxReportedTokens)
	if err != nil {
		return ErrInvalid
	}
	recovered, err := jev.RecoverCapturedOfferTradeoff(input, attempt.LogicalRequestJSON, attempt.RawResponseBytes, attempt.RequestedModel)
	if err != nil || !reflect.DeepEqual(recovered, result) {
		return ErrInvalid
	}
	var old string
	if err := s.db.QueryRowContext(ctx, `SELECT result_json FROM offer_tradeoff_assessments WHERE comparison_id=?`, comparisonID).Scan(&old); err == nil {
		encoded, _ := json.Marshal(result)
		if old == string(encoded) {
			return nil
		}
		return ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		var roundID, purpose, status string
		err := tx.QueryRowContext(ctx, `SELECT round_id,purpose,status FROM jev_attempts WHERE id=?`, attemptID).Scan(&roundID, &purpose, &status)
		if err != nil {
			return Change{}, err
		}
		if roundID != comparison.RoundID || purpose != "offer_tradeoff" || status != "succeeded" {
			return Change{}, ErrFenced
		}
		id, err := randomID()
		if err != nil {
			return Change{}, err
		}
		raw, _ := json.Marshal(result)
		_, err = tx.ExecContext(ctx, `INSERT INTO offer_tradeoff_assessments(id,comparison_id,round_id,jev_attempt_id,result_json,created_at) VALUES (?,?,?,?,?,?)`, id, comparisonID, roundID, attemptID, string(raw), utcNow())
		return Change{Operation: "offer_comparison.tradeoff", EntityKind: "offer_tradeoff", EntityID: id}, err
	})
	return err
}

func (s *Store) OfferTradeoffJevAttemptByRoundAttempt(ctx context.Context, roundAttemptID string) (JevAttempt, error) {
	a, err := scanJevAttempt(s.db.QueryRowContext(ctx, `SELECT `+jevAttemptColumns+` FROM jev_attempts WHERE round_attempt_id=? AND purpose='offer_tradeoff' AND step_index=0`, roundAttemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return JevAttempt{}, ErrNotFound
	}
	return a, err
}

// A captured local outcome resolves a paused Jev attempt without another
// provider call or reconciliation charge. A successful projection must already
// be saved from the exact captured response.
func (s *Store) ResolveCapturedOfferRoundAttempt(ctx context.Context, roundID, attemptID, jevID string, success bool) error {
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if round.State != RoundPaused {
		return ErrFenced
	}
	var operation, state, status, purpose string
	err = tx.QueryRowContext(ctx, `SELECT a.operation,a.state,j.status,j.purpose FROM round_attempts a JOIN jev_attempts j ON j.round_attempt_id=a.id WHERE a.id=? AND a.round_id=? AND j.id=?`, attemptID, roundID, jevID).Scan(&operation, &state, &status, &purpose)
	if err != nil {
		return err
	}
	if operation != RoundJevRequest || state != string(AttemptUncertain) || purpose != "offer_tradeoff" {
		return ErrFenced
	}
	if success {
		if status != "succeeded" {
			return ErrFenced
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM offer_tradeoff_assessments WHERE round_id=? AND jev_attempt_id=?`, roundID, jevID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrFenced
		}
	} else if status != "failed" && status != "invalid_response" && status != "budget_exceeded" {
		return ErrUncertain
	}
	next := AttemptObservedFailure
	if success {
		next = AttemptObservedSuccess
	}
	_, err = tx.ExecContext(ctx, `UPDATE round_attempts SET state=?,finished_at=?,updated_at=? WHERE id=? AND round_id=? AND state='uncertain'`, next, utcNow(), utcNow(), attemptID, roundID)
	if err != nil {
		return err
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "offer-capture-recovery"}, "round.reconcile.local", roundID); err != nil {
		return err
	}
	return tx.Commit()
}
