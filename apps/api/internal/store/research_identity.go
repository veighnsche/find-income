package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Identity subject/entity kinds and decision values (D §1.1, confirmed).
const (
	IdentitySubjectEmployer = "employer"
	IdentitySubjectVacancy  = "vacancy"

	IdentityDecisionSame       = "same"
	IdentityDecisionNew        = "new"
	IdentityDecisionUnresolved = "unresolved"
)

// Identity key strength and reuse status (D §1.2, C5-C8).
const (
	IdentityKeyStrong = "strong"
	IdentityKeyAlias  = "alias"

	IdentityKeyCurrent    = "current"
	IdentityKeySuperseded = "superseded"
)

// Record sighting kinds (D §3.1).
const (
	SightingFirst            = "first"
	SightingUnchanged        = "unchanged"
	SightingChanged          = "changed"
	SightingReusedIdentifier = "reused_identifier"
)

// IdentityDecisionRecord is one stored same/new/unresolved call. Candidates
// carry kind + revision (C1); the set hash is canonical (C2); decision=same
// names exactly one chosen target plus its revision (C3). Rows are
// advice-until-commit: the save transaction rechecks candidates/revisions.
type IdentityDecisionRecord struct {
	ID                   string
	ActorKind            string
	ActorID              string
	RoundID              string
	SubjectKind          string
	ObservationRefs      string
	Candidates           []researchcontract.CandidateIdentity
	CandidateSetHash     string
	Decision             string
	DecisionBasis        string
	SubjectCompanyID     string
	SubjectOpportunityID string
	SubjectRevision      int64
	DistinguishingRefs   string
	BriefIndependent     bool
	JevAssessmentID      string
	SupersedesID         string
	CreatedAt            string
}

// IdentityDecisionInput carries the caller-supplied decision fields. The
// candidate JSON and set hash are derived canonically from Candidates.
type IdentityDecisionInput struct {
	RoundID              string
	SubjectKind          string
	ObservationRefs      string
	Candidates           []researchcontract.CandidateIdentity
	Decision             string
	DecisionBasis        string
	SubjectCompanyID     string
	SubjectOpportunityID string
	SubjectRevision      int64
	DistinguishingRefs   string
	JevAssessmentID      string
	SupersedesID         string
}

// EntityIdentityKey is one strong identity or alias row.
type EntityIdentityKey struct {
	ID            string
	ActorKind     string
	ActorID       string
	EntityKind    string
	Namespace     string
	KeyValue      string
	Strength      string
	CompanyID     string
	OpportunityID string
	EvidenceRefs  string
	Status        string
	SupersedesID  string
	CreatedAt     string
}

// EntityIdentityKeyInput carries the caller-supplied key fields. Status
// defaults to current.
type EntityIdentityKeyInput struct {
	EntityKind    string
	Namespace     string
	KeyValue      string
	Strength      string
	CompanyID     string
	OpportunityID string
	EvidenceRefs  string
	Status        string
	SupersedesID  string
}

// RecordSighting is one immutable cross-post sighting for a research-saved
// record. Never shares rows with the ingestion path's source_sightings.
type RecordSighting struct {
	ID            string
	ActorKind     string
	ActorID       string
	CompanyID     string
	OpportunityID string
	CaptureID     string
	ObservedURL   string
	FinalURL      string
	ContentSHA256 string
	SightingKind  string
	ObservedAt    string
	RecordedAt    string
}

// RecordSightingInput carries the caller-supplied sighting fields. Empty
// ObservedAt/RecordedAt default to now.
type RecordSightingInput struct {
	CompanyID     string
	OpportunityID string
	CaptureID     string
	ObservedURL   string
	FinalURL      string
	ContentSHA256 string
	SightingKind  string
	ObservedAt    string
	RecordedAt    string
}

// CanonicalCandidateSet sorts entries by (kind, candidateId) and returns the
// canonical JSON plus candidate_set_hash = sha256 over it (C2). D's save
// transaction recomputes this; mismatch means revision_conflict.
func CanonicalCandidateSet(cands []researchcontract.CandidateIdentity) (jsonText, hash string, err error) {
	sorted := append([]researchcontract.CandidateIdentity(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].CandidateID < sorted[j].CandidateID
	})
	for _, c := range sorted {
		if c.Kind != "company" && c.Kind != "opportunity" {
			return "", "", fmt.Errorf("%w: candidate kind must be company|opportunity", ErrInvalid)
		}
		if c.CandidateID == "" || c.Revision <= 0 {
			return "", "", fmt.Errorf("%w: candidate id/revision required", ErrInvalid)
		}
	}
	if sorted == nil {
		sorted = []researchcontract.CandidateIdentity{}
	}
	raw, err := json.Marshal(sorted)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

// CanonicalEvidenceRefsJSON sorts refs by (capture, span_start, span_end) and
// returns the canonical JSON stored in evidence_refs columns. Key names follow
// the contract type (camelCase); this helper is the single canonical form D's
// reuse-key computation must hash.
func CanonicalEvidenceRefsJSON(refs []researchcontract.EvidenceRef) (string, error) {
	sorted := append([]researchcontract.EvidenceRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CaptureID != sorted[j].CaptureID {
			return sorted[i].CaptureID < sorted[j].CaptureID
		}
		if sorted[i].SpanStart != sorted[j].SpanStart {
			return sorted[i].SpanStart < sorted[j].SpanStart
		}
		return sorted[i].SpanEnd < sorted[j].SpanEnd
	})
	if sorted == nil {
		sorted = []researchcontract.EvidenceRef{}
	}
	raw, err := json.Marshal(sorted)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

const identityDecisionColumns = `id,actor_kind,actor_id,round_id,subject_kind,` +
	`observation_refs_json,candidate_ids_json,candidate_set_hash,decision,` +
	`decision_basis,subject_company_id,subject_opportunity_id,subject_revision,` +
	`distinguishing_refs_json,brief_independent,jev_assessment_id,supersedes_id,created_at`

// InsertIdentityDecision stores one decision with its canonical candidate set.
func InsertIdentityDecision(ctx context.Context, db ResearchDB, actor Actor, in IdentityDecisionInput) (IdentityDecisionRecord, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return IdentityDecisionRecord{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if in.RoundID == "" {
		return IdentityDecisionRecord{}, fmt.Errorf("%w: round required", ErrInvalid)
	}
	if !validEnum(in.SubjectKind, IdentitySubjectEmployer, IdentitySubjectVacancy) {
		return IdentityDecisionRecord{}, fmt.Errorf("%w: subject kind must be employer|vacancy", ErrInvalid)
	}
	if !validEnum(in.Decision, IdentityDecisionSame, IdentityDecisionNew, IdentityDecisionUnresolved) {
		return IdentityDecisionRecord{}, fmt.Errorf("%w: decision must be same|new|unresolved", ErrInvalid)
	}
	chosen := 0
	if in.SubjectCompanyID != "" {
		chosen++
	}
	if in.SubjectOpportunityID != "" {
		chosen++
	}
	if in.Decision == IdentityDecisionSame {
		if chosen != 1 || in.SubjectRevision <= 0 {
			return IdentityDecisionRecord{}, fmt.Errorf("%w: decision=same needs exactly one subject plus revision", ErrInvalid)
		}
	} else if chosen != 0 || in.SubjectRevision != 0 {
		return IdentityDecisionRecord{}, fmt.Errorf("%w: non-same decisions name no subject", ErrInvalid)
	}
	candidatesJSON, setHash, err := CanonicalCandidateSet(in.Candidates)
	if err != nil {
		return IdentityDecisionRecord{}, err
	}
	id, err := randomID()
	if err != nil {
		return IdentityDecisionRecord{}, err
	}
	now := recordNow()
	d := IdentityDecisionRecord{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID, RoundID: in.RoundID,
		SubjectKind:      in.SubjectKind,
		ObservationRefs:  defaultJSON(in.ObservationRefs, "[]"),
		Candidates:       append([]researchcontract.CandidateIdentity(nil), in.Candidates...),
		CandidateSetHash: setHash, Decision: in.Decision, DecisionBasis: in.DecisionBasis,
		SubjectCompanyID: in.SubjectCompanyID, SubjectOpportunityID: in.SubjectOpportunityID,
		SubjectRevision:    in.SubjectRevision,
		DistinguishingRefs: defaultJSON(in.DistinguishingRefs, "[]"),
		BriefIndependent:   true, JevAssessmentID: in.JevAssessmentID,
		SupersedesID: in.SupersedesID, CreatedAt: now,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO identity_decisions
  (id,actor_kind,actor_id,round_id,subject_kind,observation_refs_json,
   candidate_ids_json,candidate_set_hash,decision,decision_basis,
   subject_company_id,subject_opportunity_id,subject_revision,
   distinguishing_refs_json,brief_independent,jev_assessment_id,supersedes_id,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ActorKind, d.ActorID, d.RoundID, d.SubjectKind, d.ObservationRefs,
		candidatesJSON, d.CandidateSetHash, d.Decision, d.DecisionBasis,
		nullString(d.SubjectCompanyID), nullString(d.SubjectOpportunityID),
		nullRevision(d.SubjectRevision), d.DistinguishingRefs, 1,
		nullString(d.JevAssessmentID), nullString(d.SupersedesID), d.CreatedAt)
	if err != nil {
		return IdentityDecisionRecord{}, err
	}
	return d, nil
}

func nullRevision(revision int64) sql.NullInt64 {
	if revision <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: revision, Valid: true}
}

// GetIdentityDecision loads one decision by id.
func GetIdentityDecision(ctx context.Context, r Reader, id string) (IdentityDecisionRecord, error) {
	row := r.QueryRowContext(ctx, `SELECT `+identityDecisionColumns+`
  FROM identity_decisions WHERE id=?`, id)
	d, err := scanIdentityDecisionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return IdentityDecisionRecord{}, ErrNotFound
	}
	return d, err
}

// ListIdentityDecisionsBySubject returns newest-first decisions for one
// (subject_kind, decision) pair, e.g. unresolved vacancies awaiting
// re-investigation.
func ListIdentityDecisionsBySubject(ctx context.Context, r Reader, subjectKind, decision string, limit int) ([]IdentityDecisionRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.QueryContext(ctx, `SELECT `+identityDecisionColumns+`
  FROM identity_decisions WHERE subject_kind=? AND decision=?
  ORDER BY created_at DESC,id DESC LIMIT ?`, subjectKind, decision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityDecisionRecord
	for rows.Next() {
		var d IdentityDecisionRecord
		if err := scanIdentityDecisionInto(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanIdentityDecisionRow(row *sql.Row) (IdentityDecisionRecord, error) {
	var d IdentityDecisionRecord
	return d, scanIdentityDecisionInto(row, &d)
}

func scanIdentityDecisionInto(s researchRequestScanner, d *IdentityDecisionRecord) error {
	var candidatesJSON string
	var subjectCompany, subjectOpportunity, jev, supersedes sql.NullString
	var revision sql.NullInt64
	var briefIndependent int64
	if err := s.Scan(&d.ID, &d.ActorKind, &d.ActorID, &d.RoundID, &d.SubjectKind,
		&d.ObservationRefs, &candidatesJSON, &d.CandidateSetHash, &d.Decision,
		&d.DecisionBasis, &subjectCompany, &subjectOpportunity, &revision,
		&d.DistinguishingRefs, &briefIndependent, &jev, &supersedes, &d.CreatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(candidatesJSON), &d.Candidates); err != nil {
		return err
	}
	d.SubjectCompanyID = subjectCompany.String
	d.SubjectOpportunityID = subjectOpportunity.String
	if revision.Valid {
		d.SubjectRevision = revision.Int64
	}
	d.BriefIndependent = briefIndependent == 1
	d.JevAssessmentID = jev.String
	d.SupersedesID = supersedes.String
	return nil
}

var namespaceShape = regexp.MustCompile(`^[_a-z0-9:.-]{1,128}$`)

// ValidIdentityNamespace reports whether a namespace has valid open-namespace
// shape (C6). Namespaces are never membership-checked.
func ValidIdentityNamespace(namespace string) bool { return namespaceShape.MatchString(namespace) }

const entityIdentityKeyColumns = `id,actor_kind,actor_id,entity_kind,namespace,` +
	`key_value,strength,company_id,opportunity_id,evidence_refs_json,status,` +
	`supersedes_id,created_at`

// InsertEntityIdentityKey stores one strong identity or alias. Reused
// identifiers for materially different openings must be new rows superseding
// the old one, never overwrites (C7).
func InsertEntityIdentityKey(ctx context.Context, db ResearchDB, actor Actor, in EntityIdentityKeyInput) (EntityIdentityKey, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return EntityIdentityKey{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if !validEnum(in.EntityKind, IdentitySubjectEmployer, IdentitySubjectVacancy) {
		return EntityIdentityKey{}, fmt.Errorf("%w: entity kind must be employer|vacancy", ErrInvalid)
	}
	if !ValidIdentityNamespace(in.Namespace) {
		return EntityIdentityKey{}, fmt.Errorf("%w: invalid identity namespace shape", ErrInvalid)
	}
	if in.KeyValue == "" || len(in.KeyValue) > 1024 {
		return EntityIdentityKey{}, fmt.Errorf("%w: key value length 1..1024 required", ErrInvalid)
	}
	if !validEnum(in.Strength, IdentityKeyStrong, IdentityKeyAlias) {
		return EntityIdentityKey{}, fmt.Errorf("%w: strength must be strong|alias", ErrInvalid)
	}
	if (in.CompanyID == "") == (in.OpportunityID == "") {
		return EntityIdentityKey{}, fmt.Errorf("%w: exactly one of company/opportunity required", ErrInvalid)
	}
	status := in.Status
	if status == "" {
		status = IdentityKeyCurrent
	}
	if !validEnum(status, IdentityKeyCurrent, IdentityKeySuperseded) {
		return EntityIdentityKey{}, fmt.Errorf("%w: status must be current|superseded", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return EntityIdentityKey{}, err
	}
	now := recordNow()
	k := EntityIdentityKey{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID, EntityKind: in.EntityKind,
		Namespace: in.Namespace, KeyValue: in.KeyValue, Strength: in.Strength,
		CompanyID: in.CompanyID, OpportunityID: in.OpportunityID,
		EvidenceRefs: defaultJSON(in.EvidenceRefs, "[]"), Status: status,
		SupersedesID: in.SupersedesID, CreatedAt: now,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO entity_identity_keys
  (id,actor_kind,actor_id,entity_kind,namespace,key_value,strength,company_id,
   opportunity_id,evidence_refs_json,status,supersedes_id,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.ActorKind, k.ActorID, k.EntityKind, k.Namespace, k.KeyValue,
		k.Strength, nullString(k.CompanyID), nullString(k.OpportunityID),
		k.EvidenceRefs, k.Status, nullString(k.SupersedesID), k.CreatedAt)
	if err != nil {
		return EntityIdentityKey{}, err
	}
	return k, nil
}

// MarkEntityIdentityKeySuperseded flips one key row to superseded. Callers
// insert the superseding row in the same transaction.
func MarkEntityIdentityKeySuperseded(ctx context.Context, db ResearchDB, id string) error {
	res, err := db.ExecContext(ctx, `UPDATE entity_identity_keys
  SET status='superseded' WHERE id=? AND status='current'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// FindCurrentStrongKey resolves one live strong identity, or ErrNotFound.
func FindCurrentStrongKey(ctx context.Context, r Reader, namespace, keyValue string) (EntityIdentityKey, error) {
	row := r.QueryRowContext(ctx, `SELECT `+entityIdentityKeyColumns+`
  FROM entity_identity_keys
  WHERE namespace=? AND key_value=? AND strength='strong' AND status='current'`,
		namespace, keyValue)
	k, err := scanEntityIdentityKeyRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EntityIdentityKey{}, ErrNotFound
	}
	return k, err
}

// ListEntityIdentityKeysByCompany returns a company's keys, current first.
func ListEntityIdentityKeysByCompany(ctx context.Context, r Reader, companyID string) ([]EntityIdentityKey, error) {
	return listEntityIdentityKeys(ctx, r, `WHERE company_id=? ORDER BY status,created_at,id`, companyID)
}

// ListEntityIdentityKeysByOpportunity returns an opportunity's keys, current first.
func ListEntityIdentityKeysByOpportunity(ctx context.Context, r Reader, opportunityID string) ([]EntityIdentityKey, error) {
	return listEntityIdentityKeys(ctx, r, `WHERE opportunity_id=? ORDER BY status,created_at,id`, opportunityID)
}

func listEntityIdentityKeys(ctx context.Context, r Reader, suffix string, args ...any) ([]EntityIdentityKey, error) {
	rows, err := r.QueryContext(ctx, `SELECT `+entityIdentityKeyColumns+
		` FROM entity_identity_keys `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityIdentityKey
	for rows.Next() {
		var k EntityIdentityKey
		if err := scanEntityIdentityKeyInto(rows, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func scanEntityIdentityKeyRow(row *sql.Row) (EntityIdentityKey, error) {
	var k EntityIdentityKey
	return k, scanEntityIdentityKeyInto(row, &k)
}

func scanEntityIdentityKeyInto(s researchRequestScanner, k *EntityIdentityKey) error {
	var company, opportunity, supersedes sql.NullString
	if err := s.Scan(&k.ID, &k.ActorKind, &k.ActorID, &k.EntityKind, &k.Namespace,
		&k.KeyValue, &k.Strength, &company, &opportunity, &k.EvidenceRefs,
		&k.Status, &supersedes, &k.CreatedAt); err != nil {
		return err
	}
	k.CompanyID = company.String
	k.OpportunityID = opportunity.String
	k.SupersedesID = supersedes.String
	return nil
}

const recordSightingColumns = `id,actor_kind,actor_id,company_id,opportunity_id,` +
	`capture_id,observed_url,final_url,content_sha256,sighting_kind,observed_at,recorded_at`

// InsertRecordSighting appends one immutable sighting row.
func InsertRecordSighting(ctx context.Context, db ResearchDB, actor Actor, in RecordSightingInput) (RecordSighting, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return RecordSighting{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if (in.CompanyID == "") == (in.OpportunityID == "") {
		return RecordSighting{}, fmt.Errorf("%w: exactly one of company/opportunity required", ErrInvalid)
	}
	if in.CaptureID == "" || len(in.ContentSHA256) != 64 {
		return RecordSighting{}, fmt.Errorf("%w: capture/content sha256 required", ErrInvalid)
	}
	if !validEnum(in.SightingKind, SightingFirst, SightingUnchanged, SightingChanged, SightingReusedIdentifier) {
		return RecordSighting{}, fmt.Errorf("%w: unknown sighting kind %q", ErrInvalid, in.SightingKind)
	}
	id, err := randomID()
	if err != nil {
		return RecordSighting{}, err
	}
	now := recordNow()
	observed := in.ObservedAt
	if observed == "" {
		observed = now
	}
	recorded := in.RecordedAt
	if recorded == "" {
		recorded = now
	}
	s := RecordSighting{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID,
		CompanyID: in.CompanyID, OpportunityID: in.OpportunityID,
		CaptureID: in.CaptureID, ObservedURL: in.ObservedURL, FinalURL: in.FinalURL,
		ContentSHA256: in.ContentSHA256, SightingKind: in.SightingKind,
		ObservedAt: observed, RecordedAt: recorded,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO record_sightings
  (id,actor_kind,actor_id,company_id,opportunity_id,capture_id,observed_url,
   final_url,content_sha256,sighting_kind,observed_at,recorded_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.ActorKind, s.ActorID, nullString(s.CompanyID),
		nullString(s.OpportunityID), s.CaptureID, nullString(s.ObservedURL),
		nullString(s.FinalURL), s.ContentSHA256, s.SightingKind,
		s.ObservedAt, s.RecordedAt)
	if err != nil {
		return RecordSighting{}, err
	}
	return s, nil
}

// ListRecordSightingsByOpportunity returns an opportunity's sightings in
// observation order.
func ListRecordSightingsByOpportunity(ctx context.Context, r Reader, opportunityID string) ([]RecordSighting, error) {
	return listRecordSightings(ctx, r, `WHERE opportunity_id=? ORDER BY observed_at,id`, opportunityID)
}

// ListRecordSightingsByCompany returns a company's sightings in observation order.
func ListRecordSightingsByCompany(ctx context.Context, r Reader, companyID string) ([]RecordSighting, error) {
	return listRecordSightings(ctx, r, `WHERE company_id=? ORDER BY observed_at,id`, companyID)
}

// ListRecordSightingsByCapture returns every sighting bound to one capture.
func ListRecordSightingsByCapture(ctx context.Context, r Reader, captureID string) ([]RecordSighting, error) {
	return listRecordSightings(ctx, r, `WHERE capture_id=? ORDER BY observed_at,id`, captureID)
}

func listRecordSightings(ctx context.Context, r Reader, suffix string, args ...any) ([]RecordSighting, error) {
	rows, err := r.QueryContext(ctx, `SELECT `+recordSightingColumns+
		` FROM record_sightings `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecordSighting
	for rows.Next() {
		var s RecordSighting
		var company, opportunity, observedURL, finalURL sql.NullString
		if err := rows.Scan(&s.ID, &s.ActorKind, &s.ActorID, &company,
			&opportunity, &s.CaptureID, &observedURL, &finalURL, &s.ContentSHA256,
			&s.SightingKind, &s.ObservedAt, &s.RecordedAt); err != nil {
			return nil, err
		}
		s.CompanyID = company.String
		s.OpportunityID = opportunity.String
		s.ObservedURL = observedURL.String
		s.FinalURL = finalURL.String
		out = append(out, s)
	}
	return out, rows.Err()
}
