package jevservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

func relevanceSource() applicationpacks.Source {
	body := "Built a Go service for account administration."
	digest := sha256.Sum256([]byte(body))
	return applicationpacks.Source{ID: "asset-1", Name: "Approved work story", SHA256: hex.EncodeToString(digest[:]), Approved: true, Body: body}
}

const relevanceBody = `{"model":"jev-1.13.0","answers":{"relevance":{"type":"choice","choice":"relevant","probabilities":{"relevant":0.8,"uncertain":0.1,"unrelated":0.1},"confidence":0.7}},"usage":{"input_tokens":10,"output_tokens":2}}`

func TestAssessPackRelevanceChargedAndCaptured(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(relevanceBody)) }))
	defer server.Close()
	s, binding := serviceRound(t, 1)
	defer s.Close()
	binding.RequestKeyPrefix = "pack-relevance-asset-1-go"
	binding.MaxReportedTokens = 100
	result, err := (Service{Store: s, Client: serviceClient(t, server)}).AssessPackRelevance(context.Background(), binding,
		"Go service implementation", relevanceSource(), "Built a Go service")
	if err != nil || result.Scope != "relevant" || calls != 1 {
		t.Fatalf("relevance: %#v %v calls=%d", result, err, calls)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || attempts[0].Purpose != "pack_relevance" || attempts[0].Status != "succeeded" || !strings.Contains(string(attempts[0].SourceRefsJSON), "asset-1") {
		t.Fatalf("missing pack evidence: %#v %v", attempts, err)
	}
}

func TestAssessPackRelevanceBudgetAndUnavailableSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(relevanceBody)) }))
	defer server.Close()
	s, binding := serviceRound(t, 1)
	defer s.Close()
	binding.MaxReportedTokens = 1
	_, err := (Service{Store: s, Client: serviceClient(t, server)}).AssessPackRelevance(context.Background(), binding,
		"Go service implementation", relevanceSource(), "Built a Go service")
	if !errors.Is(err, &jev.Error{Kind: jev.ErrBudgetExceeded}) {
		t.Fatalf("over-budget relevance accepted: %v", err)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "budget_exceeded" || attempts[0].InputTokens == nil || *attempts[0].InputTokens != 10 {
		t.Fatalf("budget response lost: %#v %v", attempts, err)
	}
	// Invalid source fails in the pack helper before it can invoke an evaluator.
	invalid := relevanceSource()
	invalid.Approved = false
	_, err = (Service{Store: s, Client: serviceClient(t, server)}).AssessPackRelevance(context.Background(), binding,
		"Go service implementation", invalid, "Built a Go service")
	if !errors.Is(err, applicationpacks.ErrInvalid) {
		t.Fatalf("unapproved source accepted: %v", err)
	}
}
