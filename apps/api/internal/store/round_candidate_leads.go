package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
)

// RoundCandidateLead is one staged discovery candidate with its verification
// outcome. Leads are explicitly unverified until an employer board check
// passes and the role is saved as an opportunity.
type RoundCandidateLead struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	CompanyURL    string `json:"companyUrl"`
	EvidenceQuote string `json:"evidenceQuote"`
	ObservedAt    string `json:"observedAt"`
	Status        string `json:"status"`
	StatusReason  string `json:"statusReason"`
}

// RoundCandidateSearch is one recorded discovery read in a round.
type RoundCandidateSearch struct {
	Source     string `json:"source"`
	Method     string `json:"method"`
	ObservedAt string `json:"observedAt"`
	Candidates int64  `json:"candidates"`
	Succeeded  bool   `json:"succeeded"`
}

// RoundCandidateLeads returns the staged leads of a round with per-lead
// verification status plus the discovery reads that produced them.
func (s *Store) RoundCandidateLeads(ctx context.Context, roundID string) ([]RoundCandidateLead, []RoundCandidateSearch, error) {
	if _, err := s.Round(ctx, roundID); err != nil {
		return nil, nil, err
	}
	candidates, err := s.RoundDiscoveryCandidates(ctx, roundID)
	if err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{}
	rows, err := s.db.QueryContext(ctx, `SELECT dc.attempt_id,COUNT(*) FROM discovery_candidates dc
	  JOIN round_attempts a ON a.id=dc.attempt_id WHERE a.round_id=? GROUP BY dc.attempt_id`, roundID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var attempt string
		var count int64
		if err := rows.Scan(&attempt, &count); err != nil {
			rows.Close()
			return nil, nil, err
		}
		counts[attempt] = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	leads := make([]RoundCandidateLead, 0, len(candidates))
	for _, candidate := range candidates {
		status, reason, err := s.candidateLeadStatus(ctx, roundID, candidate)
		if err != nil {
			return nil, nil, err
		}
		leads = append(leads, RoundCandidateLead{
			ID: candidate.ID, Kind: candidate.Kind, Title: candidate.Title, URL: candidate.URL,
			CompanyURL: candidate.CompanyURL, EvidenceQuote: candidate.EvidenceQuote,
			ObservedAt: candidate.ObservedAt, Status: status, StatusReason: reason,
		})
	}
	searches, err := s.roundCandidateSearches(ctx, roundID, counts)
	if err != nil {
		return nil, nil, err
	}
	return leads, searches, nil
}

// OpportunityBySourceURL returns the opportunity saved for a candidate
// source URL in any round, so re-staged leads are recognised as saved.
func (s *Store) OpportunityBySourceURL(ctx context.Context, sourceURL string) (string, error) {
	if sourceURL == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM opportunities WHERE source_url=? LIMIT 1`, sourceURL).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// RoundOpportunityBySourceURL returns the opportunity a round saved for a
// candidate source URL, so direct verified saves count without a board.
func (s *Store) RoundOpportunityBySourceURL(ctx context.Context, roundID, sourceURL string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT o.id FROM opportunities o
	  JOIN round_record_changes rc ON rc.round_id=?
	  JOIN audit_changes ac ON ac.id=rc.audit_id AND ac.entity_kind='opportunity' AND ac.entity_id=o.id
	  WHERE o.source_url=? LIMIT 1`, roundID, sourceURL).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) candidateLeadStatus(ctx context.Context, roundID string, candidate RoundDiscoveryCandidate) (string, string, error) {
	if _, err := s.RoundOpportunityBySourceURL(ctx, roundID, candidate.URL); err == nil {
		return "saved", "Saved as an opportunity.", nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	if _, err := s.OpportunityBySourceURL(ctx, candidate.URL); err == nil {
		return "saved", "Saved as an opportunity in an earlier search.", nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	if _, err := s.RoundDiscoveryBoardForCandidate(ctx, roundID, candidate.ID); err == nil {
		return "verified", "Employer verified; not saved as an opportunity yet.", nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	var checked int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_official_reads WHERE round_id=? AND candidate_id=?`, roundID, candidate.ID).Scan(&checked); err != nil {
		return "", "", err
	}
	if checked > 0 {
		return "checked", "Employer site checked but could not be verified automatically.", nil
	}
	return "staged", "Found in search; not yet checked.", nil
}

func (s *Store) roundCandidateSearches(ctx context.Context, roundID string, counts map[string]int64) ([]RoundCandidateSearch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT attempt_id,method,endpoint,status_code,error_code,observed_at
	  FROM discovery_http WHERE round_id=? ORDER BY observed_at,attempt_id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	searches := []RoundCandidateSearch{}
	for rows.Next() {
		var attempt, method, endpoint, observedAt string
		var status int64
		var errorCode string
		if err := rows.Scan(&attempt, &method, &endpoint, &status, &errorCode, &observedAt); err != nil {
			return nil, err
		}
		searches = append(searches, RoundCandidateSearch{
			Source: discoveryHost(endpoint), Method: method, ObservedAt: observedAt,
			Candidates: counts[attempt], Succeeded: status == 200 && errorCode == "",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return searches, nil
}

func discoveryHost(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return endpoint
}
