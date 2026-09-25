// Persisted Jev group classifications with selected personalized reasons
// (lane C, C3). The Jev Choice/Score call itself runs through jevservice and
// its exchange is retained by jevassess/jev_attempts; this store only persists
// the outcome: the group, up to three catalog reason selections with raw Jev
// support, optional conflict/missing singletons, evidence provenance and the
// pinned brief/rubric/catalog versions.
//
// Groups are Recommended / Could be recommended / Probably not recommended /
// Not recommended; Unknown is reserved for unusable evidence and carries an
// unknown_basis with no reason selections. Reason text renders verbatim from
// the bound catalog row: no per-job writing pass, no invented display
// threshold, and jevSupport is the raw recorded signal, never presented as
// verified correctness. Saving a finding commissions no deeper research and
// triggers no job check.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Jev finding groups, byte-identical to the FindingEntry.group contract enum.
const (
	FindingGroupRecommended            = "recommended"
	FindingGroupCouldBeRecommended     = "could_be_recommended"
	FindingGroupProbablyNotRecommended = "probably_not_recommended"
	FindingGroupNotRecommended         = "not_recommended"
	FindingGroupUnknown                = "unknown"
	FindingReasonPositive              = "positive"
	FindingReasonNegative              = "negative"
	FindingReasonMissingInformation    = "missing_information"
	FindingMaxReasons                  = 3
	FindingMaxEvidenceLinks            = 256
	FindingUnknownBasisMax             = 2000
	FindingSourceRefMax                = 256
	FindingObservedURLMax              = 2048
	FindingDefaultListLimit            = 50
	FindingMaxListLimit                = 100
	FindingStaleBriefChanged           = "brief_changed"
	FindingStaleCatalogChanged         = "catalog_changed"
	FindingStaleOpportunityRevised     = "opportunity_revised"
)

// FindingReason is one saved catalog reason selection with its raw Jev
// support signal. Label/detail are verbatim catalog text.
type FindingReason struct {
	ReasonID   string  `json:"reasonId"`
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	Detail     string  `json:"detail"`
	JevSupport float64 `json:"jevSupport"`
}

// FindingEvidenceLink pins one cited capture span plus the excerpt hash.
type FindingEvidenceLink struct {
	CaptureID     string `json:"captureId"`
	SpanStart     int64  `json:"spanStart"`
	SpanEnd       int64  `json:"spanEnd"`
	ExcerptSHA256 string `json:"excerptSha256"`
}

// FindingSourceRef is loose observed provenance for the classified vacancy.
type FindingSourceRef struct {
	SourceID       string `json:"sourceId"`
	SourceRevision string `json:"sourceRevision"`
	ObservedURL    string `json:"observedUrl,omitempty"`
}

// Finding is one immutable classification row plus read-time staleness.
// Stale/StaleBasis are computed on every read from live brief, catalog and
// opportunity state; they are never stored.
type Finding struct {
	ID                  string
	Actor               Actor
	RunID               string
	OpportunityID       string
	OpportunityRevision int64
	AssessmentID        string
	ProfileVersion      int64
	RubricVersion       string
	CatalogVersion      string
	CandidateSetHash    string
	ReuseKey            string
	Group               string
	UnknownBasis        string
	Reasons             []FindingReason
	Conflict            *ReasonChoice
	MissingFact         *ReasonChoice
	EvidenceLinks       []FindingEvidenceLink
	SourceRef           *FindingSourceRef
	Stale               bool
	StaleBasis          string
	CreatedAt           string
}

// FindingReasonInput carries one Jev-selected reason. Label/detail must
// repeat the bound catalog entry byte-for-byte.
type FindingReasonInput struct {
	ReasonID   string
	Kind       string
	Label      string
	Detail     string
	JevSupport float64
}

// FindingEvidenceLinkInput carries one cited capture span.
type FindingEvidenceLinkInput struct {
	CaptureID     string
	SpanStart     int64
	SpanEnd       int64
	ExcerptSHA256 string
}

// FindingSaveInput carries one classification outcome. Profile, rubric and
// catalog versions bind from the cited assessment plus its catalog row, never
// from caller-supplied copies.
type FindingSaveInput struct {
	RunID               string
	OpportunityID       string
	OpportunityRevision int64
	AssessmentID        string
	Group               string
	UnknownBasis        string
	Reasons             []FindingReasonInput
	Conflict            *ReasonChoice
	MissingFact         *ReasonChoice
	EvidenceLinks       []FindingEvidenceLinkInput
	SourceRef           *FindingSourceRef
}

// FindingListPage is one cursor page of latest-per-opportunity findings.
type FindingListPage struct {
	Items      []Finding
	NextCursor string
}

// findingReuseInput is the canonical dedup input: the judged evidence and
// versions, never the outcome. Same inputs always yield the same key, so an
// exact repeat replays instead of forking.
type findingReuseInput struct {
	RunID               string                `json:"runId"`
	OpportunityID       string                `json:"opportunityId"`
	OpportunityRevision int64                 `json:"opportunityRevision"`
	ProfileVersion      int64                 `json:"profileVersion"`
	CatalogVersion      string                `json:"catalogVersion"`
	CandidateSetHash    string                `json:"candidateSetHash"`
	Evidence            []FindingEvidenceLink `json:"evidence"`
}

// FindingReuseKey derives the 64-hex dedup key for one classification input.
// Links are canonicalized (sorted) before hashing, so link order never forks
// a key. The group and reason selections are outcomes, not inputs, and stay
// out of the key: resubmitting the same evidence with a different outcome
// conflicts instead of silently overwriting.
func FindingReuseKey(runID, opportunityID string, opportunityRevision, profileVersion int64,
	catalogVersion, candidateSetHash string, links []FindingEvidenceLink) (string, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(opportunityID) == "" ||
		opportunityRevision < 1 || profileVersion < 1 ||
		strings.TrimSpace(catalogVersion) == "" || len(candidateSetHash) != 64 || len(links) == 0 {
		return "", fmt.Errorf("%w: finding reuse input incomplete", ErrInvalid)
	}
	sorted := append([]FindingEvidenceLink(nil), links...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CaptureID != sorted[j].CaptureID {
			return sorted[i].CaptureID < sorted[j].CaptureID
		}
		if sorted[i].SpanStart != sorted[j].SpanStart {
			return sorted[i].SpanStart < sorted[j].SpanStart
		}
		return sorted[i].SpanEnd < sorted[j].SpanEnd
	})
	raw, err := json.Marshal(findingReuseInput{
		RunID: runID, OpportunityID: opportunityID, OpportunityRevision: opportunityRevision,
		ProfileVersion: profileVersion, CatalogVersion: catalogVersion,
		CandidateSetHash: candidateSetHash, Evidence: sorted,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validFindingID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 128
}

func validFindingSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validFindingGroup(group string) bool {
	return validEnum(group, FindingGroupRecommended, FindingGroupCouldBeRecommended,
		FindingGroupProbablyNotRecommended, FindingGroupNotRecommended, FindingGroupUnknown)
}

func validFindingReasonKind(kind string) bool {
	return validEnum(kind, FindingReasonPositive, FindingReasonNegative, FindingReasonMissingInformation)
}

// catalogReasonIndex maps every catalog reason id to its kind group.
func catalogReasonIndex(catalog ReasonCatalog) map[string]struct {
	kind   string
	choice ReasonChoice
} {
	out := map[string]struct {
		kind   string
		choice ReasonChoice
	}{}
	for _, c := range catalog.Positive {
		out[c.ID] = struct {
			kind   string
			choice ReasonChoice
		}{FindingReasonPositive, c}
	}
	for _, c := range catalog.Negative {
		out[c.ID] = struct {
			kind   string
			choice ReasonChoice
		}{FindingReasonNegative, c}
	}
	for _, c := range catalog.MissingInformation {
		out[c.ID] = struct {
			kind   string
			choice ReasonChoice
		}{FindingReasonMissingInformation, c}
	}
	return out
}

func validateFindingSaveInput(in FindingSaveInput) error {
	if !validFindingID(in.RunID) || !validFindingID(in.OpportunityID) || in.OpportunityRevision < 1 ||
		!validFindingID(in.AssessmentID) || !validFindingGroup(in.Group) {
		return fmt.Errorf("%w: run/opportunity/assessment/group binding required", ErrInvalid)
	}
	unknown := in.Group == FindingGroupUnknown
	if unknown != (strings.TrimSpace(in.UnknownBasis) != "" && len(in.UnknownBasis) <= FindingUnknownBasisMax) {
		return fmt.Errorf("%w: unknown carries an unknown_basis and only unknown does", ErrInvalid)
	}
	if unknown && (len(in.Reasons) != 0 || in.Conflict != nil || in.MissingFact != nil) {
		return fmt.Errorf("%w: unusable evidence supports no reason selections", ErrInvalid)
	}
	if len(in.Reasons) > FindingMaxReasons {
		return fmt.Errorf("%w: at most %d reason selections", ErrInvalid, FindingMaxReasons)
	}
	seen := map[string]bool{}
	for _, r := range in.Reasons {
		if !validFindingID(r.ReasonID) || !validFindingReasonKind(r.Kind) ||
			!boundedNonempty(r.Label, 200) || !boundedNonempty(r.Detail, 2000) ||
			math.IsNaN(r.JevSupport) || math.IsInf(r.JevSupport, 0) {
			return fmt.Errorf("%w: invalid reason selection %q", ErrInvalid, r.ReasonID)
		}
		if seen[r.ReasonID] {
			return fmt.Errorf("%w: duplicate reason selection %q", ErrInvalid, r.ReasonID)
		}
		seen[r.ReasonID] = true
	}
	for name, singleton := range map[string]*ReasonChoice{"conflict": in.Conflict, "missingFact": in.MissingFact} {
		if singleton == nil {
			continue
		}
		if !validFindingID(singleton.ID) || !boundedNonempty(singleton.Label, 200) || !boundedNonempty(singleton.Detail, 2000) {
			return fmt.Errorf("%w: invalid %s reason %q", ErrInvalid, name, singleton.ID)
		}
	}
	if len(in.EvidenceLinks) == 0 || len(in.EvidenceLinks) > FindingMaxEvidenceLinks {
		return fmt.Errorf("%w: classification needs 1..%d evidence links", ErrInvalid, FindingMaxEvidenceLinks)
	}
	seenLinks := map[FindingEvidenceLink]bool{}
	for _, link := range in.EvidenceLinks {
		if !validFindingID(link.CaptureID) || link.SpanStart < 0 || link.SpanEnd <= link.SpanStart ||
			!validFindingSHA256(link.ExcerptSHA256) {
			return fmt.Errorf("%w: invalid evidence link for capture %q", ErrInvalid, link.CaptureID)
		}
		key := FindingEvidenceLink{CaptureID: link.CaptureID, SpanStart: link.SpanStart, SpanEnd: link.SpanEnd}
		if seenLinks[key] {
			return fmt.Errorf("%w: duplicate evidence link for capture %q", ErrInvalid, link.CaptureID)
		}
		seenLinks[key] = true
	}
	if ref := in.SourceRef; ref != nil {
		if !boundedNonempty(ref.SourceID, FindingSourceRefMax) || !boundedNonempty(ref.SourceRevision, FindingSourceRefMax) ||
			len(ref.ObservedURL) > FindingObservedURLMax {
			return fmt.Errorf("%w: invalid finding source ref", ErrInvalid)
		}
	}
	return nil
}

const findingColumns = `id,actor_kind,actor_id,run_id,opportunity_id,opportunity_revision,
 assessment_id,profile_version,rubric_version,catalog_version,candidate_set_hash,reuse_key,
 group_name,unknown_basis,reasons_json,conflict_json,missing_fact_json,evidence_links_json,
 source_id,source_revision,observed_url,created_at`

func scanFinding(row rowScanner) (Finding, error) {
	var f Finding
	var reasonsJSON, evidenceJSON, sourceID, sourceRevision, observedURL string
	var conflictJSON, missingJSON sql.NullString
	err := row.Scan(&f.ID, &f.Actor.Kind, &f.Actor.ID, &f.RunID, &f.OpportunityID, &f.OpportunityRevision,
		&f.AssessmentID, &f.ProfileVersion, &f.RubricVersion, &f.CatalogVersion, &f.CandidateSetHash, &f.ReuseKey,
		&f.Group, &f.UnknownBasis, &reasonsJSON, &conflictJSON, &missingJSON, &evidenceJSON,
		&sourceID, &sourceRevision, &observedURL, &f.CreatedAt)
	if err != nil {
		return Finding{}, err
	}
	f.Reasons = []FindingReason{}
	if err := json.Unmarshal([]byte(reasonsJSON), &f.Reasons); err != nil {
		return Finding{}, err
	}
	if conflictJSON.Valid {
		var c ReasonChoice
		if err := json.Unmarshal([]byte(conflictJSON.String), &c); err != nil {
			return Finding{}, err
		}
		f.Conflict = &c
	}
	if missingJSON.Valid {
		var m ReasonChoice
		if err := json.Unmarshal([]byte(missingJSON.String), &m); err != nil {
			return Finding{}, err
		}
		f.MissingFact = &m
	}
	f.EvidenceLinks = []FindingEvidenceLink{}
	if err := json.Unmarshal([]byte(evidenceJSON), &f.EvidenceLinks); err != nil {
		return Finding{}, err
	}
	if sourceID != "" || sourceRevision != "" || observedURL != "" {
		f.SourceRef = &FindingSourceRef{SourceID: sourceID, SourceRevision: sourceRevision, ObservedURL: observedURL}
	}
	return f, nil
}

// findingMatches reports whether stored already holds exactly this outcome.
func findingMatches(stored Finding, group, unknownBasis, reasonsJSON string, conflict, missing *ReasonChoice,
	evidenceJSON, sourceID, sourceRevision, observedURL, assessmentID string, opportunityRevision int64) bool {
	if stored.Group != group || stored.UnknownBasis != unknownBasis ||
		stored.OpportunityRevision != opportunityRevision || stored.AssessmentID != assessmentID {
		return false
	}
	storedReasons, _ := json.Marshal(stored.Reasons)
	if string(storedReasons) != reasonsJSON {
		return false
	}
	storedEvidence, _ := json.Marshal(stored.EvidenceLinks)
	if string(storedEvidence) != evidenceJSON {
		return false
	}
	if (stored.Conflict == nil) != (conflict == nil) || (stored.MissingFact == nil) != (missing == nil) {
		return false
	}
	if conflict != nil && *stored.Conflict != *conflict {
		return false
	}
	if missing != nil && *stored.MissingFact != *missing {
		return false
	}
	storedSource := stored.SourceRef
	if storedSource == nil {
		storedSource = &FindingSourceRef{}
	}
	return storedSource.SourceID == sourceID && storedSource.SourceRevision == sourceRevision &&
		storedSource.ObservedURL == observedURL
}

// SaveFinding persists one Jev classification outcome. Versions bind from the
// cited dynamic assessment plus its reason catalog; every reason selection
// must repeat its catalog entry verbatim with the matching kind. An exact
// repeat (same reuse key, same outcome) replays the stored row; the same
// evidence with a different outcome conflicts. Saving never commissions
// deeper research and never starts a job check.
func (s *Store) SaveFinding(ctx context.Context, in FindingSaveInput) (Finding, error) {
	if err := validateFindingSaveInput(in); err != nil {
		return Finding{}, err
	}
	actor, err := ResearchRoundActor(ctx, s.db, in.RunID)
	if err != nil {
		return Finding{}, err
	}
	assessment, err := GetDynamicAssessment(ctx, s.db, in.AssessmentID)
	if err != nil {
		return Finding{}, err
	}
	if assessment.ActorKind != actor.Kind || assessment.ActorID != actor.ID {
		return Finding{}, ErrFenced
	}
	catalog, err := s.ReasonCatalog(ctx, assessment.ProfileVersion)
	if err != nil {
		return Finding{}, err
	}
	if assessment.RubricVersion != catalog.RubricVersion {
		return Finding{}, ErrConflict
	}
	index := catalogReasonIndex(catalog)
	reasons := make([]FindingReason, 0, len(in.Reasons))
	for _, r := range in.Reasons {
		entry, ok := index[r.ReasonID]
		if !ok || entry.kind != r.Kind || entry.choice.Label != r.Label || entry.choice.Detail != r.Detail {
			return Finding{}, fmt.Errorf("%w: reason %q is not a %s choice of %s",
				ErrInvalid, r.ReasonID, r.Kind, catalog.CatalogVersion)
		}
		reasons = append(reasons, FindingReason{ReasonID: r.ReasonID, Kind: r.Kind, Label: r.Label, Detail: r.Detail, JevSupport: r.JevSupport})
	}
	if in.Conflict != nil {
		entry, ok := index[in.Conflict.ID]
		if !ok || entry.choice.Label != in.Conflict.Label || entry.choice.Detail != in.Conflict.Detail {
			return Finding{}, fmt.Errorf("%w: conflict %q is not a choice of %s",
				ErrInvalid, in.Conflict.ID, catalog.CatalogVersion)
		}
	}
	if in.MissingFact != nil {
		entry, ok := index[in.MissingFact.ID]
		if !ok || entry.kind != FindingReasonMissingInformation ||
			entry.choice.Label != in.MissingFact.Label || entry.choice.Detail != in.MissingFact.Detail {
			return Finding{}, fmt.Errorf("%w: missing fact %q is not a missing-information choice of %s",
				ErrInvalid, in.MissingFact.ID, catalog.CatalogVersion)
		}
	}
	links := make([]FindingEvidenceLink, 0, len(in.EvidenceLinks))
	for _, link := range in.EvidenceLinks {
		if _, err := GetSourceCapture(ctx, s.db, link.CaptureID); err != nil {
			return Finding{}, fmt.Errorf("%w: unknown evidence capture %q", ErrInvalid, link.CaptureID)
		}
		links = append(links, FindingEvidenceLink{CaptureID: link.CaptureID,
			SpanStart: link.SpanStart, SpanEnd: link.SpanEnd, ExcerptSHA256: link.ExcerptSHA256})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].CaptureID != links[j].CaptureID {
			return links[i].CaptureID < links[j].CaptureID
		}
		if links[i].SpanStart != links[j].SpanStart {
			return links[i].SpanStart < links[j].SpanStart
		}
		return links[i].SpanEnd < links[j].SpanEnd
	})
	var liveRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		in.OpportunityID).Scan(&liveRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Finding{}, ErrNotFound
		}
		return Finding{}, err
	}
	if liveRevision != in.OpportunityRevision {
		return Finding{}, ErrConflict
	}
	reuseKey, err := FindingReuseKey(in.RunID, in.OpportunityID, in.OpportunityRevision,
		catalog.ProfileVersion, catalog.CatalogVersion, assessment.CandidateSetHash, links)
	if err != nil {
		return Finding{}, err
	}
	reasonsJSON, _ := json.Marshal(reasons)
	evidenceJSON, _ := json.Marshal(links)
	var sourceID, sourceRevision, observedURL string
	if in.SourceRef != nil {
		sourceID, sourceRevision, observedURL = in.SourceRef.SourceID, in.SourceRef.SourceRevision, in.SourceRef.ObservedURL
	}
	var conflictJSON, missingJSON any
	if in.Conflict != nil {
		raw, _ := json.Marshal(in.Conflict)
		conflictJSON = string(raw)
	}
	if in.MissingFact != nil {
		raw, _ := json.Marshal(in.MissingFact)
		missingJSON = string(raw)
	}
	var result Finding
	err = s.ResearchWrite(ctx, func(db ResearchDB) error {
		var current int64
		if err := db.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
			in.OpportunityID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current != in.OpportunityRevision {
			return ErrConflict
		}
		if existing, err := scanFinding(db.QueryRowContext(ctx, `SELECT `+findingColumns+
			` FROM findings WHERE actor_kind=? AND actor_id=? AND reuse_key=?`, actor.Kind, actor.ID, reuseKey)); err == nil {
			if existing.ProfileVersion != catalog.ProfileVersion || existing.CatalogVersion != catalog.CatalogVersion ||
				existing.CandidateSetHash != assessment.CandidateSetHash ||
				!findingMatches(existing, in.Group, in.UnknownBasis, string(reasonsJSON), in.Conflict, in.MissingFact,
					string(evidenceJSON), sourceID, sourceRevision, observedURL, in.AssessmentID, in.OpportunityRevision) {
				return ErrConflict
			}
			result = existing
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		now := utcNow()
		if _, err := db.ExecContext(ctx, `INSERT INTO findings
   (id,actor_kind,actor_id,run_id,opportunity_id,opportunity_revision,assessment_id,
    profile_version,rubric_version,catalog_version,candidate_set_hash,reuse_key,
    group_name,unknown_basis,reasons_json,conflict_json,missing_fact_json,evidence_links_json,
    source_id,source_revision,observed_url,created_at)
   VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, actor.Kind, actor.ID, in.RunID, in.OpportunityID, in.OpportunityRevision, in.AssessmentID,
			catalog.ProfileVersion, catalog.RubricVersion, catalog.CatalogVersion, assessment.CandidateSetHash, reuseKey,
			in.Group, in.UnknownBasis, string(reasonsJSON), conflictJSON, missingJSON, string(evidenceJSON),
			sourceID, sourceRevision, observedURL, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				if existing, rerr := scanFinding(db.QueryRowContext(ctx, `SELECT `+findingColumns+
					` FROM findings WHERE actor_kind=? AND actor_id=? AND reuse_key=?`, actor.Kind, actor.ID, reuseKey)); rerr == nil {
					if findingMatches(existing, in.Group, in.UnknownBasis, string(reasonsJSON), in.Conflict, in.MissingFact,
						string(evidenceJSON), sourceID, sourceRevision, observedURL, in.AssessmentID, in.OpportunityRevision) {
						result = existing
						return nil
					}
				}
				return ErrConflict
			}
			return err
		}
		stored, err := scanFinding(db.QueryRowContext(ctx, `SELECT `+findingColumns+` FROM findings WHERE id=?`, id))
		if err != nil {
			return err
		}
		result = stored
		return nil
	})
	if err != nil {
		return Finding{}, err
	}
	if serr := s.applyFindingStaleness(ctx, &result); serr != nil {
		return Finding{}, serr
	}
	return result, nil
}

// applyFindingStaleness flags version drift without mutating anything: a
// changed brief, a changed catalog under the same brief, or a revised
// opportunity each stale the saved classification. Pure read.
func (s *Store) applyFindingStaleness(ctx context.Context, f *Finding) error {
	var bases []string
	var currentProfile int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return err
	}
	if currentProfile != f.ProfileVersion {
		bases = append(bases, FindingStaleBriefChanged)
	} else if catalog, err := s.ReasonCatalog(ctx, f.ProfileVersion); err != nil || catalog.CatalogVersion != f.CatalogVersion {
		bases = append(bases, FindingStaleCatalogChanged)
	}
	var liveRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=?`, f.OpportunityID).Scan(&liveRevision); err == nil {
		if liveRevision != f.OpportunityRevision {
			bases = append(bases, FindingStaleOpportunityRevised)
		}
	}
	f.Stale = len(bases) > 0
	f.StaleBasis = strings.Join(bases, ",")
	return nil
}

// GetOpportunityFinding reads the latest saved classification for one role
// across runs. Pure read: it makes no model call and writes nothing.
func (s *Store) GetOpportunityFinding(ctx context.Context, opportunityID string) (Finding, error) {
	if !validFindingID(opportunityID) {
		return Finding{}, fmt.Errorf("%w: opportunity required", ErrInvalid)
	}
	f, err := scanFinding(s.db.QueryRowContext(ctx, `SELECT `+findingColumns+
		` FROM findings WHERE opportunity_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	if err := s.applyFindingStaleness(ctx, &f); err != nil {
		return Finding{}, err
	}
	return f, nil
}

// ListRunFindings reads one entry per collected vacancy in a run: the latest
// classification per opportunity, ordered by opportunity id. The group filter
// is exact; the cursor is the last seen opportunity id (exclusive). Pure read.
func (s *Store) ListRunFindings(ctx context.Context, runID, group, cursor string, limit int) (FindingListPage, error) {
	var page FindingListPage
	if !validFindingID(runID) {
		return page, fmt.Errorf("%w: run required", ErrInvalid)
	}
	if group != "" && !validFindingGroup(group) {
		return page, fmt.Errorf("%w: unknown finding group %q", ErrInvalid, group)
	}
	if limit < 0 {
		return page, fmt.Errorf("%w: finding list limit must be >= 0", ErrInvalid)
	}
	if limit == 0 {
		limit = FindingDefaultListLimit
	}
	if limit > FindingMaxListLimit {
		limit = FindingMaxListLimit
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM rounds WHERE id=?`, runID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return page, ErrNotFound
		}
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingColumns+` FROM (
  SELECT findings.*, ROW_NUMBER() OVER
   (PARTITION BY opportunity_id ORDER BY created_at DESC, id DESC) AS rn
  FROM findings WHERE run_id=?) WHERE rn=1 AND opportunity_id > ? AND (? = '' OR group_name = ?)
 ORDER BY opportunity_id ASC LIMIT ?`, runID, cursor, group, group, limit)
	if err != nil {
		return page, err
	}
	// Scan everything before computing staleness: the pool holds a single
	// connection, so a staleness query issued while rows are open deadlocks.
	items := []Finding{}
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			rows.Close()
			return FindingListPage{}, err
		}
		items = append(items, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FindingListPage{}, err
	}
	rows.Close()
	page.Items = []Finding{}
	for _, f := range items {
		if err := s.applyFindingStaleness(ctx, &f); err != nil {
			return FindingListPage{}, err
		}
		page.Items = append(page.Items, f)
	}
	if len(page.Items) == limit {
		page.NextCursor = page.Items[len(page.Items)-1].OpportunityID
	}
	return page, nil
}
