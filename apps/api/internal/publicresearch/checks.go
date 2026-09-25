// Selected-role public checks (lane R, task E07).
//
// The CheckService runs only on the owner's explicit Check action and only
// for explicitly selected vacancy refs. It reuses the saved capture behind
// each selected vacancy's receipt when one resolves, otherwise retrieves
// the actual public detail page through the same run-scoped Executor
// backend that serves E03's public tools, and saves employer questions
// found verbatim in captured bytes. Questions are never invented: a
// prompt is saved only when it is a verbatim substring of bytes opened
// from a real capture, and every saved question links its vacancy and
// source URL.
//
// Generic question categories are a fixed catalog prepared here for a
// later Jev choice (E08); this service assigns no per-question category.
// Unselected vacancies are never read, fetched or saved. A missing route
// (unknown vacancy, unresolvable evidence, failed fetch) blocks only
// that role: every selected role reports its own checked/blocked outcome.
package publicresearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
)

// QuestionCategoryCatalogVersion pins the generic category set saved with
// each check report so a later Jev choice binds a stable catalog.
const QuestionCategoryCatalogVersion = "e07-v1"

// QuestionCategory is one generic bucket a later Jev choice may assign to
// an actual employer question. The catalog is fixed: preparing it here
// performs no classification and no model call.
type QuestionCategory struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// QuestionCategories returns the fixed generic catalog for later Jev
// choice. It is deterministic and touches no backend.
func QuestionCategories() []QuestionCategory {
	return []QuestionCategory{
		{ID: "eligibility", Description: "Work rights, visas and permits for the listed location."},
		{ID: "availability", Description: "Start date, hours, on-site presence and notice period."},
		{ID: "experience", Description: "Past work, skills and evidence of capability."},
		{ID: "motivation", Description: "Interest in the role, the employer and the field."},
		{ID: "logistics", Description: "Salary expectations, travel, relocation and equipment."},
		{ID: "other", Description: "Any other actual employer question."},
	}
}

// RoleCheckOutcome is the per-role terminal state of one explicit check.
type RoleCheckOutcome string

const (
	// RoleChecked means the role's evidence was retrieved (reused or
	// fetched) and its verbatim questions saved; zero questions is still
	// checked when the capture held none.
	RoleChecked RoleCheckOutcome = "checked"
	// RoleBlocked means this role alone could not be checked; the report
	// carries the reason and every other selected role still ran.
	RoleBlocked RoleCheckOutcome = "blocked"
)

// QuestionSource pins one saved question to its verbatim capture span:
// the bytes text[Start:End] of capture CaptureID equal the saved
// question's prompt exactly, with Start >= 0 and End > Start.
type QuestionSource struct {
	QuestionRef string `json:"question_ref"`
	CaptureID   string `json:"capture_id"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
}

// RoleCheck is the evidence chain for one selected role.
type RoleCheck struct {
	VacancyRef    string                    `json:"vacancy_ref"`
	Outcome       RoleCheckOutcome          `json:"outcome"`
	ReusedCapture bool                      `json:"reused_capture"`
	CaptureIDs    []string                  `json:"capture_ids"`
	ReceiptIDs    []string                  `json:"receipt_ids"`
	Questions     []musecode.PublicQuestion `json:"questions"`
	Sources       []QuestionSource          `json:"question_sources"`
	// BlockReason names why this role alone blocked; empty when checked.
	// Vocabulary for M's mapping: "unknown_vacancy" (selected ref was
	// never saved), "missing_route" (no public detail route, fetch
	// returned no capture, or the fetched capture could not be opened),
	// "fetch_error" (detail fetch failed outside the contract),
	// "save_error" (question save failed or returned no question), or a
	// passthrough researchcontract outcome code ("not_found",
	// "budget_exhausted", "rate_limited", ...) from a refused fetch or a
	// failed question save. Detail carries the human-readable cause.
	BlockReason string `json:"block_reason,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// CheckReport is the terminal record of one explicit check action.
type CheckReport struct {
	CheckedAt              string             `json:"checked_at"`
	CategoryCatalogVersion string             `json:"category_catalog_version"`
	Categories             []QuestionCategory `json:"categories"`
	Roles                  []RoleCheck        `json:"roles"`
}

// Selection is the owner's explicit choice of vacancy refs to check.
// Building a Selection performs no backend call; only Check acts on it.
type Selection struct {
	VacancyRefs []string
}

// NewSelection records an explicit owner choice. It never touches a backend.
func NewSelection(refs ...string) Selection {
	return Selection{VacancyRefs: append([]string(nil), refs...)}
}

// CheckDeps binds one check service. Executor and Captures must be the
// same run-scoped backends that serve E03's public tools; Saved is the
// E03 server holding the saved vacancies and questions. Bounds, RunID
// and Generation scope every fresh retrieval; Now is an optional clock.
type CheckDeps struct {
	Executor   researchcontract.Executor
	Captures   researchcontract.CaptureReader
	Saved      *Server
	Bounds     musecode.Bounds
	RunID      string
	Generation int64
	Now        func() time.Time
}

// CheckService checks only explicitly selected public roles.
type CheckService struct {
	executor   researchcontract.Executor
	captures   researchcontract.CaptureReader
	saved      *Server
	bounds     musecode.Bounds
	runID      string
	generation int64
	now        func() time.Time
	startedAt  time.Time
}

// NewCheckService builds the E07 seam M wires at E08. Invalid deps fail
// closed before any check can run.
func NewCheckService(deps CheckDeps) (*CheckService, error) {
	if deps.Executor == nil || deps.Captures == nil {
		return nil, errors.New("publicresearch: check executor and capture reader required")
	}
	if deps.Saved == nil {
		return nil, errors.New("publicresearch: check saved store required")
	}
	if err := deps.Bounds.Validate(); err != nil {
		return nil, err
	}
	if deps.RunID == "" {
		return nil, errors.New("publicresearch: check run id required")
	}
	if deps.Generation <= 0 {
		return nil, errors.New("publicresearch: check positive generation required")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &CheckService{
		executor: deps.Executor, captures: deps.Captures, saved: deps.Saved,
		bounds: deps.Bounds, runID: deps.RunID, generation: deps.Generation,
		now: now, startedAt: now(),
	}, nil
}

// Check runs one explicit check over exactly the selected refs, in
// selection order. Unselected vacancies are untouched. A per-role failure
// blocks only that role; the returned error is reserved for a canceled
// context, never for a role-level miss.
func (c *CheckService) Check(ctx context.Context, sel Selection) (CheckReport, error) {
	report := CheckReport{
		CheckedAt:              c.now().UTC().Format(time.RFC3339),
		CategoryCatalogVersion: QuestionCategoryCatalogVersion,
		Categories:             QuestionCategories(),
		Roles:                  []RoleCheck{},
	}
	for _, ref := range sel.VacancyRefs {
		if err := ctx.Err(); err != nil {
			return CheckReport{}, err
		}
		report.Roles = append(report.Roles, c.checkOne(ctx, ref))
	}
	return report, nil
}

// checkOne checks a single selected role: reuse the saved capture when it
// resolves, otherwise fetch the actual public detail page.
func (c *CheckService) checkOne(ctx context.Context, ref string) RoleCheck {
	role := RoleCheck{VacancyRef: ref, Outcome: RoleChecked, CaptureIDs: []string{}, ReceiptIDs: []string{}, Questions: []musecode.PublicQuestion{}, Sources: []QuestionSource{}}
	vac, ok := c.lookupVacancy(ref)
	if !ok {
		role.Outcome = RoleBlocked
		role.BlockReason = "unknown_vacancy"
		role.Detail = "vacancy " + ref + " was never saved"
		return role
	}
	if strings.TrimSpace(vac.PageURL) == "" {
		role.Outcome = RoleBlocked
		role.BlockReason = "missing_route"
		role.Detail = "vacancy " + ref + " has no public detail route"
		return role
	}
	if text, captureID, receiptID, ok := c.reuseSaved(ctx, vac); ok {
		role.ReusedCapture = true
		role.CaptureIDs = append(role.CaptureIDs, captureID)
		role.ReceiptIDs = append(role.ReceiptIDs, receiptID)
		return c.saveVerbatim(ctx, vac, role, text, sourceURL(receiptFinalURL(ctx, c.captures, receiptID), vac.PageURL))
	}
	text, captureID, receiptID, blocked := c.fetchDetail(ctx, vac)
	if blocked != nil {
		return *blocked
	}
	role.CaptureIDs = append(role.CaptureIDs, captureID)
	if receiptID != "" {
		role.ReceiptIDs = append(role.ReceiptIDs, receiptID)
	}
	return c.saveVerbatim(ctx, vac, role, text, sourceURL(receiptFinalURL(ctx, c.captures, receiptID), vac.PageURL))
}

// lookupVacancy reads one saved vacancy without touching any backend.
func (c *CheckService) lookupVacancy(ref string) (musecode.PublicVacancy, bool) {
	c.saved.mu.Lock()
	defer c.saved.mu.Unlock()
	vac, ok := c.saved.vacancies[ref]
	return vac, ok
}

// reuseSaved opens the capture behind the vacancy's saved receipt.
// ok=false means no saved evidence resolved and the caller must fetch.
func (c *CheckService) reuseSaved(ctx context.Context, vac musecode.PublicVacancy) (text, captureID, receiptID string, ok bool) {
	if strings.TrimSpace(vac.ReceiptRef) == "" {
		return "", "", "", false
	}
	receipt, err := c.captures.ResolveReceipt(ctx, vac.ReceiptRef)
	if err != nil || receipt.CaptureID == "" {
		return "", "", "", false
	}
	text, ok = c.readCapture(ctx, receipt.CaptureID)
	if !ok {
		return "", "", "", false
	}
	return text, receipt.CaptureID, receipt.ID, true
}

// fetchDetail retrieves the actual public detail page for one vacancy.
// A failure blocks only this role.
func (c *CheckService) fetchDetail(ctx context.Context, vac musecode.PublicVacancy) (string, string, string, *RoleCheck) {
	blocked := func(reason, detail string) (string, string, string, *RoleCheck) {
		return "", "", "", &RoleCheck{
			VacancyRef: vac.VacancyRef, Outcome: RoleBlocked,
			CaptureIDs: []string{}, ReceiptIDs: []string{}, Questions: []musecode.PublicQuestion{}, Sources: []QuestionSource{},
			BlockReason: reason, Detail: detail,
		}
	}
	out, err := c.executor.Execute(ctx, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: researchexecute.BackendHTTP,
			Method: http.MethodGet, URLOrQuery: vac.PageURL,
		},
		Bounds: c.execBounds(), RunID: c.runID, Generation: c.generation,
	})
	if err != nil {
		if out, ok := contractOutcome(err); ok {
			return blocked(string(outcomeOf(out)), "detail fetch refused for "+vac.VacancyRef+": "+detailOf(out))
		}
		return blocked("fetch_error", "detail fetch failed for "+vac.VacancyRef+": "+err.Error())
	}
	if out.Outcome != researchcontract.OutcomeOK && out.Outcome != researchcontract.OutcomeReused {
		return blocked(string(out.Outcome), "detail fetch ended "+string(out.Outcome)+" for "+vac.VacancyRef)
	}
	if out.CaptureID == "" {
		return blocked("missing_route", "detail fetch returned no capture for "+vac.VacancyRef)
	}
	text, ok := c.readCapture(ctx, out.CaptureID)
	if !ok {
		return blocked("missing_route", "fetched capture "+out.CaptureID+" could not be opened for "+vac.VacancyRef)
	}
	return text, out.CaptureID, out.Receipt.ID, nil
}

// readCapture opens one immutable capture and returns at most the
// per-operation byte bound of its stored bytes.
func (c *CheckService) readCapture(ctx context.Context, captureID string) (string, bool) {
	_, rc, err := c.captures.OpenCapture(ctx, captureID)
	if err != nil {
		return "", false
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, c.bounds.MaxBytesPerOp+1))
	if err != nil {
		return "", false
	}
	if int64(len(data)) > c.bounds.MaxBytesPerOp {
		data = data[:c.bounds.MaxBytesPerOp]
	}
	return string(data), true
}

// saveVerbatim saves every verbatim employer question found in captured
// text. Prompts that are not substrings of the capture are dropped, so a
// bug in extraction can never invent a question.
func (c *CheckService) saveVerbatim(ctx context.Context, vac musecode.PublicVacancy, role RoleCheck, text, sourceURL string) RoleCheck {
	for _, prompt := range extractQuestions(text) {
		if !strings.Contains(text, prompt.Text) {
			continue
		}
		out, err := c.saved.saveQuestionTool(ctx, saveQuestionArgs{
			VacancyRef: vac.VacancyRef, PromptText: prompt.Text,
			Required: prompt.Required, SourceURL: sourceURL,
		})
		if err != nil {
			role.Outcome = RoleBlocked
			role.BlockReason = "save_error"
			role.Detail = "question save failed for " + vac.VacancyRef + ": " + err.Error()
			return role
		}
		if outcomeOf(out) != "ok" {
			role.Outcome = RoleBlocked
			role.BlockReason = string(outcomeOf(out))
			role.Detail = "question save ended " + string(outcomeOf(out)) + " for " + vac.VacancyRef
			return role
		}
		saved, ok := out["question"].(musecode.PublicQuestion)
		if !ok {
			role.Outcome = RoleBlocked
			role.BlockReason = "save_error"
			role.Detail = "question save returned no question for " + vac.VacancyRef
			return role
		}
		role.Questions = append(role.Questions, saved)
		captureID := ""
		if n := len(role.CaptureIDs); n > 0 {
			captureID = role.CaptureIDs[n-1]
		}
		if at := strings.Index(text, prompt.Text); at >= 0 {
			role.Sources = append(role.Sources, QuestionSource{
				QuestionRef: saved.QuestionRef, CaptureID: captureID,
				Start: at, End: at + len(prompt.Text),
			})
		}
	}
	return role
}

// execBounds derives one detail retrieval's caps from the session bounds.
func (c *CheckService) execBounds() researchcontract.Bounds {
	remainingWall := c.bounds.MaxWallClock - c.now().Sub(c.startedAt)
	if remainingWall < time.Millisecond {
		remainingWall = time.Millisecond
	}
	return researchcontract.Bounds{
		MaxBytes:    c.bounds.MaxBytesPerOp,
		MaxRequests: 2,
		DeadlineMs:  int64(remainingWall / time.Millisecond),
	}
}

// questionCandidate is one interrogative line found verbatim in a capture.
type questionCandidate struct {
	Text     string
	Required bool
}

// extractQuestions returns interrogative lines verbatim: trimmed lines
// ending with "?", within length bounds, deduplicated, capped. It
// performs no rewriting, so its output can only quote the capture.
// Required is a mechanical marker rule only: an explicit "(required)" or
// "[required]" marker, or a "*" flag, in the captured line.
func extractQuestions(text string) []questionCandidate {
	var out []questionCandidate
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 8 || len(trimmed) > 500 || !strings.HasSuffix(trimmed, "?") {
			continue
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		lower := strings.ToLower(trimmed)
		out = append(out, questionCandidate{
			Text:     trimmed,
			Required: strings.Contains(lower, "(required)") || strings.Contains(lower, "[required]") || strings.Contains(trimmed, "*"),
		})
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// sourceURL prefers the backend-issued final URL and falls back to the
// saved vacancy route. Both are real captured addresses, never composed.
func sourceURL(finalURL, pageURL string) string {
	if strings.TrimSpace(finalURL) != "" {
		return finalURL
	}
	return pageURL
}

// receiptFinalURL reads the backend-issued final URL for one receipt,
// returning "" when the receipt cannot be resolved.
func receiptFinalURL(ctx context.Context, caps researchcontract.CaptureReader, receiptID string) string {
	if strings.TrimSpace(receiptID) == "" {
		return ""
	}
	receipt, err := caps.ResolveReceipt(ctx, receiptID)
	if err != nil {
		return ""
	}
	return receipt.FinalURL
}

func outcomeOf(out map[string]any) researchcontract.Outcome {
	code, _ := out["outcome"].(string)
	return researchcontract.Outcome(code)
}

// detailOf renders the actionable detail of an in-band contract outcome.
func detailOf(out map[string]any) string {
	detail, _ := out["detail"].(string)
	field, _ := out["field"].(string)
	if field != "" && detail != "" {
		return field + ": " + detail
	}
	return detail
}
