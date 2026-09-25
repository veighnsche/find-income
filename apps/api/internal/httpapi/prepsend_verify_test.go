package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RW-A4 verification through HTTP: reconcile never resends, double-send
// commissions one round, deliver rounds refuse resume, capability reporting
// is honest, and the safe-send preconditions (test transport configured,
// evidenced route, exact approval) are enforced or honestly reported. The
// shared fixture sender counts every SMTP call; all tests require zero.

func prepsendGetReview(t *testing.T, h *harness, cookie *http.Cookie, reviewID string) map[string]any {
	t.Helper()
	response := h.request(http.MethodGet, "/api/v1/delivery/reviews/"+reviewID, "", cookie, "", "", origin)
	if response.Code != http.StatusOK {
		t.Fatalf("get review: %d %s", response.Code, response.Body.String())
	}
	var review map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	return review
}

func prepsendSendRound(t *testing.T, h *harness, cookie *http.Cookie, csrf, reviewID string) (int, string, map[string]any) {
	t.Helper()
	response := h.request(http.MethodPost, "/api/v1/delivery/reviews/"+reviewID+"/send", "", cookie, "", csrf, origin)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("send envelope: %v %s", err, response.Body.String())
	}
	var round map[string]any
	roundID := ""
	if raw, ok := envelope["round"]; ok {
		if err := json.Unmarshal(raw, &round); err != nil {
			t.Fatal(err)
		}
		if id, ok := round["id"].(string); ok {
			roundID = id
		}
	}
	var review map[string]any
	if raw, ok := envelope["review"]; ok {
		if err := json.Unmarshal(raw, &review); err != nil {
			t.Fatal(err)
		}
	}
	return response.Code, roundID, review
}

func TestPrepsendReconcileNeverResends(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	cookie, csrf := h.login()
	before := prepsendGetReview(t, h, cookie, "http-review")
	response := h.request(http.MethodPost, "/api/v1/delivery/reviews/http-review/reconcile", "", cookie, "", csrf, origin)
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile: %d %s", response.Code, response.Body.String())
	}
	var outcome struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil || outcome.Supported || outcome.Reason == "" {
		t.Fatalf("reconcile must stay read-only: %s %v", response.Body.String(), err)
	}
	if sender.calls != 0 {
		t.Fatalf("reconcile reached sender: %d calls", sender.calls)
	}
	after := prepsendGetReview(t, h, cookie, "http-review")
	beforeJSON, _ := json.Marshal(before["items"])
	afterJSON, _ := json.Marshal(after["items"])
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("reconcile mutated items:\n%s\n%s", beforeJSON, afterJSON)
	}
}

func TestPrepsendDoubleSendSingleRound(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	cookie, csrf := h.login()
	code, firstID, firstReview := prepsendSendRound(t, h, cookie, csrf, "http-review")
	if code != http.StatusOK || firstID == "" {
		t.Fatalf("first send: code=%d round=%q", code, firstID)
	}
	code, secondID, secondReview := prepsendSendRound(t, h, cookie, csrf, "http-review")
	if code != http.StatusOK || secondID != firstID {
		t.Fatalf("second send forked: code=%d %q vs %q", code, secondID, firstID)
	}
	firstJSON, _ := json.Marshal(firstReview["items"])
	secondJSON, _ := json.Marshal(secondReview["items"])
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("double send changed items:\n%s\n%s", firstJSON, secondJSON)
	}
	if sender.calls != 0 {
		t.Fatalf("unassessed route reached sender: %d calls", sender.calls)
	}
	items, _ := secondReview["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != "prepared" {
		t.Fatalf("blocked send left no clean prepared item: %+v", items)
	}
}

func TestPrepsendDeliverResumeRefused(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	cookie, csrf := h.login()
	code, roundID, _ := prepsendSendRound(t, h, cookie, csrf, "http-review")
	if code != http.StatusOK || roundID == "" {
		t.Fatalf("send: code=%d round=%q", code, roundID)
	}
	response := h.request(http.MethodPost, "/api/v1/rounds/"+roundID+"/resume", "", cookie, "", csrf, origin)
	if response.Code != http.StatusConflict {
		t.Fatalf("deliver resume: got %d, want 409", response.Code)
	}
	if sender.calls != 0 {
		t.Fatalf("resume reached sender: %d calls", sender.calls)
	}
}

func TestPrepsendCapabilityHonest(t *testing.T) {
	h, _, _ := setupBoundSendFixture(t)
	cookie, _ := h.login()
	response := h.request(http.MethodGet, "/api/v1/delivery/capability", "", cookie, "", "", origin)
	var configured struct {
		SubmissionAvailable bool   `json:"submissionAvailable"`
		ReceiptLookup       bool   `json:"receiptLookup"`
		Reason              string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &configured); err != nil ||
		!configured.SubmissionAvailable || configured.ReceiptLookup || configured.Reason == "" {
		t.Fatalf("configured capability: %s %v", response.Body.String(), err)
	}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}})
	response = h.request(http.MethodGet, "/api/v1/delivery/capability", "", cookie, "", "", origin)
	var missing struct {
		SubmissionAvailable bool   `json:"submissionAvailable"`
		ReceiptLookup       bool   `json:"receiptLookup"`
		Reason              string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &missing); err != nil ||
		missing.SubmissionAvailable || missing.ReceiptLookup || missing.Reason == "" {
		t.Fatalf("unconfigured capability: %s %v", response.Body.String(), err)
	}
}

func TestPrepsendPrepareRequiresRouteEvidence(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	cookie, csrf := h.login()
	response := h.request(http.MethodPost, "/api/v1/delivery/reviews",
		`{"requestKey":"ps-no-route","packIds":["http-pack"]}`, cookie, "", csrf, origin)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("prepare without route evidence: got %d, want 422 (%s)", response.Code, response.Body.String())
	}
	if sender.calls != 0 {
		t.Fatalf("rejected prepare reached sender: %d calls", sender.calls)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, err := h.db.DeliveryReviewByRequest(context.Background(), owner, "ps-no-route", []string{"http-pack"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rejected prepare persisted a review: %v", err)
	}
}

func TestPrepsendTamperedApprovalCannotSend(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, err := h.db.WriteAudited(context.Background(), owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(context.Background(), `UPDATE delivery_reviews SET approved_sha256=? WHERE id='http-review'`,
			"0000000000000000000000000000000000000000000000000000000000000000")
		return store.Change{Operation: "fixture.prepsend_tamper", EntityKind: "delivery_review", EntityID: "http-review"}, err
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := h.login()
	code, roundID, _ := prepsendSendRound(t, h, cookie, csrf, "http-review")
	if code != http.StatusConflict || roundID != "" {
		t.Fatalf("tampered approval sent: code=%d round=%q", code, roundID)
	}
	if sender.calls != 0 {
		t.Fatalf("tampered approval reached sender: %d calls", sender.calls)
	}
	review := prepsendGetReview(t, h, cookie, "http-review")
	items, _ := review["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != "prepared" {
		t.Fatalf("tampered send left item dirty: %+v", items)
	}
}

func TestPrepsendSendWithoutSenderUnavailable(t *testing.T) {
	h, _, _ := setupBoundSendFixture(t)
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin},
		Delivery: &deliveryservice.Service{Store: h.db, From: "owner@example.org"}})
	cookie, csrf := h.login()
	code, roundID, _ := prepsendSendRound(t, h, cookie, csrf, "http-review")
	if code != http.StatusServiceUnavailable || roundID != "" {
		t.Fatalf("senderless send: code=%d round=%q, want 503", code, roundID)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, err := h.db.RoundByRequest(context.Background(), owner, "delivery:http-review"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("senderless send commissioned a round: %v", err)
	}
	review := prepsendGetReview(t, h, cookie, "http-review")
	items, _ := review["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != "prepared" {
		t.Fatalf("senderless send left item dirty: %+v", items)
	}
}
