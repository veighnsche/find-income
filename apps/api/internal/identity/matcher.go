// Package identity implements employer/vacancy identity matching and
// dynamic-assessment persistence (lane D, T14).
//
// Retrieval is broad (FTS + btree + strong keys/aliases per T06 §6) and
// never decides by itself: every exact match is a Jev same-verdict over
// cited evidence. Keys retrieve, Jev decides — reused identifiers (T04
// R02/R03) and same-title distinct roles (T04 D01/D02) are separated
// semantically, never by title similarity or URL equality alone.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	// PurposeIdentityMatch is the Jev purpose for semantic identity
	// comparisons. Identity stays brief-independent (no brief column by
	// design); the assessment still binds the asking brief as provenance.
	PurposeIdentityMatch = "identity_match"

	// AnswerSame marks an observed posting as the same employer/vacancy
	// as the compared candidate. AnswerDistinct rules the candidate out.
	AnswerSame     = "same"
	AnswerDistinct = "distinct"

	// MaxCompareCandidates bounds one Match call's semantic comparisons.
	// Uncompared candidates stay possible, never exact.
	MaxCompareCandidates = 12
	// MaxRetrievedCandidates bounds the broad candidate set.
	MaxRetrievedCandidates = 50
)

// Briefs reports the current brief versions for run-scoped assessment
// binding. The real reader binds at T14/T23; tests use doubles.
type Briefs interface {
	CurrentBrief(context.Context, string) (int64, string, error)
}

// Handler retrieves identity candidates and resolves them through
// evidence-based Jev comparisons. It implements researchcontract.Matcher.
type Handler struct {
	Authority researchcontract.Authority
	Store     *store.Store
	Briefs    Briefs
	Assessor  researchcontract.Assessor
}

var _ researchcontract.Matcher = (*Handler)(nil)

func (h *Handler) ready() error {
	if h == nil || h.Authority == nil || h.Store == nil || h.Briefs == nil || h.Assessor == nil {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "", "identity matcher missing a required dependency")
	}
	return nil
}

// candidate is one retrieved record plus how retrieval reached it.
type candidate struct {
	store.IdentityCandidate
	strong  bool // current strong key hit
	alias   bool // current alias hit
	history bool // superseded-row link only (retained reuse history)
	pinned  bool // owner/Codex-supplied known id or extra candidate
	sighted bool // already sighted for a cited capture
}

func candidateKey(kind, id string) string { return kind + "\x00" + id }

// Match implements researchcontract.Matcher: broad retrieval, then one Jev
// comparison round over cited evidence. Advice until commit: identity is
// rechecked inside the records_save transaction.
func (h *Handler) Match(ctx context.Context, in researchcontract.MatchInput) (researchcontract.MatchOutput, error) {
	if err := h.ready(); err != nil {
		return researchcontract.MatchOutput{}, err
	}
	if strings.TrimSpace(in.RunID) == "" {
		return researchcontract.MatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "runId", "runId is required")
	}
	if in.Generation < 1 {
		return researchcontract.MatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "generation", "generation must be >= 1")
	}
	for _, extra := range in.ExtraCandidates {
		if extra.CandidateID == "" || (extra.Kind != "company" && extra.Kind != "opportunity") || extra.Revision < 1 {
			return researchcontract.MatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"extraCandidates", "extra candidates need candidateId, kind company|opportunity and revision >= 1")
		}
	}
	for _, ref := range in.EvidenceRefs {
		if ref.CaptureID == "" || ref.SpanStart < 0 || ref.SpanEnd <= ref.SpanStart {
			return researchcontract.MatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"evidenceRefs", "evidence refs need a capture id and a positive span")
		}
	}
	if err := h.Authority.Check(ctx, researchcontract.CheckInput{
		RunID: in.RunID, Generation: in.Generation,
		Permission: researchcontract.PermissionResearchDispatch, Now: time.Now(),
	}); err != nil {
		return researchcontract.MatchOutput{}, err
	}
	cands, err := h.retrieve(ctx, in)
	if err != nil {
		return researchcontract.MatchOutput{}, err
	}
	compare, deferred := splitCompare(cands, len(in.EvidenceRefs) > 0)
	verdicts := map[string]researchcontract.AssessAnswer{}
	if len(compare) > 0 {
		verdicts, err = h.compare(ctx, in, compare)
		if err != nil {
			return researchcontract.MatchOutput{}, err
		}
	}
	return assemble(in, compare, deferred, verdicts), nil
}

// retrieve gathers the broad candidate set in one store read: strong keys
// and aliases (canonical URL, req ids across issuers, employer domain),
// same-capture sightings, FTS and btree hits, owner/Codex pins, plus
// retained reuse history.
func (h *Handler) retrieve(ctx context.Context, in researchcontract.MatchInput) ([]candidate, error) {
	byKey := map[string]*candidate{}
	order := []string{}
	add := func(c store.IdentityCandidate) *candidate {
		key := candidateKey(c.Kind, c.ID)
		if existing, ok := byKey[key]; ok {
			if c.Revision > existing.Revision {
				existing.Revision = c.Revision
			}
			if existing.Title == "" {
				existing.Title = c.Title
			}
			if existing.Location == "" {
				existing.Location = c.Location
			}
			return existing
		}
		out := &candidate{IdentityCandidate: c}
		byKey[key] = out
		order = append(order, key)
		return out
	}
	err := h.Store.Read(ctx, func(r store.Reader) error {
		attrs := in.Attributes
		if raw := strings.TrimSpace(attrs.URL); raw != "" {
			canonical, err := store.CanonicalIdentityURL(raw)
			if err == nil {
				rows, err := store.ListIdentityKeysByValue(ctx, r, store.IdentityNamespaceCanonicalURL, canonical)
				if err != nil {
					return err
				}
				if err := addKeyed(ctx, r, add, rows); err != nil {
					return err
				}
			}
			if host := store.IdentityURLHost(raw); host != "" {
				rows, err := store.ListIdentityKeysByValue(ctx, r, store.IdentityNamespaceEmployerDomain, host)
				if err != nil {
					return err
				}
				if err := addKeyed(ctx, r, add, rows); err != nil {
					return err
				}
			}
		}
		if strings.TrimSpace(attrs.RequisitionID) != "" {
			rows, err := store.ListReqIDKeys(ctx, r, attrs.RequisitionID)
			if err != nil {
				return err
			}
			if err := addKeyed(ctx, r, add, rows); err != nil {
				return err
			}
		}
		for _, ref := range in.EvidenceRefs {
			sightings, err := store.ListRecordSightingsByCapture(ctx, r, ref.CaptureID)
			if err != nil {
				return err
			}
			for _, sighting := range sightings {
				kind, id := sightingTarget(sighting)
				if kind == "" {
					continue
				}
				hit, err := store.GetIdentityCandidate(ctx, r, kind, id)
				if err != nil {
					if errors.Is(err, store.ErrNotFound) {
						continue
					}
					return err
				}
				add(hit).sighted = true
			}
		}
		if strings.TrimSpace(attrs.Title) != "" || strings.TrimSpace(attrs.Location) != "" {
			hits, err := store.SearchOpportunityCandidatesFTS(ctx, r, attrs.Title, attrs.Location, 20)
			if err != nil {
				return err
			}
			for _, hit := range hits {
				add(hit)
			}
		}
		if strings.TrimSpace(attrs.Title) != "" {
			hits, err := store.FindOpportunitiesByTitle(ctx, r, attrs.Title, 10)
			if err != nil {
				return err
			}
			for _, hit := range hits {
				add(hit)
			}
		}
		if strings.TrimSpace(attrs.Employer) != "" {
			hits, err := store.FindCompaniesByName(ctx, r, attrs.Employer, 10)
			if err != nil {
				return err
			}
			for _, hit := range hits {
				add(hit)
			}
		}
		for _, id := range in.KnownIDs {
			if strings.TrimSpace(id) == "" {
				continue
			}
			for _, kind := range []string{"opportunity", "company"} {
				hit, err := store.GetIdentityCandidate(ctx, r, kind, id)
				if err == nil {
					add(hit).pinned = true
				} else if !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
		}
		for _, extra := range in.ExtraCandidates {
			hit, err := store.GetIdentityCandidate(ctx, r, extra.Kind, extra.CandidateID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return researchcontract.NewError(researchcontract.OutcomeInvalid,
						"extraCandidates", "pinned candidate "+extra.CandidateID+" is unknown; refresh the pin")
				}
				return err
			}
			add(hit).pinned = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]candidate, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return candidateRank(out[i]) < candidateRank(out[j])
	})
	if len(out) > MaxRetrievedCandidates {
		out = out[:MaxRetrievedCandidates]
	}
	return out, nil
}

// addKeyed resolves key rows to live candidates and flags how each row
// links: current strong, current alias, or superseded history. History-only
// records (reused identifiers, R02/R03) surface for visibility but never
// compare and never go exact.
func addKeyed(ctx context.Context, r store.Reader,
	add func(store.IdentityCandidate) *candidate, rows []store.EntityIdentityKey) error {
	for _, row := range rows {
		kind, id := keyTarget(row)
		if kind == "" {
			continue
		}
		hit, err := store.GetIdentityCandidate(ctx, r, kind, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		entry := add(hit)
		switch {
		case row.Status == store.IdentityKeyCurrent && row.Strength == store.IdentityKeyStrong:
			entry.strong = true
		case row.Status == store.IdentityKeyCurrent:
			entry.alias = true
		default:
			entry.history = true
		}
	}
	return nil
}

func keyTarget(row store.EntityIdentityKey) (kind, id string) {
	if row.CompanyID != "" {
		return "company", row.CompanyID
	}
	if row.OpportunityID != "" {
		return "opportunity", row.OpportunityID
	}
	return "", ""
}

// candidateRank orders retrieval: strong keys first, retained history last.
func candidateRank(c candidate) int {
	switch {
	case c.strong:
		return 0
	case c.pinned || c.sighted:
		return 1
	case c.alias:
		return 2
	case c.history && !c.strong && !c.alias:
		return 4
	default:
		return 3
	}
}

func sightingTarget(sighting store.RecordSighting) (kind, id string) {
	if sighting.CompanyID != "" {
		return "company", sighting.CompanyID
	}
	if sighting.OpportunityID != "" {
		return "opportunity", sighting.OpportunityID
	}
	return "", ""
}

func historyOnly(c candidate) bool { return c.history && !c.strong && !c.alias }

// splitCompare selects the Jev comparison set: non-history candidates up
// to the cap, only when cited evidence grounds the comparison. Everything
// else defers to possible.
func splitCompare(cands []candidate, haveEvidence bool) (compare, deferred []candidate) {
	for _, c := range cands {
		if historyOnly(c) || !haveEvidence || len(compare) >= MaxCompareCandidates {
			deferred = append(deferred, c)
			continue
		}
		compare = append(compare, c)
	}
	return compare, deferred
}

// compare frames one semantic question per candidate and runs a single Jev
// assessment over the cited evidence. All refs travel in SourceRefs; the
// verdicts map question ids back to candidates.
func (h *Handler) compare(ctx context.Context, in researchcontract.MatchInput, compare []candidate) (map[string]researchcontract.AssessAnswer, error) {
	profile, rubric, err := h.Briefs.CurrentBrief(ctx, in.RunID)
	if err != nil {
		return nil, err
	}
	questions := make([]researchcontract.AssessQuestion, 0, len(compare))
	identities := make([]researchcontract.CandidateIdentity, 0, len(compare))
	for _, c := range compare {
		questions = append(questions, researchcontract.AssessQuestion{
			ID:   compareQuestionID(c.IdentityCandidate),
			Text: compareQuestionText(in.Attributes, c.IdentityCandidate),
			Alternatives: []researchcontract.AssessAlternative{
				{ID: AnswerSame, Label: "The observed posting and " + candidateLabel(c.IdentityCandidate) + " describe the same employer/vacancy."},
				{ID: AnswerDistinct, Label: "They describe different employers/vacancies."},
			},
			AbstainAllowed: true,
		})
		identities = append(identities, researchcontract.CandidateIdentity{
			CandidateID: c.ID, Kind: c.Kind, Revision: c.Revision,
		})
	}
	assessment, err := h.Assessor.Assess(ctx, researchcontract.AssessInput{
		Purpose:             PurposeIdentityMatch,
		Questions:           questions,
		ProfileVersion:      profile,
		RubricVersion:       rubric,
		CandidateIdentities: identities,
		SourceRefs:          append([]researchcontract.EvidenceRef(nil), in.EvidenceRefs...),
		IdempotencyKey:      compareIdempotencyKey(in.RunID, profile, rubric, questions, in.EvidenceRefs, identities),
		RunID:               in.RunID,
		Generation:          in.Generation,
	})
	if err != nil {
		return nil, err
	}
	byQuestion := map[string]researchcontract.AssessAnswer{}
	for _, answer := range assessment.Results {
		byQuestion[answer.QuestionID] = answer
	}
	verdicts := map[string]researchcontract.AssessAnswer{}
	for _, c := range compare {
		answer, ok := byQuestion[compareQuestionID(c.IdentityCandidate)]
		if !ok {
			return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"", "assessor dropped the comparison for candidate "+c.ID)
		}
		if !answer.Abstained && answer.AnswerID != AnswerSame && answer.AnswerID != AnswerDistinct {
			return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"", "assessor returned an unknown verdict for candidate "+c.ID)
		}
		verdicts[candidateKey(c.Kind, c.ID)] = answer
	}
	return verdicts, nil
}

func compareQuestionID(c store.IdentityCandidate) string {
	return "cmp-" + c.Kind + "-" + c.ID
}

func candidateLabel(c store.IdentityCandidate) string {
	summary := c.Kind + " " + c.ID
	if c.Title != "" {
		summary += " (" + c.Title
		if c.Location != "" {
			summary += ", " + c.Location
		}
		summary += ")"
	}
	return summary
}

func compareQuestionText(attrs researchcontract.MatchAttributes, c store.IdentityCandidate) string {
	var observed []string
	if attrs.Employer != "" {
		observed = append(observed, "employer "+quoteAttr(attrs.Employer))
	}
	if attrs.Title != "" {
		observed = append(observed, "title "+quoteAttr(attrs.Title))
	}
	if attrs.URL != "" {
		observed = append(observed, "url "+quoteAttr(attrs.URL))
	}
	if attrs.RequisitionID != "" {
		observed = append(observed, "req "+quoteAttr(attrs.RequisitionID))
	}
	if attrs.Location != "" {
		observed = append(observed, "location "+quoteAttr(attrs.Location))
	}
	if attrs.Team != "" {
		observed = append(observed, "team "+quoteAttr(attrs.Team))
	}
	detail := "the observed posting"
	if len(observed) > 0 {
		detail += " (" + strings.Join(observed, "; ") + ")"
	}
	return "Is " + detail + " the same employer/vacancy as " + candidateLabel(c) +
		"? Answer same only when the cited evidence shows one employer/vacancy behind both; " +
		"answer distinct when the evidence shows different employers/vacancies " +
		"(reused identifiers and overlapping titles alone never prove sameness); " +
		"abstain when the evidence does not decide."
}

func quoteAttr(value string) string {
	if len(value) > 120 {
		value = value[:120] + "…"
	}
	return `"` + strings.ReplaceAll(value, `"`, "'") + `"`
}

// compareIdempotencyKey derives a stable key for one comparison round so an
// identical Match replays its reservation instead of dispatching again.
func compareIdempotencyKey(runID string, profile int64, rubric string,
	questions []researchcontract.AssessQuestion, refs []researchcontract.EvidenceRef,
	identities []researchcontract.CandidateIdentity) string {
	raw, _ := json.Marshal(struct {
		RunID      string                               `json:"run_id"`
		Profile    int64                                `json:"profile_version"`
		Rubric     string                               `json:"rubric_version"`
		Questions  []researchcontract.AssessQuestion    `json:"questions"`
		Refs       []researchcontract.EvidenceRef       `json:"refs"`
		Identities []researchcontract.CandidateIdentity `json:"identities"`
	}{runID, profile, rubric, questions, refs, identities})
	sum := sha256.Sum256(raw)
	return "identity-match-" + hex.EncodeToString(sum[:])
}

// assemble maps compared verdicts and deferred candidates onto the contract
// output. Exact requires a Jev same-verdict; abstentions, uncompared
// candidates and retained history stay possible; an empty set with thin
// input stays unresolved instead of forcing a call.
func assemble(in researchcontract.MatchInput, compare, deferred []candidate,
	verdicts map[string]researchcontract.AssessAnswer) researchcontract.MatchOutput {
	var out researchcontract.MatchOutput
	distinguishing := append([]researchcontract.EvidenceRef(nil), in.EvidenceRefs...)
	for _, c := range compare {
		answer := verdicts[candidateKey(c.Kind, c.ID)]
		switch {
		case answer.Abstained:
			out.PossibleMatches = append(out.PossibleMatches, researchcontract.PossibleMatch{
				RecordID: c.ID, DistinguishingEvidence: distinguishing,
			})
		case answer.AnswerID == AnswerSame:
			out.ExactMatches = append(out.ExactMatches, researchcontract.ExactMatch{
				RecordID: c.ID, Kind: c.Kind,
			})
		default: // distinct: separated, never merged or surfaced as possible
		}
	}
	for _, c := range deferred {
		out.PossibleMatches = append(out.PossibleMatches, researchcontract.PossibleMatch{
			RecordID: c.ID, DistinguishingEvidence: distinguishing,
		})
	}
	switch {
	case len(out.ExactMatches) > 0:
		out.Outcome = researchcontract.OutcomeOK
	case len(out.PossibleMatches) > 0:
		out.Outcome = researchcontract.OutcomeIdentityAmbiguous
		out.Unresolved = &researchcontract.UnresolvedMatch{NextQuestion: fmt.Sprintf(
			"%d candidate(s) need distinguishing evidence (req ID, board record, canonical URL); cite the deciding spans and re-run match before saving.",
			len(out.PossibleMatches))}
	default:
		out.Outcome = researchcontract.OutcomeOK
		if len(in.EvidenceRefs) == 0 && strings.TrimSpace(in.Attributes.URL) == "" &&
			strings.TrimSpace(in.Attributes.RequisitionID) == "" {
			out.Unresolved = &researchcontract.UnresolvedMatch{NextQuestion: "No candidates and no citable evidence; cite the posting capture (employer, title, URL, req ID) and re-run match before deciding new."}
		}
	}
	return out
}
