package musewire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ClassificationPurpose scopes every discovery assessment for audit.
const ClassificationPurpose = "muse_discovery_classification"

// maxClassifyExcerpt bounds the capture bytes cited per vacancy.
const maxClassifyExcerpt = 8192

// VacancyClassification scopes one saved vacancy to its run, brief and
// evidence for Jev judgment.
type VacancyClassification struct {
	RoundID        string
	ProfileVersion int64
	RubricVersion  string
	Generation     int64
	Actor          store.Actor
	Vacancy        musecode.PublicVacancy
}

// Classifier judges one saved vacancy against the brief's saved reason
// catalog and persists the finding. Production uses StoreClassifier;
// fixtures may substitute a fake.
type Classifier interface {
	ClassifyVacancy(ctx context.Context, in VacancyClassification) (store.Finding, error)
}

// BoundClassifier caps judged vacancies for one commission: the first Max
// vacancies reach the inner classifier, the rest fail closed as honest
// gaps so a separately billed Jev budget is never exceeded silently.
type BoundClassifier struct {
	Inner Classifier
	Max   int

	mu   sync.Mutex
	used int
}

func (c *BoundClassifier) ClassifyVacancy(ctx context.Context, in VacancyClassification) (store.Finding, error) {
	if c == nil || c.Inner == nil || c.Max <= 0 {
		return store.Finding{}, errors.New("musewire: bounded classifier needs an inner classifier and a positive bound")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used >= c.Max {
		return store.Finding{}, fmt.Errorf("musewire: Jev judgment bound of %d reached; vacancy %q left unclassified", c.Max, in.Vacancy.VacancyRef)
	}
	c.used++
	return c.Inner.ClassifyVacancy(ctx, in)
}

// Used reports judged vacancies so far.
func (c *BoundClassifier) Used() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// StoreClassifier binds saved vacancies to opportunities, assesses them
// through the wired Jev assessor against the saved catalog, and persists
// findings. It authors nothing: a missing catalog fails closed until the
// brief's catalog exists.
type StoreClassifier struct {
	DB       *store.Store
	Assessor researchcontract.Assessor
	Captures researchcontract.CaptureReader
}

// bucket binds one catalog reason list to its finding kind and question.
type bucket struct {
	questionID string
	kind       string
	label      string
	choices    []store.ReasonChoice
}

func catalogBuckets(catalog store.ReasonCatalog) []bucket {
	return []bucket{
		{questionID: "reason-positive", kind: store.FindingReasonPositive, label: "supporting", choices: catalog.Positive},
		{questionID: "reason-negative", kind: store.FindingReasonNegative, label: "opposing", choices: catalog.Negative},
		{questionID: "reason-missing", kind: store.FindingReasonMissingInformation, label: "missing-information", choices: catalog.MissingInformation},
	}
}

// ClassifyVacancy implements Classifier.
func (c StoreClassifier) ClassifyVacancy(ctx context.Context, in VacancyClassification) (store.Finding, error) {
	if c.DB == nil || c.Assessor == nil || c.Captures == nil {
		return store.Finding{}, errors.New("musewire: classifier missing a required dependency")
	}
	catalog, err := c.DB.ReasonCatalog(ctx, in.ProfileVersion)
	if err != nil {
		return store.Finding{}, fmt.Errorf("musewire: catalog for brief version %d unavailable: %w", in.ProfileVersion, err)
	}
	receipt, err := c.Captures.ResolveReceipt(ctx, in.Vacancy.ReceiptRef)
	if err != nil {
		return store.Finding{}, fmt.Errorf("musewire: resolve receipt: %w", err)
	}
	if receipt.Status != researchcontract.ReceiptOK && receipt.Status != researchcontract.ReceiptReused {
		return store.Finding{}, fmt.Errorf("musewire: receipt %q is %s, not usable evidence", in.Vacancy.ReceiptRef, receipt.Status)
	}
	excerpt, err := readExcerpt(ctx, c.Captures, receipt.CaptureID)
	if err != nil {
		return store.Finding{}, err
	}
	sum := sha256.Sum256(excerpt)
	excerptSHA := hex.EncodeToString(sum[:])
	employer := strings.TrimSpace(in.Vacancy.EmployerName)
	if employer == "" {
		return store.Finding{}, fmt.Errorf("musewire: vacancy %q has no employer name", in.Vacancy.VacancyRef)
	}
	// Lookup-first: a recollected vacancy reuses its existing identity
	// without creating a redundant company row. CreateOpportunity would
	// reconcile anyway; this only skips the orphan company insert.
	var opp store.Opportunity
	if existing, err := c.DB.OpportunityBySourceURL(ctx, strings.TrimSpace(in.Vacancy.PageURL)); err == nil {
		opp = existing
	} else {
		// One company per classified vacancy: the store has no employer dedup.
		// A later pass may merge companies without touching findings.
		company, _, err := c.DB.CreateCompany(ctx, in.Actor, store.CompanyInput{Name: employer})
		if err != nil {
			return store.Finding{}, fmt.Errorf("musewire: create company: %w", err)
		}
		opp, _, err = c.DB.CreateOpportunity(ctx, in.Actor, vacancyOpportunity(company.ID, in.Vacancy))
		if err != nil {
			return store.Finding{}, fmt.Errorf("musewire: create opportunity: %w", err)
		}
	}
	questions, index, err := frameQuestions(catalog)
	if err != nil {
		return store.Finding{}, err
	}
	assessment, err := c.Assessor.Assess(ctx, researchcontract.AssessInput{
		Purpose: ClassificationPurpose, Questions: questions,
		ProfileVersion: in.ProfileVersion, RubricVersion: in.RubricVersion,
		CandidateIdentities: []researchcontract.CandidateIdentity{
			{CandidateID: opp.ID, Kind: "opportunity", Revision: opp.Revision},
		},
		SourceRefs:     []researchcontract.EvidenceRef{{CaptureID: receipt.CaptureID, SpanStart: 0, SpanEnd: int64(len(excerpt))}},
		IdempotencyKey: in.RoundID + ":" + in.Vacancy.VacancyRef,
		RunID:          in.RoundID, Generation: in.Generation,
	})
	if err != nil {
		return store.Finding{}, fmt.Errorf("musewire: assess vacancy: %w", err)
	}
	group, basis, reasons := mapAnswers(assessment.Results, index)
	input := store.FindingSaveInput{
		RunID: in.RoundID, OpportunityID: opp.ID, OpportunityRevision: opp.Revision,
		AssessmentID: assessment.ID, Group: group, UnknownBasis: basis, Reasons: reasons,
		VacancyRef: in.Vacancy.VacancyRef,
		EvidenceLinks: []store.FindingEvidenceLinkInput{{
			CaptureID: receipt.CaptureID, SpanStart: 0, SpanEnd: int64(len(excerpt)), ExcerptSHA256: excerptSHA,
		}},
	}
	if sourceRef := vacancySourceRef(in.Vacancy); sourceRef != nil {
		input.SourceRef = sourceRef
	}
	finding, err := c.DB.SaveFinding(ctx, input)
	if err != nil {
		return store.Finding{}, fmt.Errorf("musewire: save finding: %w", err)
	}
	return finding, nil
}

func readExcerpt(ctx context.Context, captures researchcontract.CaptureReader, captureID string) ([]byte, error) {
	_, reader, err := captures.OpenCapture(ctx, captureID)
	if err != nil {
		return nil, fmt.Errorf("musewire: open capture: %w", err)
	}
	defer reader.Close()
	excerpt, err := io.ReadAll(io.LimitReader(reader, maxClassifyExcerpt+1))
	if err != nil {
		return nil, fmt.Errorf("musewire: read capture: %w", err)
	}
	if len(excerpt) == 0 {
		return nil, fmt.Errorf("musewire: capture %q is empty", captureID)
	}
	if len(excerpt) > maxClassifyExcerpt {
		excerpt = excerpt[:maxClassifyExcerpt]
	}
	return excerpt, nil
}

func vacancyOpportunity(companyID string, vacancy musecode.PublicVacancy) store.OpportunityInput {
	return store.OpportunityInput{
		CompanyID: companyID, Title: strings.TrimSpace(vacancy.Title), Kind: "employment",
		SourceURL: strings.TrimSpace(vacancy.PageURL), Stage: "found",
		LocationText: strings.TrimSpace(vacancy.LocationText),
		WorkPattern:  musecode.SanitizeWorkPattern(strings.TrimSpace(vacancy.WorkPattern)),
	}
}

func vacancySourceRef(vacancy musecode.PublicVacancy) *store.FindingSourceRef {
	if strings.TrimSpace(vacancy.SourceHost) == "" || strings.TrimSpace(vacancy.ReceiptRef) == "" {
		return nil
	}
	return &store.FindingSourceRef{
		SourceID: vacancy.SourceHost, SourceRevision: vacancy.ReceiptRef,
		ObservedURL: strings.TrimSpace(vacancy.PageURL),
	}
}

// frameQuestions asks one Choice question per non-empty catalog bucket with
// the catalog choices as alternatives. Jev selects at most one reason per
// bucket or abstains; the bridge maps the triple to a group.
func frameQuestions(catalog store.ReasonCatalog) ([]researchcontract.AssessQuestion, map[string]bucketChoice, error) {
	index := map[string]bucketChoice{}
	questions := []researchcontract.AssessQuestion{}
	for _, b := range catalogBuckets(catalog) {
		if len(b.choices) == 0 {
			continue
		}
		if len(b.choices) > 64 {
			return nil, nil, fmt.Errorf("musewire: %d %s choices exceed the assessment limit", len(b.choices), b.kind)
		}
		alternatives := make([]researchcontract.AssessAlternative, 0, len(b.choices))
		for _, choice := range b.choices {
			alternatives = append(alternatives, researchcontract.AssessAlternative{ID: choice.ID, Label: choice.Label})
			index[b.questionID+":"+choice.ID] = bucketChoice{kind: b.kind, choice: choice}
		}
		questions = append(questions, researchcontract.AssessQuestion{
			ID: b.questionID, AbstainAllowed: true,
			Text:         "Select the " + b.label + " catalog reason best supported by the cited vacancy capture, or abstain.",
			Alternatives: alternatives,
		})
	}
	if len(questions) == 0 {
		return nil, nil, errors.New("musewire: catalog has no reason choices to judge")
	}
	return questions, index, nil
}

type bucketChoice struct {
	kind   string
	choice store.ReasonChoice
}

// mapAnswers converts one answer per bucket into a finding group and
// verbatim reason selections. Supported negative reasons weigh against the
// role, missing information marks it checkable later, and positives carry
// it; total abstention is unusable evidence, never a default group.
//
// JevSupport stays 0: assessment answers select reasons without calibrated
// support, and the bridge never invents a confidence.
func mapAnswers(results []researchcontract.AssessAnswer, index map[string]bucketChoice) (group, basis string, reasons []store.FindingReasonInput) {
	byQuestion := map[string]researchcontract.AssessAnswer{}
	for _, answer := range results {
		byQuestion[answer.QuestionID] = answer
	}
	supported := map[string]store.FindingReasonInput{}
	for _, questionID := range []string{"reason-positive", "reason-negative", "reason-missing"} {
		answer, ok := byQuestion[questionID]
		if !ok || answer.Abstained || answer.AnswerID == "" {
			continue
		}
		entry, ok := index[questionID+":"+answer.AnswerID]
		if !ok {
			continue
		}
		supported[questionID] = store.FindingReasonInput{
			ReasonID: entry.choice.ID, Kind: entry.kind,
			Label: entry.choice.Label, Detail: entry.choice.Detail,
		}
	}
	_, pos := supported["reason-positive"]
	_, neg := supported["reason-negative"]
	_, miss := supported["reason-missing"]
	for _, questionID := range []string{"reason-positive", "reason-negative", "reason-missing"} {
		if reason, ok := supported[questionID]; ok {
			reasons = append(reasons, reason)
		}
	}
	switch {
	case neg && pos:
		return store.FindingGroupProbablyNotRecommended, "", reasons
	case neg:
		return store.FindingGroupNotRecommended, "", reasons
	case miss:
		return store.FindingGroupCouldBeRecommended, "", reasons
	case pos:
		return store.FindingGroupRecommended, "", reasons
	default:
		return store.FindingGroupUnknown, "no catalog reason supported by the cited capture", nil
	}
}
