package store

import (
	"context"
	"encoding/json"
)

func selectedDiscoveryCandidate(round Round, candidateID string) bool {
	if round.Outcome != "discover" || round.State != RoundRunning || candidateID == "" {
		return false
	}
	var cursor struct {
		SelectedDiscovery *struct {
			CandidateID string `json:"candidateId"`
		} `json:"selectedDiscovery"`
	}
	return json.Unmarshal(round.Cursor, &cursor) == nil && cursor.SelectedDiscovery != nil && cursor.SelectedDiscovery.CandidateID == candidateID
}

func (s *Store) CheckSelectedDiscoveryCandidate(ctx context.Context, roundID, candidateID string) error {
	round, err := s.Round(ctx, roundID)
	if err != nil {
		return err
	}
	if !selectedDiscoveryCandidate(round, candidateID) {
		return ErrFenced
	}
	return nil
}

type RoundDiscoveryCandidate struct {
	DiscoveryCandidate
	SourceSHA256 string
	ObservedAt   string
}

// RoundDiscoveryCandidates returns only charged, saved candidate facts from
// successful search reads in this exact round. They remain unverified leads.
func (s *Store) RoundDiscoveryCandidates(ctx context.Context, roundID string) ([]RoundDiscoveryCandidate, error) {
	if roundID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT dc.id,dc.attempt_id,dc.kind,dc.title,dc.url,dc.company_url,dc.evidence_quote,dc.created_at,
	  h.response_sha256,h.observed_at
	  FROM discovery_candidates dc JOIN round_attempts stage ON stage.id=dc.id
	  JOIN discovery_http h ON h.attempt_id=dc.attempt_id
	  JOIN round_attempts source ON source.id=h.attempt_id
	  WHERE stage.round_id=? AND stage.operation=? AND stage.state='succeeded'
	    AND source.round_id=? AND source.state='succeeded' AND h.method='search_jobs' AND h.status_code=200 AND h.error_code=''
	  ORDER BY dc.created_at,dc.id`, roundID, RoundStageDiscovery, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoundDiscoveryCandidate
	for rows.Next() {
		var c RoundDiscoveryCandidate
		if err := rows.Scan(&c.ID, &c.AttemptID, &c.Kind, &c.Title, &c.URL, &c.CompanyURL, &c.EvidenceQuote, &c.CreatedAt, &c.SourceSHA256, &c.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RoundDiscoveryBoardForCandidate(ctx context.Context, roundID, candidateID string) (string, error) {
	if roundID == "" || candidateID == "" {
		return "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT result_json FROM round_attempts
	  WHERE round_id=? AND operation=? AND state='succeeded' ORDER BY created_at,id`, roundID, RoundRegisterDiscoveryBoard)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return "", err
		}
		var board DiscoveryBoardResult
		if json.Unmarshal([]byte(result), &board) != nil {
			return "", ErrInvalid
		}
		if board.CandidateID == candidateID {
			return board.BoardID, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", ErrNotFound
}

func (s *Store) RoundDiscoveryCandidate(ctx context.Context, roundID, candidateID string) (RoundDiscoveryCandidate, error) {
	candidates, err := s.RoundDiscoveryCandidates(ctx, roundID)
	if err != nil {
		return RoundDiscoveryCandidate{}, err
	}
	for _, c := range candidates {
		if c.ID == candidateID {
			return c, nil
		}
	}
	return RoundDiscoveryCandidate{}, ErrNotFound
}
