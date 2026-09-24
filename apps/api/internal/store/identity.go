// Identity judgment support (lane D, T14): T06 §6 canonicalization,
// broad candidate retrieval, round-actor resolution, and the
// caller-assigned-id dynamic-assessment insert the AssessmentSink needs.
// Reuses the T07 helpers (keys, sightings, links) without duplicating them.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Identity key namespaces (T06 §6, open-namespace rule C6).
const (
	// IdentityNamespaceEmployerDomain keys employer hosts (lowercase,
	// trailing dot stripped).
	IdentityNamespaceEmployerDomain = "employer_domain"
	// IdentityNamespaceCanonicalURL keys T06-canonical posting URLs.
	IdentityNamespaceCanonicalURL = "canonical_url"
	// IdentityNamespaceReqIDPrefix prefixes issuer req-id namespaces
	// (issuer_req_id:<issuer>, trimmed + case-folded).
	IdentityNamespaceReqIDPrefix = "issuer_req_id:"
	// IdentityNamespaceBoardPrefix prefixes board record namespaces
	// (board_record_id:<provider>:<board>).
	IdentityNamespaceBoardPrefix = "board_record_id:"
)

// CanonicalIdentityURL canonicalizes a posting URL for identity keys ONLY
// (fingerprints stay exact). Rules (T06 §6): lowercase scheme/host, strip
// the trailing host dot, drop the default port, drop the fragment, keep
// path/query byte-identical (no re-encoding, no empty-path normalization).
// URLs with userinfo are rejected: credentials never belong in keys.
func CanonicalIdentityURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	scheme, rest, ok := splitURLScheme(trimmed)
	if !ok {
		return "", fmt.Errorf("%w: identity URL needs a scheme://host", ErrInvalid)
	}
	scheme = strings.ToLower(scheme)
	if !validURLScheme(scheme) {
		return "", fmt.Errorf("%w: bad identity URL scheme", ErrInvalid)
	}
	authority := rest
	remainder := ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, remainder = rest[:i], rest[i:]
	}
	if strings.Contains(authority, "@") {
		return "", fmt.Errorf("%w: identity URL must not carry userinfo", ErrInvalid)
	}
	host, port, ok := splitURLAuthority(authority)
	if !ok || host == "" {
		return "", fmt.Errorf("%w: identity URL needs a host", ErrInvalid)
	}
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", fmt.Errorf("%w: identity URL needs a host", ErrInvalid)
	}
	if port != "" {
		for _, r := range port {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("%w: bad identity URL port", ErrInvalid)
			}
		}
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
	}
	if i := strings.IndexByte(remainder, '#'); i >= 0 {
		remainder = remainder[:i]
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + remainder, nil
}

func splitURLScheme(trimmed string) (scheme, rest string, ok bool) {
	i := strings.Index(trimmed, "://")
	if i <= 0 {
		return "", "", false
	}
	return trimmed[:i], trimmed[i+3:], true
}

func validURLScheme(scheme string) bool {
	for i, r := range scheme {
		lower := r >= 'a' && r <= 'z'
		if i == 0 {
			if !lower {
				return false
			}
			continue
		}
		if !lower && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return scheme != ""
}

func splitURLAuthority(authority string) (host, port string, ok bool) {
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", false
		}
		host, rest := authority[:end+1], authority[end+1:]
		if rest == "" {
			return host, "", true
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return host, rest[1:], true
	}
	if strings.Count(authority, ":") > 1 {
		return "", "", false
	}
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		return authority[:i], authority[i+1:], true
	}
	return authority, "", true
}

// CanonicalEmployerDomain normalizes an employer host for the employer_domain
// namespace: lowercase, trailing dot stripped (T06 §6).
func CanonicalEmployerDomain(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// CanonicalReqID normalizes a requisition id for issuer_req_id namespaces:
// trim + case-fold (T06 §6). The original spelling stays in evidence.
func CanonicalReqID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// IdentityURLHost returns the canonical host of a raw URL for employer_domain
// derivation, or "" when the URL does not canonicalize.
func IdentityURLHost(raw string) string {
	canonical, err := CanonicalIdentityURL(raw)
	if err != nil {
		return ""
	}
	rest := canonical[strings.Index(canonical, "://")+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	host, _, ok := splitURLAuthority(rest)
	if !ok {
		return ""
	}
	return host
}

// IssuerReqIDNamespace builds the open namespace for one req-id issuer
// (trimmed + case-folded). Namespaces are shape-validated, never
// membership-checked (C6).
func IssuerReqIDNamespace(issuer string) (string, error) {
	issuer = strings.ToLower(strings.TrimSpace(issuer))
	if issuer == "" {
		return "", fmt.Errorf("%w: req-id issuer required", ErrInvalid)
	}
	namespace := IdentityNamespaceReqIDPrefix + issuer
	if !ValidIdentityNamespace(namespace) {
		return "", fmt.Errorf("%w: invalid req-id issuer namespace", ErrInvalid)
	}
	return namespace, nil
}

// BoardRecordNamespace builds the open namespace for one provider board.
// Namespace segments are lowercased to fit the namespace shape; the record
// id VALUE stays case-sensitive (T06 §6).
func BoardRecordNamespace(provider, board string) (string, error) {
	provider, board = strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(board))
	if provider == "" || board == "" {
		return "", fmt.Errorf("%w: board provider and board required", ErrInvalid)
	}
	namespace := IdentityNamespaceBoardPrefix + provider + ":" + board
	if !ValidIdentityNamespace(namespace) {
		return "", fmt.Errorf("%w: invalid board record namespace", ErrInvalid)
	}
	return namespace, nil
}

// IdentityCandidate is one broad-retrieval hit with its live revision.
// Retrieval broadens; it never decides identity by itself.
type IdentityCandidate struct {
	Kind     string // company|opportunity
	ID       string
	Revision int64
	Title    string // opportunity title or company name
	Location string // opportunity location_text, else ""
}

// GetIdentityCandidate resolves one record by kind + id with its live
// revision, or ErrNotFound. Archived records resolve: matching advises and
// the save transaction decides.
func GetIdentityCandidate(ctx context.Context, r Reader, kind, id string) (IdentityCandidate, error) {
	switch kind {
	case "company":
		var c IdentityCandidate
		c.Kind = kind
		if err := r.QueryRowContext(ctx, `SELECT id,name,revision FROM companies WHERE id=?`,
			id).Scan(&c.ID, &c.Title, &c.Revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return IdentityCandidate{}, ErrNotFound
			}
			return IdentityCandidate{}, err
		}
		return c, nil
	case "opportunity":
		var c IdentityCandidate
		c.Kind = kind
		if err := r.QueryRowContext(ctx, `SELECT id,title,location_text,revision FROM opportunities WHERE id=?`,
			id).Scan(&c.ID, &c.Title, &c.Location, &c.Revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return IdentityCandidate{}, ErrNotFound
			}
			return IdentityCandidate{}, err
		}
		return c, nil
	default:
		return IdentityCandidate{}, fmt.Errorf("%w: candidate kind must be company|opportunity", ErrInvalid)
	}
}

// SearchOpportunityCandidatesFTS runs broad full-text retrieval over
// opportunities(title, location_text) (unicode61, T06 §6). Attribute words
// are OR-joined; an empty query matches nothing, never everything.
func SearchOpportunityCandidatesFTS(ctx context.Context, r Reader, title, location string, limit int) ([]IdentityCandidate, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := ftsOrQuery(title + " " + location)
	if query == "" {
		return nil, nil
	}
	rows, err := r.QueryContext(ctx, `SELECT o.id,o.title,o.location_text,o.revision
  FROM opportunities_fts JOIN opportunities o ON o.rowid=opportunities_fts.rowid
  WHERE opportunities_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityCandidate
	for rows.Next() {
		var c IdentityCandidate
		c.Kind = "opportunity"
		if err := rows.Scan(&c.ID, &c.Title, &c.Location, &c.Revision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ftsOrQuery quotes attribute words for an FTS5 OR query. Quoting keeps
// user text inert: no query operators or column filters escape.
func ftsOrQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > 12 {
		words = words[:12]
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		quoted = append(quoted, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// FindOpportunitiesByTitle looks up the title btree (case-insensitive).
func FindOpportunitiesByTitle(ctx context.Context, r Reader, title string, limit int) ([]IdentityCandidate, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := r.QueryContext(ctx, `SELECT id,title,location_text,revision FROM opportunities
  WHERE title=? COLLATE NOCASE ORDER BY updated_at DESC,id LIMIT ?`, title, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityCandidate
	for rows.Next() {
		var c IdentityCandidate
		c.Kind = "opportunity"
		if err := rows.Scan(&c.ID, &c.Title, &c.Location, &c.Revision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FindCompaniesByName looks up the name btree (case-insensitive).
func FindCompaniesByName(ctx context.Context, r Reader, name string, limit int) ([]IdentityCandidate, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := r.QueryContext(ctx, `SELECT id,name,revision FROM companies
  WHERE name=? COLLATE NOCASE ORDER BY updated_at DESC,id LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityCandidate
	for rows.Next() {
		var c IdentityCandidate
		c.Kind = "company"
		if err := rows.Scan(&c.ID, &c.Title, &c.Revision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListIdentityKeysByValue returns current + historical key rows for one
// (namespace, value), current first. Reused identifiers surface here as
// superseded rows beside their successors (C7, R02/R03).
func ListIdentityKeysByValue(ctx context.Context, r Reader, namespace, keyValue string) ([]EntityIdentityKey, error) {
	return listEntityIdentityKeys(ctx, r,
		`WHERE namespace=? AND key_value=? ORDER BY status,created_at,id`, namespace, keyValue)
}

// ListReqIDKeys returns current + historical rows for one requisition id
// across every issuer namespace (the matcher never assumes the issuer).
func ListReqIDKeys(ctx context.Context, r Reader, reqID string) ([]EntityIdentityKey, error) {
	value := CanonicalReqID(reqID)
	if value == "" {
		return nil, nil
	}
	return listEntityIdentityKeys(ctx, r,
		`WHERE namespace LIKE 'issuer_req_id:%' AND key_value=? ORDER BY status,created_at,id`, value)
}

// ResearchRoundActor resolves the account scope of one run for assessment
// persistence (T09: actor scope is resolved from the run at write time).
func ResearchRoundActor(ctx context.Context, r Reader, roundID string) (Actor, error) {
	if roundID == "" {
		return Actor{}, fmt.Errorf("%w: round required", ErrInvalid)
	}
	var actor Actor
	if err := r.QueryRowContext(ctx, `SELECT actor_kind,actor_id FROM rounds WHERE id=?`,
		roundID).Scan(&actor.Kind, &actor.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, ErrNotFound
		}
		return Actor{}, err
	}
	return actor, nil
}

// InsertSeededDynamicAssessment stores one immutable assessment binding under
// a caller-assigned id (the assessment id the Jev handler returned; the link
// table and explanation reads join on it). Field rules mirror
// InsertDynamicAssessment; only the id source differs.
func InsertSeededDynamicAssessment(ctx context.Context, db ResearchDB, actor Actor, id string, in DynamicAssessmentInput) (DynamicAssessment, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return DynamicAssessment{}, fmt.Errorf("%w: assessment id required", ErrInvalid)
	}
	if in.RoundID == "" || in.JevAttemptID == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: round/jev_attempt required", ErrInvalid)
	}
	if strings.TrimSpace(in.Purpose) == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: purpose required", ErrInvalid)
	}
	if in.QuestionsJSON == "" || in.EvidenceRefsJSON == "" || in.AnswersJSON == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: questions/evidence/answers JSON required", ErrInvalid)
	}
	if in.ProfileVersion <= 0 || in.RubricVersion == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: profile/rubric version required", ErrInvalid)
	}
	if in.CandidatesJSON == "" || len(in.CandidateSetHash) != 64 {
		return DynamicAssessment{}, fmt.Errorf("%w: candidates/set hash required", ErrInvalid)
	}
	if len(in.ReuseKey) != 64 {
		return DynamicAssessment{}, fmt.Errorf("%w: reuse key required", ErrInvalid)
	}
	if !validEnum(in.Status, DynamicAssessmentSucceeded, DynamicAssessmentPartialAbstain,
		DynamicAssessmentInvalidResponse, DynamicAssessmentFailed) {
		return DynamicAssessment{}, fmt.Errorf("%w: unknown assessment status %q", ErrInvalid, in.Status)
	}
	now := recordNow()
	a := DynamicAssessment{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID, RoundID: in.RoundID,
		JevAttemptID: in.JevAttemptID, Purpose: in.Purpose,
		QuestionsJSON: in.QuestionsJSON, EvidenceRefsJSON: in.EvidenceRefsJSON,
		ProfileVersion: in.ProfileVersion, RubricVersion: in.RubricVersion,
		CandidatesJSON: in.CandidatesJSON, CandidateSetHash: in.CandidateSetHash,
		RequestedModel: in.RequestedModel, ReuseKey: in.ReuseKey,
		Status: in.Status, AnswersJSON: in.AnswersJSON,
		SupersedesID: in.SupersedesID, CreatedAt: now,
	}
	_, err := db.ExecContext(ctx, `INSERT INTO jev_assessments_dynamic
  (id,actor_kind,actor_id,round_id,jev_attempt_id,purpose,questions_json,
   evidence_refs_json,profile_version,rubric_version,candidates_json,
   candidate_set_hash,requested_model,reuse_key,status,answers_json,
   supersedes_id,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ActorKind, a.ActorID, a.RoundID, a.JevAttemptID, a.Purpose,
		a.QuestionsJSON, a.EvidenceRefsJSON, a.ProfileVersion, a.RubricVersion,
		a.CandidatesJSON, a.CandidateSetHash, nullString(a.RequestedModel),
		a.ReuseKey, a.Status, a.AnswersJSON, nullString(a.SupersedesID), a.CreatedAt)
	if err != nil {
		return DynamicAssessment{}, err
	}
	return a, nil
}
