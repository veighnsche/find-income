package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// DiscoveryHTTP records the exact prepared request before dispatch and its
// single bounded response afterward, even when the round has stopped.
type DiscoveryHTTP struct {
	AttemptID, RoundID, Method, RequestJSON, Endpoint string
	StatusCode                                        int
	ResponseBody                                      []byte
	ResponseSHA256, ObservedAt, ErrorCode             string
}

func (s *Store) PrepareDiscoveryHTTP(ctx context.Context, v DiscoveryHTTP) error {
	if v.AttemptID == "" || v.RoundID == "" || v.Method == "" || v.RequestJSON == "" || v.Endpoint == "" || v.ObservedAt == "" || v.StatusCode != 0 || len(v.ResponseBody) != 0 || v.ErrorCode != "" {
		return ErrInvalid
	}
	if v.Method != "search_jobs" && v.Method != "get_company_details" && v.Method != "get_job_details" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state RoundAttemptState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM round_attempts WHERE id=? AND round_id=? AND operation=?`, v.AttemptID, v.RoundID, RoundSearchSource).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFenced
		}
		return err
	}
	if state != AttemptDispatched {
		return ErrFenced
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discovery_http(attempt_id,round_id,method,request_json,endpoint,status_code,response_body,response_sha256,observed_at,error_code) VALUES(?,?,?,?,?,0,?,'',?,'pending')`, v.AttemptID, v.RoundID, v.Method, v.RequestJSON, v.Endpoint, []byte{}, v.ObservedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteDiscoveryHTTP(ctx context.Context, v DiscoveryHTTP) error {
	if v.AttemptID == "" || v.ObservedAt == "" || v.StatusCode < 0 || v.StatusCode > 599 || len(v.ResponseBody) > 128<<10 || v.ErrorCode == "pending" {
		return ErrInvalid
	}
	if v.ResponseBody == nil {
		v.ResponseBody = []byte{}
	}
	sum := sha256.Sum256(v.ResponseBody)
	result, err := s.db.ExecContext(ctx, `UPDATE discovery_http SET status_code=?,response_body=?,response_sha256=?,observed_at=?,error_code=? WHERE attempt_id=? AND error_code='pending'`, v.StatusCode, v.ResponseBody, hex.EncodeToString(sum[:]), v.ObservedAt, v.ErrorCode, v.AttemptID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

func (s *Store) DiscoveryHTTP(ctx context.Context, attemptID string) (DiscoveryHTTP, error) {
	var v DiscoveryHTTP
	err := s.db.QueryRowContext(ctx, `SELECT attempt_id,round_id,method,request_json,endpoint,status_code,response_body,response_sha256,observed_at,error_code FROM discovery_http WHERE attempt_id=?`, attemptID).Scan(&v.AttemptID, &v.RoundID, &v.Method, &v.RequestJSON, &v.Endpoint, &v.StatusCode, &v.ResponseBody, &v.ResponseSHA256, &v.ObservedAt, &v.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// DiscoveryCandidate is a Codex-extracted lead, never employer verification.
type DiscoveryCandidate struct {
	ID, AttemptID, Kind, Title, URL, CompanyURL, EvidenceQuote, CreatedAt string
}

// DiscoveryContinuation reads the latest successful page boundary across
// commissioned rounds for the exact query. A caller must still obtain a new
// round reservation before fetching NextPage.
type DiscoveryContinuation struct {
	AttemptID      string `json:"attemptId"`
	Page           int    `json:"page"`
	NextPage       int    `json:"nextPage"`
	Omitted        int    `json:"omitted"`
	ResponseSHA256 string `json:"responseSha256"`
}

func (s *Store) DiscoverySearchContinuation(ctx context.Context, keyword, country string) (DiscoveryContinuation, error) {
	if strings.TrimSpace(keyword) != keyword || keyword == "" || strings.TrimSpace(country) != country {
		return DiscoveryContinuation{}, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.attempt_id,h.request_json,a.result_json FROM discovery_http h JOIN round_attempts a ON a.id=h.attempt_id WHERE h.method='search_jobs' AND h.status_code=200 AND h.error_code='' AND a.state='succeeded' ORDER BY h.observed_at DESC,h.attempt_id DESC`)
	if err != nil {
		return DiscoveryContinuation{}, err
	}
	defer rows.Close()
	var best DiscoveryContinuation
	for rows.Next() {
		var id, req, result string
		if err := rows.Scan(&id, &req, &result); err != nil {
			return DiscoveryContinuation{}, err
		}
		var call struct {
			Params struct {
				Arguments struct {
					Keyword string `json:"keyword"`
					Country string `json:"country"`
					Page    int    `json:"page"`
				} `json:"arguments"`
			} `json:"params"`
		}
		var page struct {
			Page           int
			NextPage       int
			Omitted        int
			ResponseSHA256 string
		}
		if json.Unmarshal([]byte(req), &call) != nil || json.Unmarshal([]byte(result), &page) != nil || call.Params.Arguments.Keyword != keyword || call.Params.Arguments.Country != country || call.Params.Arguments.Page != page.Page {
			continue
		}
		if page.Page > best.Page {
			best = DiscoveryContinuation{AttemptID: id, Page: page.Page, NextPage: page.NextPage, Omitted: page.Omitted, ResponseSHA256: page.ResponseSHA256}
		}
	}
	if err := rows.Err(); err != nil {
		return DiscoveryContinuation{}, err
	}
	if best.AttemptID == "" {
		return best, ErrNotFound
	}
	return best, nil
}

type DiscoveryStageInput struct {
	RoundID, Capability, RequestKey string
	Candidate                       DiscoveryCandidate
}
type DiscoveryStageResult struct{ StageAttemptID, CandidateID, SourceAttemptID, Digest string }

// StageDiscoveryCandidate charges one item and one tool for exactly one lead.
// The token supplies the actor; payload replay must match the saved digest.
func (s *Store) StageDiscoveryCandidate(ctx context.Context, in DiscoveryStageInput) (out DiscoveryStageResult, err error) {
	if in.RoundID == "" || in.Capability == "" || in.RequestKey == "" || len(in.RequestKey) > 200 || strings.TrimSpace(in.RequestKey) != in.RequestKey {
		return DiscoveryStageResult{}, ErrInvalid
	}
	c := in.Candidate
	v, err := s.DiscoveryHTTP(ctx, c.AttemptID)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	if v.StatusCode != 200 || v.ErrorCode != "" {
		return DiscoveryStageResult{}, ErrInvalid
	}
	text, err := discoveryText(v.ResponseBody)
	if err != nil || c.AttemptID == "" || (c.Kind != "job" && c.Kind != "company") || strings.TrimSpace(c.Title) == "" || len(c.Title) > 300 || len(c.EvidenceQuote) < 12 || len(c.EvidenceQuote) > 2000 || !strings.Contains(c.EvidenceQuote, c.Title) || !strings.Contains(text, c.EvidenceQuote) || !discoveryURLInText(text, c.URL, true) || c.CompanyURL != "" && !discoveryURLInText(text, c.CompanyURL, false) {
		return DiscoveryStageResult{}, ErrInvalid
	}
	payload, _ := json.Marshal(struct{ Operation, SourceAttemptID, Kind, Title, URL, CompanyURL, EvidenceQuote, CapabilityHash string }{RoundStageDiscovery, c.AttemptID, c.Kind, c.Title, c.URL, c.CompanyURL, c.EvidenceQuote, roundCapabilityHash(in.Capability)})
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	tx, round, err := s.roundWriter(ctx, in.RoundID)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	defer func() {
		if errors.Is(err, ErrExpired) {
			_, _ = s.ExpireRound(context.WithoutCancel(ctx), in.RoundID)
		}
	}()
	defer tx.Rollback()
	authority, err := verifyRoundToolCapabilityTx(ctx, tx, in.Capability, in.RoundID)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	if !scopeAllowsActor(round.Scope, authority.Actor) || !scopeAllows(round.Scope, RoundStageDiscovery, "discovery:himalayas") {
		return DiscoveryStageResult{}, ErrFenced
	}
	var oldDigest, oldResult string
	err = tx.QueryRowContext(ctx, `SELECT request_sha256,result_json FROM round_attempts WHERE round_id=? AND request_key=?`, in.RoundID, in.RequestKey).Scan(&oldDigest, &oldResult)
	if err == nil {
		if oldDigest != digest {
			return DiscoveryStageResult{}, ErrRoundIdempotencyConflict
		}
		if json.Unmarshal([]byte(oldResult), &out) != nil {
			return DiscoveryStageResult{}, ErrUncertain
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DiscoveryStageResult{}, err
	}
	if err = requireRoundState(round, RoundRunning); err != nil {
		return DiscoveryStageResult{}, err
	}
	var sourceState RoundAttemptState
	if err = tx.QueryRowContext(ctx, `SELECT state FROM round_attempts WHERE id=?`, c.AttemptID).Scan(&sourceState); err != nil {
		return DiscoveryStageResult{}, err
	}
	if sourceState != AttemptSucceeded {
		return DiscoveryStageResult{}, ErrFenced
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM discovery_candidates WHERE attempt_id=? AND kind=? AND url=?`, c.AttemptID, c.Kind, c.URL).Scan(&existingID)
	if err == nil {
		return DiscoveryStageResult{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DiscoveryStageResult{}, err
	}
	cost, _ := RoundOperationCost(RoundStageDiscovery)
	updated, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+?,items_used=items_used+?,tools_used=tools_used+?,turns_used=turns_used+?,revision=revision+1,updated_at=? WHERE id=? AND state='running' AND generation=? AND requests_used+?<=request_limit AND items_used+?<=item_limit AND tools_used+?<=tool_limit AND turns_used+?<=turn_limit`, cost.Requests, cost.Items, cost.Tools, cost.Turns, utcNow(), in.RoundID, round.Generation, cost.Requests, cost.Items, cost.Tools, cost.Turns)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	if n != 1 {
		return DiscoveryStageResult{}, ErrAllowance
	}
	attemptID, err := randomID()
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	resultID, err := randomID()
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO discovery_candidates(id,attempt_id,kind,title,url,company_url,evidence_quote,created_at) VALUES(?,?,?,?,?,?,?,?)`, attemptID, c.AttemptID, c.Kind, c.Title, c.URL, c.CompanyURL, c.EvidenceQuote, now)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	out = DiscoveryStageResult{StageAttemptID: attemptID, CandidateID: attemptID, SourceAttemptID: c.AttemptID, Digest: digest}
	resultJSON, _ := json.Marshal(out)
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes(id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at) VALUES(?,?,?,?,?,?,1,?)`, auditID, authority.Actor.Kind, authority.Actor.ID, RoundStageDiscovery, "discovery_candidate", attemptID, now)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_attempts(id,round_id,request_key,request_sha256,operation,resource_id,generation,state,requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,'succeeded',?,?,?,?,?,?,?,?)`, attemptID, in.RoundID, in.RequestKey, digest, RoundStageDiscovery, "discovery:himalayas", round.Generation, cost.Requests, cost.Items, cost.Tools, cost.Turns, string(resultJSON), now, now, now)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_record_changes(round_id,attempt_id,audit_id,attached_at) VALUES(?,?,?,?)`, in.RoundID, attemptID, auditID, now)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_results(id,round_id,attempt_id,result_json,created_at) VALUES(?,?,?,?,?)`, resultID, in.RoundID, attemptID, string(resultJSON), now)
	if err != nil {
		return DiscoveryStageResult{}, err
	}
	return out, tx.Commit()
}

func discoveryURLInText(text, raw string, providerOnly bool) bool {
	idx := strings.Index(text, raw)
	if raw == "" || idx < 0 {
		return false
	}
	end := idx + len(raw)
	if end < len(text) && !strings.ContainsRune(" \n\r\t<>)]}", rune(text[end])) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && (!providerOnly || u.Host == "himalayas.app") && u.User == nil && u.Fragment == ""
}

func discoveryText(raw []byte) (string, error) {
	data := raw
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("event:")) {
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				data = line[len("data: "):]
				break
			}
		}
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" {
		return "", ErrInvalid
	}
	return envelope.Result.Content[0].Text, nil
}
