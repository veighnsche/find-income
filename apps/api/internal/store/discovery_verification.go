package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var discoveryWebsiteLine = regexp.MustCompile(`(?m)🌐 \*\*Website:\*\* (https://[^\s]+)`)
var discoverySlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type DiscoveryCompanyClaim struct {
	CandidateID, CompanyDetailAttemptID, CompanySlug, CompanyName string
	ClaimedWebsiteURL, SourceURL                                  string
}

type DiscoveryOfficialRead struct {
	AttemptID, RoundID, CandidateID, CompanyDetailAttemptID, ParentAttemptID string
	SourceURL, ClaimURL, Status, ObservedAt                                  string
	Snapshot                                                                 json.RawMessage
}

func (s *Store) PrepareDiscoveryOfficialRead(ctx context.Context, v DiscoveryOfficialRead) error {
	if v.AttemptID == "" || v.RoundID == "" || v.CandidateID == "" || v.CompanyDetailAttemptID == "" || v.SourceURL == "" || v.ClaimURL == "" || v.ObservedAt == "" || v.Status != "" || len(v.Snapshot) != 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state RoundAttemptState
	if err = tx.QueryRowContext(ctx, `SELECT state FROM round_attempts WHERE id=? AND round_id=? AND operation=?`, v.AttemptID, v.RoundID, RoundFetchSource).Scan(&state); err != nil {
		return err
	}
	if state != AttemptDispatched {
		return ErrFenced
	}
	var parent any
	if v.ParentAttemptID != "" {
		parent = v.ParentAttemptID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discovery_official_reads(attempt_id,round_id,candidate_id,company_detail_attempt_id,parent_attempt_id,source_url,claim_url,status,snapshot_json,observed_at) VALUES(?,?,?,?,?,?,?,'pending',NULL,?)`, v.AttemptID, v.RoundID, v.CandidateID, v.CompanyDetailAttemptID, parent, v.SourceURL, v.ClaimURL, v.ObservedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteDiscoveryOfficialRead(ctx context.Context, attemptID, status string, snapshot json.RawMessage, observedAt string) error {
	if attemptID == "" || status == "" || status == "pending" || len(status) > 100 || len(snapshot) > 256<<10 || observedAt == "" || len(snapshot) > 0 && !json.Valid(snapshot) {
		return ErrInvalid
	}
	var result any
	if len(snapshot) > 0 {
		result = string(snapshot)
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE discovery_official_reads SET status=?,snapshot_json=?,observed_at=? WHERE attempt_id=? AND status='pending'`, status, result, observedAt, attemptID)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

func (s *Store) DiscoveryOfficialRead(ctx context.Context, id string) (DiscoveryOfficialRead, error) {
	var v DiscoveryOfficialRead
	var parent, snapshot sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT attempt_id,round_id,candidate_id,company_detail_attempt_id,parent_attempt_id,source_url,claim_url,status,snapshot_json,observed_at FROM discovery_official_reads WHERE attempt_id=?`, id).Scan(&v.AttemptID, &v.RoundID, &v.CandidateID, &v.CompanyDetailAttemptID, &parent, &v.SourceURL, &v.ClaimURL, &v.Status, &snapshot, &v.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.ParentAttemptID = parent.String
	if snapshot.Valid {
		v.Snapshot = json.RawMessage(snapshot.String)
	}
	return v, nil
}

// HasDiscoveryOfficialRead reports whether a turn attempted the official-site
// check for a candidate, regardless of the read outcome.
func (s *Store) HasDiscoveryOfficialRead(ctx context.Context, roundID, candidateID string) (bool, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_official_reads WHERE round_id=? AND candidate_id=?`, roundID, candidateID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// DiscoveryCompanyClaim ties a staged lead to a matching company-detail read.
// The returned website remains an aggregator claim until an official read.
func (s *Store) DiscoveryCompanyClaim(ctx context.Context, candidateID, detailAttemptID, ownerID string) (DiscoveryCompanyClaim, error) {
	if candidateID == "" || detailAttemptID == "" || ownerID == "" {
		return DiscoveryCompanyClaim{}, ErrInvalid
	}
	var c DiscoveryCompanyClaim
	var sourceURL, request string
	var body []byte
	var candidateState, sourceState, detailState string
	var detailMethod string
	var status int
	var errorCode string
	err := s.db.QueryRowContext(ctx, `SELECT dc.url,da.state,sa.state,ha.method,ha.request_json,ha.response_body,ha.status_code,ha.error_code,ta.state FROM discovery_candidates dc JOIN round_attempts da ON da.id=dc.id JOIN rounds dr ON dr.id=da.round_id JOIN round_attempts sa ON sa.id=dc.attempt_id JOIN discovery_http ha ON ha.attempt_id=? JOIN round_attempts ta ON ta.id=ha.attempt_id JOIN rounds tr ON tr.id=ta.round_id WHERE dc.id=? AND dr.actor_kind='administrator' AND dr.actor_id=? AND tr.actor_kind='administrator' AND tr.actor_id=?`, detailAttemptID, candidateID, ownerID, ownerID).Scan(&sourceURL, &candidateState, &sourceState, &detailMethod, &request, &body, &status, &errorCode, &detailState)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if candidateState != "succeeded" || sourceState != "succeeded" || detailState != "succeeded" || detailMethod != "get_company_details" || status != 200 || errorCode != "" {
		return c, ErrFenced
	}
	u, err := url.Parse(sourceURL)
	if err != nil || u.Scheme != "https" || u.Host != "himalayas.app" || u.User != nil {
		return c, ErrInvalid
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 && len(parts) != 4 || parts[0] != "companies" || !discoverySlug.MatchString(parts[1]) || len(parts) == 4 && (parts[2] != "jobs" || !discoverySlug.MatchString(parts[3])) {
		return c, ErrInvalid
	}
	var call struct {
		Params struct {
			Arguments struct {
				CompanySlug string `json:"company_slug"`
			} `json:"arguments"`
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(request), &call) != nil || call.Params.Name != "get_company_details" || call.Params.Arguments.CompanySlug != parts[1] {
		return c, ErrFenced
	}
	text, err := discoveryText(body)
	if err != nil {
		return c, err
	}
	match := discoveryWebsiteLine.FindStringSubmatch(text)
	if len(match) != 2 {
		return c, ErrNotFound
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(text, "\n", 2)[0], "# "))
	name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(name, "✅ Verified"), "✅"))
	if name == "" || len(name) > 200 {
		return c, ErrInvalid
	}
	c = DiscoveryCompanyClaim{CandidateID: candidateID, CompanyDetailAttemptID: detailAttemptID, CompanySlug: parts[1], CompanyName: name, ClaimedWebsiteURL: match[1], SourceURL: sourceURL}
	return c, nil
}
