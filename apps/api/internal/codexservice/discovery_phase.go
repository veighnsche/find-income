package codexservice

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type discoveryPhaseCursor struct {
	Research *struct {
		Criterion struct {
			Label string `json:"label"`
		} `json:"criterion"`
		Page int `json:"page"`
	} `json:"research"`
	SelectedDiscovery *struct {
		CandidateID string `json:"candidateId"`
	} `json:"selectedDiscovery"`
}

func (s *Service) checkDiscoveryToolPhase(ctx context.Context, roundID, method, keyword, country string, page int, companySlug, jobSlug string) error {
	r, err := s.db.Round(ctx, roundID)
	if err != nil {
		return err
	}
	if r.Outcome != "discover" || r.State != store.RoundRunning {
		return store.ErrFenced
	}
	var cursor discoveryPhaseCursor
	if json.Unmarshal(r.Cursor, &cursor) != nil || cursor.Research == nil {
		return store.ErrFenced
	}
	if method == "search_jobs" {
		if cursor.SelectedDiscovery != nil || keyword != cursor.Research.Criterion.Label || country != "" || page != cursor.Research.Page {
			return store.ErrFenced
		}
		return nil
	}
	if cursor.SelectedDiscovery == nil || cursor.SelectedDiscovery.CandidateID == "" {
		return store.ErrFenced
	}
	selected, err := s.db.RoundDiscoveryCandidate(ctx, roundID, cursor.SelectedDiscovery.CandidateID)
	if err != nil {
		return err
	}
	u, err := url.Parse(selected.URL)
	if err != nil || u.Scheme != "https" || u.Host != "himalayas.app" {
		return store.ErrFenced
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "companies" || parts[2] != "jobs" || companySlug != parts[1] {
		return store.ErrFenced
	}
	if (method == "get_company_details" && jobSlug == "") || (method == "get_job_details" && jobSlug == parts[3]) {
		return nil
	}
	return store.ErrFenced
}

func (s *Service) checkDiscoveryStagePhase(ctx context.Context, roundID string) error {
	r, err := s.db.Round(ctx, roundID)
	if err != nil {
		return err
	}
	var cursor discoveryPhaseCursor
	if r.Outcome != "discover" || r.State != store.RoundRunning || json.Unmarshal(r.Cursor, &cursor) != nil ||
		cursor.Research == nil || cursor.SelectedDiscovery != nil {
		return store.ErrFenced
	}
	return nil
}
