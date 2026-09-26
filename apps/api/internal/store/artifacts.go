package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Route-dependent application artifacts (A4). Each produced item a route
// calls for persists as its own versioned record with content, basis and
// exact-edit fencing, independent of the pack bundle versions. Form
// values are the exception: they derive from current question answers
// at read time and are never stored, so the store rejects that type.
const (
	ArtifactCV           = "cv"
	ArtifactCoverLetter  = "cover_letter"
	ArtifactEmailSubject = "email_subject"
	ArtifactEmailBody    = "email_body"
	ArtifactFormValues   = "form_values"
)

const (
	artifactMaxContent = 65536
	artifactMaxBasis   = 8192
	artifactMaxRefs    = 200
)

func validRequestKey(key string) bool {
	return strings.TrimSpace(key) == key && key != "" && len(key) <= 200
}

func validArtifactType(value string) bool {
	switch value {
	case ArtifactCV, ArtifactCoverLetter, ArtifactEmailSubject, ArtifactEmailBody:
		return true
	default:
		return false
	}
}

// ArtifactAnswerRef pins one owner answer version behind artifact content.
type ArtifactAnswerRef struct {
	QuestionID    string `json:"questionId"`
	AnswerVersion int64  `json:"answerVersion"`
}

// ArtifactBasis records what an artifact was built from: verified owner
// fact ids, pinned answer versions and check source spans, plus the
// consumed input pins (M2): the check, question set, opportunity
// revision and fact digests the writer verified before committing.
// Writers own the truth of the basis; the store only enforces shape
// and bounds. Pin fields are omitempty so exact edits may cite only
// what they verified; older rows read back with zero pins.
type ArtifactBasis struct {
	FactIDs    []string            `json:"factIds"`
	AnswerRefs []ArtifactAnswerRef `json:"answerRefs"`
	CheckSpans []CheckSourceSpan   `json:"checkSpans"`
	// CheckID and QuestionSetSHA256 pin the verified check whose
	// route, documents and questions the content was built from.
	CheckID           string `json:"checkId,omitempty"`
	QuestionSetSHA256 string `json:"questionSetSha256,omitempty"`
	// OpportunityRevision pins the vacancy revision the check read.
	OpportunityRevision int64 `json:"opportunityRevision,omitempty"`
	// FactSHA256 pins each cited fact id to the approved source
	// digest the writer consumed (id -> hex sha256).
	FactSHA256 map[string]string `json:"factSha256,omitempty"`
}

// ArtifactView is one immutable artifact version.
type ArtifactView struct {
	ID            string        `json:"id"`
	OpportunityID string        `json:"opportunityId"`
	Type          string        `json:"type"`
	Version       int64         `json:"version"`
	Content       string        `json:"content"`
	Basis         ArtifactBasis `json:"basis"`
	CreatedAt     string        `json:"createdAt"`
	CreatedBy     Actor         `json:"createdBy"`
}

// ArtifactSaveInput writes one artifact version. ExpectedVersion 0
// creates the first version; otherwise the write fences on the current
// version. RequestKey replays the same write.
type ArtifactSaveInput struct {
	RequestKey      string        `json:"requestKey"`
	ExpectedVersion int64         `json:"expectedVersion"`
	Type            string        `json:"type"`
	Content         string        `json:"content"`
	Basis           ArtifactBasis `json:"basis"`
}

func validArtifactBasis(basis ArtifactBasis) bool {
	if len(basis.FactIDs) > artifactMaxRefs || len(basis.AnswerRefs) > artifactMaxRefs ||
		len(basis.CheckSpans) > artifactMaxRefs || len(basis.FactSHA256) > artifactMaxRefs {
		return false
	}
	if len(basis.CheckID) > 128 || len(basis.QuestionSetSHA256) > 128 || basis.OpportunityRevision < 0 {
		return false
	}
	for id, digest := range basis.FactSHA256 {
		if strings.TrimSpace(id) == "" || len(id) > 128 || len(digest) > 128 {
			return false
		}
	}
	for _, id := range basis.FactIDs {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return false
		}
	}
	for _, ref := range basis.AnswerRefs {
		if strings.TrimSpace(ref.QuestionID) == "" || len(ref.QuestionID) > 64 || ref.AnswerVersion < 0 {
			return false
		}
	}
	for _, span := range basis.CheckSpans {
		if !validCheckSpan(span) {
			return false
		}
	}
	raw, err := json.Marshal(basis)
	return err == nil && len(raw) <= artifactMaxBasis
}

// canonicalArtifactBasis normalizes one basis so payload digests are
// stable: nil and empty collections encode identically.
func canonicalArtifactBasis(basis ArtifactBasis) ArtifactBasis {
	if basis.FactIDs == nil {
		basis.FactIDs = []string{}
	}
	if basis.AnswerRefs == nil {
		basis.AnswerRefs = []ArtifactAnswerRef{}
	}
	if basis.CheckSpans == nil {
		basis.CheckSpans = []CheckSourceSpan{}
	}
	if basis.FactSHA256 == nil {
		basis.FactSHA256 = map[string]string{}
	}
	return basis
}

// artifactPayloadDigest binds one request key to its exact payload:
// type, content and full basis pins. Identical retries replay the
// accepted version; a changed payload under a reused key conflicts
// (R23) instead of replaying success.
func artifactPayloadDigest(artifactType, content string, basis ArtifactBasis) string {
	raw, _ := json.Marshal(struct {
		Type    string
		Content string
		Basis   ArtifactBasis
	}{artifactType, content, canonicalArtifactBasis(basis)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SaveOpportunityArtifact writes one artifact version for a selected
// role. Retried request keys replay the accepted version only when
// the payload is identical; a changed payload under a reused key
// conflicts (R23), and a fenced write against a moved version
// conflicts instead of silently forking.
func (s *Store) SaveOpportunityArtifact(ctx context.Context, actor Actor, opportunityID string, input ArtifactSaveInput) (ArtifactView, bool, error) {
	if opportunityID == "" || !validRequestKey(input.RequestKey) || input.ExpectedVersion < 0 {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact save", ErrInvalid)
	}
	if !validArtifactType(input.Type) {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact type", ErrInvalid)
	}
	if strings.TrimSpace(input.Content) == "" || len([]rune(input.Content)) > artifactMaxContent {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact content", ErrInvalid)
	}
	if !validArtifactBasis(input.Basis) {
		return ArtifactView{}, false, fmt.Errorf("%w: invalid artifact basis", ErrInvalid)
	}
	var out ArtifactView
	created := false
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactView{}, false, err
	}
	defer tx.Rollback()
	err = func(tx *sql.Tx) error {
		if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
			return err
		}
		if replay, ok, err := scanArtifact(tx.QueryRowContext(ctx, `SELECT `+artifactColumns+
			` FROM opportunity_artifacts WHERE opportunity_id=? AND artifact_type=? AND request_key=?`,
			opportunityID, input.Type, input.RequestKey)); err == nil && ok {
			if artifactPayloadDigest(replay.Type, replay.Content, replay.Basis) !=
				artifactPayloadDigest(input.Type, input.Content, input.Basis) {
				return ErrConflict
			}
			out = replay
			return nil
		} else if err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM opportunity_artifacts
			WHERE opportunity_id=? AND artifact_type=?`, opportunityID, input.Type).Scan(&current); err != nil {
			return err
		}
		if current != input.ExpectedVersion {
			return ErrConflict
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		now := utcNow()
		basisJSON, _ := json.Marshal(input.Basis)
		if _, err := tx.ExecContext(ctx, `INSERT INTO opportunity_artifacts
			(id,opportunity_id,artifact_type,version,request_key,content,basis_json,actor_kind,actor_id,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, input.Type, current+1, input.RequestKey,
			input.Content, string(basisJSON), actor.Kind, actor.ID, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrConflict
			}
			return err
		}
		out = ArtifactView{ID: id, OpportunityID: opportunityID, Type: input.Type,
			Version: current + 1, Content: input.Content, Basis: input.Basis,
			CreatedAt: now, CreatedBy: actor}
		created = true
		return nil
	}(tx)
	if err != nil {
		return ArtifactView{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactView{}, false, err
	}
	return out, created, nil
}

const artifactColumns = `id,opportunity_id,artifact_type,version,request_key,content,basis_json,actor_kind,actor_id,created_at`

func scanArtifact(row rowScanner) (ArtifactView, bool, error) {
	var view ArtifactView
	var requestKey, basisJSON, actorKind, actorID string
	if err := row.Scan(&view.ID, &view.OpportunityID, &view.Type, &view.Version,
		&requestKey, &view.Content, &basisJSON, &actorKind, &actorID, &view.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactView{}, false, nil
		}
		return ArtifactView{}, false, err
	}
	view.CreatedBy = Actor{Kind: actorKind, ID: actorID}
	if basisJSON == "" {
		basisJSON = "{}"
	}
	if err := json.Unmarshal([]byte(basisJSON), &view.Basis); err != nil {
		return ArtifactView{}, false, err
	}
	if view.Basis.FactIDs == nil {
		view.Basis.FactIDs = []string{}
	}
	if view.Basis.AnswerRefs == nil {
		view.Basis.AnswerRefs = []ArtifactAnswerRef{}
	}
	if view.Basis.CheckSpans == nil {
		view.Basis.CheckSpans = []CheckSourceSpan{}
	}
	return view, true, nil
}

// GetOpportunityArtifactByRequestKey reads the version one request key
// accepted, for lost-response recovery before another operation.
// Absent keys read as ErrNotFound.
func (s *Store) GetOpportunityArtifactByRequestKey(ctx context.Context, opportunityID, artifactType, requestKey string) (ArtifactView, error) {
	view, ok, err := scanArtifact(s.db.QueryRowContext(ctx, `SELECT `+artifactColumns+
		` FROM opportunity_artifacts WHERE opportunity_id=? AND artifact_type=? AND request_key=?`,
		opportunityID, artifactType, requestKey))
	if err != nil {
		return ArtifactView{}, err
	}
	if !ok {
		return ArtifactView{}, ErrNotFound
	}
	return view, nil
}

// GetOpportunityArtifact reads the current version of one artifact type.
// Absent artifacts read as ErrNotFound: the set read maps those to held
// or not-required states instead of fabricating content.
func (s *Store) GetOpportunityArtifact(ctx context.Context, opportunityID, artifactType string) (ArtifactView, error) {
	view, ok, err := scanArtifact(s.db.QueryRowContext(ctx, `SELECT `+artifactColumns+
		` FROM opportunity_artifacts WHERE opportunity_id=? AND artifact_type=?
		ORDER BY version DESC LIMIT 1`, opportunityID, artifactType))
	if err != nil {
		return ArtifactView{}, err
	}
	if !ok {
		return ArtifactView{}, ErrNotFound
	}
	return view, nil
}

// ListOpportunityArtifacts reads the current version of every stored
// artifact type behind one opportunity.
func (s *Store) ListOpportunityArtifacts(ctx context.Context, opportunityID string) ([]ArtifactView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+artifactColumns+` FROM opportunity_artifacts
		WHERE opportunity_id=? AND version=(SELECT MAX(version) FROM opportunity_artifacts inner_rows
		WHERE inner_rows.opportunity_id=opportunity_artifacts.opportunity_id
		AND inner_rows.artifact_type=opportunity_artifacts.artifact_type) ORDER BY artifact_type`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactView{}
	for rows.Next() {
		view, ok, err := scanArtifact(rows)
		if err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

// ListArtifactVersions reads every stored version of one artifact
// type, oldest first, so previous content stays inspectable after
// exact edits and rewrites.
func (s *Store) ListArtifactVersions(ctx context.Context, opportunityID, artifactType string) ([]ArtifactView, error) {
	if opportunityID == "" || artifactType == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+artifactColumns+` FROM opportunity_artifacts
		WHERE opportunity_id=? AND artifact_type=? ORDER BY version`, opportunityID, artifactType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactView{}
	for rows.Next() {
		view, ok, err := scanArtifact(rows)
		if err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

// SavedJobItem is one index row: an artifact type with its current
// version and readiness state.
type SavedJobItem struct {
	Type      string `json:"type"`
	Required  bool   `json:"required"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// SavedJobEntry is one saved job in the durable artifact index:
// the role, its check status and every required or stored item.
type SavedJobEntry struct {
	OpportunityID string         `json:"opportunityId"`
	Title         string         `json:"title"`
	CompanyName   string         `json:"companyName"`
	CheckStatus   string         `json:"checkStatus"`
	Items         []SavedJobItem `json:"items"`
}

// ListSavedJobs reads the durable saved-job/artifact index: every
// non-archived role with stored work, most recently touched first.
// Partial and stale sets stay listed with their honest states;
// roles whose checks are unavailable keep their stored items under
// an unresolved status instead of vanishing.
func (s *Store) ListSavedJobs(ctx context.Context) ([]SavedJobEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.title, c.name
		FROM opportunity_artifacts a
		JOIN opportunities o ON o.id=a.opportunity_id
		JOIN companies c ON c.id=o.company_id
		WHERE o.archived_at IS NULL
		GROUP BY o.id ORDER BY MAX(a.created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type job struct{ id, title, company string }
	jobs := []job{}
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.title, &j.company); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SavedJobEntry, 0, len(jobs))
	for _, j := range jobs {
		entry := SavedJobEntry{OpportunityID: j.id, Title: j.title,
			CompanyName: j.company, Items: []SavedJobItem{}}
		set, err := s.ArtifactReadiness(ctx, j.id)
		if err != nil {
			entry.CheckStatus = "unknown"
			stored, listErr := s.ListOpportunityArtifacts(ctx, j.id)
			if listErr != nil {
				return nil, listErr
			}
			for _, view := range stored {
				entry.Items = append(entry.Items, SavedJobItem{Type: view.Type,
					State: ArtifactStateUnresolved, Reason: "saved work kept; role state unavailable",
					Version: view.Version, UpdatedAt: view.CreatedAt})
			}
			out = append(out, entry)
			continue
		}
		entry.CheckStatus = set.CheckStatus
		for _, readiness := range set.Entries {
			if !readiness.Required && readiness.Current == nil {
				continue
			}
			item := SavedJobItem{Type: readiness.Type, Required: readiness.Required,
				State: readiness.State, Reason: readiness.Reason}
			if readiness.Current != nil {
				item.Version = readiness.Current.Version
				item.UpdatedAt = readiness.Current.CreatedAt
			}
			entry.Items = append(entry.Items, item)
		}
		out = append(out, entry)
	}
	return out, nil
}

// HandoffUpload maps one attachment question to the file the owner
// should upload: the matched stored artifact with its state, or an
// unmatched question the owner must resolve by hand.
type HandoffUpload struct {
	QuestionID    string `json:"questionId"`
	QuestionText  string `json:"questionText"`
	Required      string `json:"required"`
	ArtifactType  string `json:"artifactType,omitempty"`
	State         string `json:"state"`
	Version       int64  `json:"version,omitempty"`
	ContentSHA256 string `json:"contentSha256,omitempty"`
}

// HandoffItem is one take-out-able artifact: the current version
// with its state, content and checksum, or a held type with its
// reason and no content to mistake for ready work.
type HandoffItem struct {
	Type          string              `json:"type"`
	Required      bool                `json:"required"`
	State         string              `json:"state"`
	Reason        string              `json:"reason"`
	Basis         string              `json:"basis,omitempty"`
	Version       int64               `json:"version,omitempty"`
	Content       string              `json:"content,omitempty"`
	ContentSHA256 string              `json:"contentSha256,omitempty"`
	FormValues    []ArtifactFormValue `json:"formValues,omitempty"`
}

// HandoffView is the manual Handoff basis for one role: the verified
// destination, every required or stored item with its take-out
// content, and the upload mapping. It never claims submission;
// opening or copying it changes nothing.
type HandoffView struct {
	OpportunityID    string          `json:"opportunityId"`
	Title            string          `json:"title"`
	CompanyName      string          `json:"companyName"`
	CheckID          string          `json:"checkId,omitempty"`
	CheckStatus      string          `json:"checkStatus"`
	WorkflowStage    string          `json:"workflowStage"`
	RouteKind        string          `json:"routeKind,omitempty"`
	RouteDestination string          `json:"routeDestination,omitempty"`
	RouteExcerpt     string          `json:"routeExcerpt,omitempty"`
	Items            []HandoffItem   `json:"items"`
	Uploads          []HandoffUpload `json:"uploads"`
}

// artifactContentChecksum hex-encodes the sha256 of one stored text.
func artifactContentChecksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// uploadArtifactType matches one attachment question to the stored
// file it asks for. Unknown uploads match nothing rather than a
// wrong file.
func uploadArtifactType(questionText string) string {
	lowered := strings.ToLower(questionText)
	for _, keyword := range []string{"cv", "resume", "curriculum vitae"} {
		if containsKeyword(lowered, keyword) {
			return ArtifactCV
		}
	}
	for _, keyword := range []string{"cover letter", "covering letter", "motivation letter", "motivational letter", "motivation", "motivatie"} {
		if containsKeyword(lowered, keyword) {
			return ArtifactCoverLetter
		}
	}
	return ""
}

// HandoffProjection reads the manual Handoff basis for one selected
// role. Blocked and outdated checks keep their retained route,
// documents and prior content readable; the workflow stage reports
// whether the owner already saved the Handoff. The read performs no
// transition: saving the Handoff is an explicit write owned by the
// integration lane.
func (s *Store) HandoffProjection(ctx context.Context, opportunityID string) (HandoffView, error) {
	if opportunityID == "" {
		return HandoffView{}, ErrInvalid
	}
	opportunity, err := s.Opportunity(ctx, opportunityID)
	if err != nil {
		return HandoffView{}, err
	}
	if opportunity.ArchivedAt != "" {
		return HandoffView{}, ErrNotFound
	}
	company, err := s.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return HandoffView{}, err
	}
	workflow, err := s.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return HandoffView{}, err
	}
	set, err := s.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return HandoffView{}, err
	}
	view := HandoffView{OpportunityID: opportunityID, Title: opportunity.Title,
		CompanyName: company.Name, CheckID: set.CheckID, CheckStatus: set.CheckStatus,
		WorkflowStage: workflow.Stage, Items: []HandoffItem{}, Uploads: []HandoffUpload{}}
	status, err := s.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return HandoffView{}, err
	}
	if status.Check != nil {
		view.RouteKind = status.Check.Route.Kind
		view.RouteDestination = status.Check.Route.DestinationText
		view.RouteExcerpt = status.Check.Route.SourceExcerpt
	}
	byType := map[string]ArtifactReadinessEntry{}
	for _, entry := range set.Entries {
		byType[entry.Type] = entry
		if !entry.Required && entry.Current == nil {
			continue
		}
		item := HandoffItem{Type: entry.Type, Required: entry.Required,
			State: entry.State, Reason: entry.Reason, Basis: entry.Basis}
		if entry.Current != nil {
			item.Version = entry.Current.Version
			item.Content = entry.Current.Content
			item.ContentSHA256 = artifactContentChecksum(entry.Current.Content)
		}
		if entry.Type == ArtifactFormValues {
			item.FormValues = entry.FormValues
		}
		view.Items = append(view.Items, item)
	}
	if status.Check != nil {
		for _, question := range status.Check.Questions {
			if question.Kind != CheckQuestionAttachment {
				continue
			}
			upload := HandoffUpload{QuestionID: question.ID, QuestionText: question.Text,
				Required: question.Required, ArtifactType: uploadArtifactType(question.Text),
				State: ArtifactStateHeld}
			if upload.ArtifactType != "" {
				if entry, ok := byType[upload.ArtifactType]; ok {
					upload.State = entry.State
					if entry.Current != nil {
						upload.Version = entry.Current.Version
						upload.ContentSHA256 = artifactContentChecksum(entry.Current.Content)
					}
				}
			}
			view.Uploads = append(view.Uploads, upload)
		}
	}
	return view, nil
}
