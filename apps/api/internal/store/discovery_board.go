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
	"time"
)

type DiscoveryBoardInput struct {
	RoundID                string `json:"roundId"`
	Capability             string `json:"capability"`
	RequestKey             string `json:"requestKey"`
	CandidateID            string `json:"candidateId"`
	OfficialLinksAttemptID string `json:"officialLinksAttemptId"`
	LeverURL               string `json:"leverUrl"`
}
type DiscoveryBoardResult struct{ AttemptID, BoardID, CandidateID, OfficialLinksAttemptID, Provider, Site, Region, OfficialCareersURL string }

type discoveryLinkProof struct {
	CandidateID            string
	CompanyDetailAttemptID string
	ParentAttemptID        string
	ClaimURL               string
	SourceURL              string
	Status                 string
	Depth                  int
	Evidence               struct {
		SourceURL string
		Status    string
		Links     []struct {
			URL  string
			Text string
		}
	}
}

func parseDiscoveryLinkProof(v DiscoveryOfficialRead) (discoveryLinkProof, error) {
	var p discoveryLinkProof
	if v.Status != "ok" || json.Unmarshal(v.Snapshot, &p) != nil || p.Status != "ok" || p.Evidence.Status != "ok" || p.CandidateID != v.CandidateID || p.CompanyDetailAttemptID != v.CompanyDetailAttemptID || p.ParentAttemptID != v.ParentAttemptID || p.ClaimURL != v.ClaimURL || p.SourceURL != v.SourceURL || p.Evidence.SourceURL != v.SourceURL || p.Depth < 0 || p.Depth > 1 {
		return p, ErrFenced
	}
	return p, nil
}

func leverBoardIdentity(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", "", ErrInvalid
	}
	region := "global"
	switch u.Host {
	case "jobs.lever.co":
	case "jobs.eu.lever.co":
		region = "eu"
	default:
		return "", "", ErrInvalid
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !collectorSitePattern.MatchString(parts[0]) {
		return "", "", ErrInvalid
	}
	if len(parts) == 2 && parts[1] == "" {
		return "", "", ErrInvalid
	}
	return parts[0], region, nil
}

// RegisterDiscoveryBoard publishes only an exact Lever anchor from a saved
// official-site read. Board, current-round scope, charge and audit commit once.
func (s *Store) RegisterDiscoveryBoard(ctx context.Context, in DiscoveryBoardInput) (out DiscoveryBoardResult, err error) {
	if in.RoundID == "" || in.Capability == "" || in.RequestKey == "" || len(in.RequestKey) > 200 || strings.TrimSpace(in.RequestKey) != in.RequestKey || in.CandidateID == "" || in.OfficialLinksAttemptID == "" || in.LeverURL == "" {
		return out, ErrInvalid
	}
	site, region, err := leverBoardIdentity(in.LeverURL)
	if err != nil {
		return out, err
	}
	proofRead, err := s.DiscoveryOfficialRead(ctx, in.OfficialLinksAttemptID)
	if err != nil {
		return out, err
	}
	proof, err := parseDiscoveryLinkProof(proofRead)
	if err != nil {
		return out, err
	}
	if proofRead.CandidateID != in.CandidateID {
		return out, ErrFenced
	}
	found := false
	for _, link := range proof.Evidence.Links {
		if link.URL == in.LeverURL {
			found = true
			break
		}
	}
	if !found {
		return out, ErrFenced
	}
	initialRound, err := s.Round(ctx, in.RoundID)
	if err != nil {
		return out, err
	}
	if !time.Now().Before(initialRound.Deadline) {
		_, _ = s.ExpireRound(ctx, in.RoundID)
		return out, ErrExpired
	}
	claim, err := s.DiscoveryCompanyClaim(ctx, in.CandidateID, proofRead.CompanyDetailAttemptID, initialRound.Actor.ID)
	if err != nil {
		return out, err
	}
	if claim.ClaimedWebsiteURL != proofRead.ClaimURL {
		return out, ErrFenced
	}
	claimURL, err := url.Parse(claim.ClaimedWebsiteURL)
	if err != nil {
		return out, ErrInvalid
	}
	claimURL.RawQuery = ""
	claimURL.ForceQuery = false
	claimURL.Fragment = ""
	claimURL.RawFragment = ""
	if claimURL.Path == "" {
		claimURL.Path = "/"
	}
	base := claimURL.String()
	if proof.Depth == 0 && proof.SourceURL != base || proof.Depth == 1 && proof.ParentAttemptID == "" {
		return out, ErrFenced
	}
	var parent DiscoveryOfficialRead
	if proof.Depth == 1 {
		parent, err = s.DiscoveryOfficialRead(ctx, proof.ParentAttemptID)
		if err != nil {
			return out, err
		}
		parentProof, parseErr := parseDiscoveryLinkProof(parent)
		if parseErr != nil {
			return out, parseErr
		}
		if parent.CandidateID != in.CandidateID || parent.CompanyDetailAttemptID != proofRead.CompanyDetailAttemptID || parent.ParentAttemptID != "" || parent.SourceURL != base {
			return out, ErrFenced
		}
		seen := false
		for _, link := range parentProof.Evidence.Links {
			if link.URL == proof.SourceURL {
				seen = true
				break
			}
		}
		next, parseErr := url.Parse(proof.SourceURL)
		if parseErr != nil || next.Hostname() != claimURL.Hostname() || !seen {
			return out, ErrFenced
		}
	}
	payload, _ := json.Marshal(struct{ Operation, CandidateID, OfficialLinksAttemptID, LeverURL, CapabilityHash string }{RoundRegisterDiscoveryBoard, in.CandidateID, in.OfficialLinksAttemptID, in.LeverURL, roundCapabilityHash(in.Capability)})
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	tx, r, err := s.roundWriter(ctx, in.RoundID)
	if err != nil {
		return out, err
	}
	defer func() {
		if errors.Is(err, ErrExpired) {
			_, _ = s.ExpireRound(context.WithoutCancel(ctx), in.RoundID)
		}
	}()
	defer tx.Rollback()
	authority, err := verifyRoundToolCapabilityTx(ctx, tx, in.Capability, in.RoundID)
	if err != nil {
		return out, err
	}
	if !scopeAllowsActor(r.Scope, authority.Actor) || !scopeAllows(r.Scope, RoundRegisterDiscoveryBoard, "discovery:himalayas") {
		return out, ErrFenced
	}
	if r.Actor != initialRound.Actor {
		return out, ErrFenced
	}
	var readState, readOwnerID string
	err = tx.QueryRowContext(ctx, `SELECT a.state,r.actor_id FROM round_attempts a JOIN rounds r ON r.id=a.round_id WHERE a.id=?`, in.OfficialLinksAttemptID).Scan(&readState, &readOwnerID)
	if err != nil {
		return out, err
	}
	if readState != "succeeded" || readOwnerID != r.Actor.ID {
		return out, ErrFenced
	}
	var savedResult []byte
	err = tx.QueryRowContext(ctx, `SELECT result_json FROM round_attempts WHERE id=?`, in.OfficialLinksAttemptID).Scan(&savedResult)
	if err != nil || !bytes.Equal(savedResult, proofRead.Snapshot) {
		return out, ErrFenced
	}
	if proof.Depth == 1 {
		var parentState, ownerID string
		if err = tx.QueryRowContext(ctx, `SELECT a.state,r.actor_id FROM round_attempts a JOIN rounds r ON r.id=a.round_id WHERE a.id=?`, parent.AttemptID).Scan(&parentState, &ownerID); err != nil {
			return out, err
		}
		if parentState != "succeeded" || ownerID != r.Actor.ID {
			return out, ErrFenced
		}
	}
	var oldDigest, oldResult string
	err = tx.QueryRowContext(ctx, `SELECT request_sha256,result_json FROM round_attempts WHERE round_id=? AND request_key=?`, in.RoundID, in.RequestKey).Scan(&oldDigest, &oldResult)
	if err == nil {
		if oldDigest != digest {
			return out, ErrRoundIdempotencyConflict
		}
		if json.Unmarshal([]byte(oldResult), &out) != nil {
			return out, ErrUncertain
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = requireRoundState(r, RoundRunning); err != nil {
		return out, err
	}
	var exists string
	err = tx.QueryRowContext(ctx, `SELECT id FROM collector_boards WHERE provider='lever' AND site=? AND region=?`, site, region).Scan(&exists)
	if err == nil {
		return out, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	input := CollectorBoardInput{Provider: "lever", Site: site, Region: region, DisplayName: claim.CompanyName, Enabled: true, IntervalMinutes: 60}
	if err = validateCollectorBoard(input); err != nil {
		return out, err
	}
	boardID, err := randomID()
	if err != nil {
		return out, err
	}
	newScope := r.Scope
	newScope.Resources = append(append([]string{}, r.Scope.Resources...), "board:"+boardID)
	if !validScope(newScope) {
		return out, ErrAllowance
	}
	scopeJSON, _ := json.Marshal(newScope)
	cost, _ := RoundOperationCost(RoundRegisterDiscoveryBoard)
	updated, err := tx.ExecContext(ctx, `UPDATE rounds SET scope_json=?,requests_used=requests_used+?,items_used=items_used+?,tools_used=tools_used+?,turns_used=turns_used+?,revision=revision+1,updated_at=? WHERE id=? AND state='running' AND generation=? AND requests_used+?<=request_limit AND items_used+?<=item_limit AND tools_used+?<=tool_limit AND turns_used+?<=turn_limit`, string(scopeJSON), cost.Requests, cost.Items, cost.Tools, cost.Turns, utcNow(), in.RoundID, r.Generation, cost.Requests, cost.Items, cost.Tools, cost.Turns)
	if err != nil {
		return out, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, ErrAllowance
	}
	now := utcNow()
	next := jobTime(time.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO collector_boards(id,provider,site,region,display_name,official_careers_url,verified_at,enabled,interval_minutes,next_scan_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,60,?,?,?)`, boardID, "lever", site, region, claim.CompanyName, proof.SourceURL, now, next, next, next)
	if err != nil {
		return out, err
	}
	attemptID, err := randomID()
	if err != nil {
		return out, err
	}
	auditID, err := randomID()
	if err != nil {
		return out, err
	}
	resultID, err := randomID()
	if err != nil {
		return out, err
	}
	out = DiscoveryBoardResult{AttemptID: attemptID, BoardID: boardID, CandidateID: in.CandidateID, OfficialLinksAttemptID: in.OfficialLinksAttemptID, Provider: "lever", Site: site, Region: region, OfficialCareersURL: proof.SourceURL}
	resultJSON, _ := json.Marshal(out)
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes(id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at) VALUES(?,?,?,?,?,?,1,?)`, auditID, authority.Actor.Kind, authority.Actor.ID, RoundRegisterDiscoveryBoard, "collector_board", boardID, now)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_attempts(id,round_id,request_key,request_sha256,operation,resource_id,generation,state,requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,'succeeded',?,?,?,?,?,?,?,?)`, attemptID, in.RoundID, in.RequestKey, digest, RoundRegisterDiscoveryBoard, "discovery:himalayas", r.Generation, cost.Requests, cost.Items, cost.Tools, cost.Turns, string(resultJSON), now, now, now)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_record_changes(round_id,attempt_id,audit_id,attached_at) VALUES(?,?,?,?)`, in.RoundID, attemptID, auditID, now)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_results(id,round_id,attempt_id,result_json,created_at) VALUES(?,?,?,?,?)`, resultID, in.RoundID, attemptID, string(resultJSON), now)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
