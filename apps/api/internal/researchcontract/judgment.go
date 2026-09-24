package researchcontract

import "context"

// EvidenceRef binds one cited span of an immutable capture.
type EvidenceRef struct {
	CaptureID string `json:"captureId"`
	SpanStart int64  `json:"spanStart"`
	SpanEnd   int64  `json:"spanEnd"`
}

// CandidateIdentity pins a record revision referenced by a judgment or save.
type CandidateIdentity struct {
	CandidateID string `json:"candidateId"`
	Kind        string `json:"kind"` // company|opportunity
	Revision    int64  `json:"revision"`
}

// AssessAlternative is one Codex-framed answer option with its evidence.
type AssessAlternative struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	EvidenceRefs []EvidenceRef `json:"evidenceRefs,omitempty"`
}

// AssessQuestion is one dynamically framed question. Abstention stays allowed.
type AssessQuestion struct {
	ID             string              `json:"id"`
	Text           string              `json:"text"`
	Alternatives   []AssessAlternative `json:"alternatives"`
	AbstainAllowed bool                `json:"abstainAllowed"`
}

// AssessInput scopes one dynamic Jev assessment to the active run.
type AssessInput struct {
	Purpose             string              `json:"purpose"`
	Questions           []AssessQuestion    `json:"questions"`
	ProfileVersion      int64               `json:"profileVersion"`
	RubricVersion       string              `json:"rubricVersion"`
	CandidateIdentities []CandidateIdentity `json:"candidateIdentities,omitempty"`
	SourceRefs          []EvidenceRef       `json:"sourceRefs"`
	IdempotencyKey      string              `json:"idempotencyKey"`
	RunID               string              `json:"runId"`
	Generation          int64               `json:"generation"`
}

// AssessAnswer is one validated classification. No invented rationale.
type AssessAnswer struct {
	QuestionID     string        `json:"questionId"`
	AnswerID       string        `json:"answerId,omitempty"` // empty when abstained
	Abstained      bool          `json:"abstained"`
	Uncertainty    string        `json:"uncertainty,omitempty"`
	SourceBindings []EvidenceRef `json:"sourceBindings,omitempty"`
}

// Assessment is the persisted judgment with full provenance.
type Assessment struct {
	ID           string         `json:"assessmentId"`
	Results      []AssessAnswer `json:"results"`
	Model        string         `json:"model"`
	ModelVersion string         `json:"modelVersion"`
	Usage        ExecuteUsage   `json:"usage"`
	ReuseKey     string         `json:"reuseKey"`
	EvidenceRefs []EvidenceRef  `json:"evidenceRefs"`
}

// Assessor answers Codex-framed questions from cited captures only.
// Implemented by lane D (T09); registered by lane B (T17).
type Assessor interface {
	Assess(ctx context.Context, in AssessInput) (Assessment, error)
}

// MatchAttributes carries captured role/employer signals for candidate retrieval.
type MatchAttributes struct {
	Employer       string   `json:"employer,omitempty"`
	Title          string   `json:"title,omitempty"`
	URL            string   `json:"url,omitempty"`
	RequisitionID  string   `json:"requisitionId,omitempty"`
	Location       string   `json:"location,omitempty"`
	Team           string   `json:"team,omitempty"`
	Dates          []string `json:"dates,omitempty"`
	ContentSignals []string `json:"contentSignals,omitempty"`
}

// ExactMatch is one established identity hit.
type ExactMatch struct {
	RecordID string `json:"recordId"`
	Kind     string `json:"kind"` // company|opportunity
}

// PossibleMatch is one broader candidate with distinguishing evidence.
type PossibleMatch struct {
	RecordID               string        `json:"recordId"`
	DistinguishingEvidence []EvidenceRef `json:"distinguishingEvidence,omitempty"`
}

// UnresolvedMatch keeps an ambiguous discovery in research memory.
type UnresolvedMatch struct {
	ObservationID string `json:"observationId"`
	NextQuestion  string `json:"nextQuestion"`
}

// MatchInput scopes one identity lookup to the active run.
type MatchInput struct {
	Attributes      MatchAttributes     `json:"attributes"`
	KnownIDs        []string            `json:"knownIds,omitempty"`
	ExtraCandidates []CandidateIdentity `json:"extraCandidates,omitempty"`
	EvidenceRefs    []EvidenceRef       `json:"evidenceRefs"`
	RunID           string              `json:"runId"`
	Generation      int64               `json:"generation"`
}

// MatchOutput is advice until commit: identity is rechecked inside the
// records_save transaction. Same title/text is never a unique key.
type MatchOutput struct {
	Outcome         Outcome          `json:"outcome"`
	ExactMatches    []ExactMatch     `json:"exactMatches,omitempty"`
	PossibleMatches []PossibleMatch  `json:"possibleMatches,omitempty"`
	Unresolved      *UnresolvedMatch `json:"unresolved,omitempty"`
}

// Matcher retrieves identity candidates. Implemented by lane D (T14).
type Matcher interface {
	Match(ctx context.Context, in MatchInput) (MatchOutput, error)
}

// IdentityDecision carries Codex's same/new/unresolved call with the
// candidate set it was made against. Candidates include kind + revision;
// the save transaction recomputes the set hash and rechecks revisions.
type IdentityDecision struct {
	Decision   string              `json:"decision"` // same|new|unresolved
	Candidates []CandidateIdentity `json:"candidates"`
}

// SaveOp is one bounded-batch record operation.
type SaveOp string

const (
	SaveCreateCompany     SaveOp = "create_company"
	SaveUpdateCompany     SaveOp = "update_company"
	SaveCreateOpportunity SaveOp = "create_opportunity"
	SaveUpdateOpportunity SaveOp = "update_opportunity"
)

// EvidenceLink binds one saved fact to an immutable capture span.
type EvidenceLink struct {
	CaptureID     string `json:"captureId"`
	SpanStart     int64  `json:"spanStart"`
	SpanEnd       int64  `json:"spanEnd"`
	ExcerptSHA256 string `json:"excerptSha256"`
}

// SaveItem is one batch element. Fields stay lane-D-typed; the contract
// fixes only the envelope, bindings and decision shape.
type SaveItem struct {
	Op               SaveOp            `json:"op"`
	RecordID         string            `json:"recordId,omitempty"`
	ExpectedRevision int64             `json:"expectedRevision,omitempty"`
	Fields           map[string]string `json:"fields"`
	EvidenceLinks    []EvidenceLink    `json:"evidenceLinks"`
	AssessmentIDs    []string          `json:"assessmentIds,omitempty"`
	IdentityDecision *IdentityDecision `json:"identityDecision,omitempty"`
}

// SaveBatch is one all-or-nothing batch with a stable logical request key.
type SaveBatch struct {
	Items          []SaveItem `json:"batch"`
	IdempotencyKey string     `json:"idempotencyKey"`
	RunID          string     `json:"runId"`
	Generation     int64      `json:"generation"`
}

// SavedRecord names one committed write.
type SavedRecord struct {
	RecordID string `json:"recordId"`
	Revision int64  `json:"revision"`
	AuditID  string `json:"auditId"`
}

// SaveItemError reports one item-specific conflict/invalid/uncertain detail.
type SaveItemError struct {
	Index           int     `json:"index"`
	Code            Outcome `json:"code"`
	Detail          string  `json:"detail"`
	CurrentRevision *int64  `json:"currentRevision,omitempty"`
}

// SaveOutput is the all-or-nothing result: saved IDs or item errors, never a
// partial business write.
type SaveOutput struct {
	Outcome   Outcome         `json:"outcome"`
	Saved     []SavedRecord   `json:"saved,omitempty"`
	ReusedIDs []string        `json:"reusedIds,omitempty"`
	Items     []SaveItemError `json:"items,omitempty"`
}

// RecordSaver commits bounded batches atomically. Implemented by lane D
// (T18). No network or Jev call runs under the write lock.
type RecordSaver interface {
	Save(ctx context.Context, batch SaveBatch) (SaveOutput, error)
}
