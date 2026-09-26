package publicresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
)

// Direct deterministic evidence API (R1). The Contributor CLI is invoked
// directly with its native web tools and returns structured sightings plus
// source URLs; it never touches this server. The app then fetches every
// cited URL itself (FetchURL) and saves only what its own captures verify
// (SaveVacancy/SaveQuestion). Model text that cites an unfetchable URL or
// mismatches captured bytes becomes an honest gap in the caller — never a
// saved claim.
//
// Evidence/save boundary: FetchURL performs the network read through the
// bounded run executor and returns the trusted receipt; SaveVacancy binds
// one vacancy projection to such a receipt; SaveQuestion records one
// employer question the caller already verified verbatim against captured
// bytes. Invalid input consumes zero allowance; every admitted call charges
// the run's request/byte bounds exactly like the tool paths.
//
// The MCP tool registry (server.go) is retained only for the stop/resume
// harness, which drives saves through it; R2 removes it with the harness.
// New production code must use this file, never the tools.

// FetchResult is the trusted outcome of one deterministic URL fetch.
type FetchResult struct {
	Receipt   researchcontract.ExecutionReceipt
	CaptureID string
	FinalURL  string
}

// FetchURL fetches one cited public URL with a deterministic GET through
// the run executor. Only OK/reused outcomes with a capture return success;
// any other outcome or status is a plain error for the caller's gap. The
// idempotency key derives from the URL, so repeat fetches of one citation
// honestly reuse the recorded receipt instead of re-reading the network.
func (s *Server) FetchURL(ctx context.Context, rawURL string) (FetchResult, error) {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch url must be an http(s) URL with a host")
	}
	if reject, ok := s.reserve(); !ok {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch refused: %s: %s", reject["field"], reject["detail"])
	}
	sum := sha256.Sum256([]byte("direct-fetch\x00" + parsed.String()))
	out, err := s.executor.Execute(ctx, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: researchexecute.BackendHTTP,
			Method: http.MethodGet, URLOrQuery: parsed.String(),
		},
		Bounds:         s.execBounds(),
		IdempotencyKey: "direct-fetch-" + hex.EncodeToString(sum[:16]),
		RunID:          s.runID, Generation: s.Generation(),
	})
	if err != nil {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch %s: %w", parsed.Redacted(), err)
	}
	if reject, ok := s.charge(out.Usage.Bytes); !ok {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch refused: %s: %s", reject["field"], reject["detail"])
	}
	if out.Outcome != researchcontract.OutcomeOK && out.Outcome != researchcontract.OutcomeReused {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch %s ended %s", parsed.Redacted(), out.Outcome)
	}
	if out.Receipt.Status != researchcontract.ReceiptOK && out.Receipt.Status != researchcontract.ReceiptReused &&
		out.Receipt.Status != researchcontract.ReceiptShared && out.Receipt.Status != researchcontract.ReceiptLate {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch %s receipt is %s, not usable evidence",
			parsed.Redacted(), out.Receipt.Status)
	}
	captureID := out.CaptureID
	if captureID == "" {
		captureID = out.Receipt.CaptureID
	}
	if captureID == "" {
		return FetchResult{}, fmt.Errorf("publicresearch: fetch %s carries no capture", parsed.Redacted())
	}
	return FetchResult{Receipt: out.Receipt, CaptureID: captureID, FinalURL: out.Receipt.FinalURL}, nil
}

// SaveVacancyInput mirrors the vacancy saver fields. Receipt must be a
// trusted receipt id from FetchURL (or an equivalent recorded capture);
// page fields come from the CLI's structured sighting and are bound to
// that receipt, exactly as the tool path binds caller fields.
type SaveVacancyInput struct {
	PageURL      string
	EmployerName string
	Title        string
	LocationText string
	PostedText   string
	WorkPattern  string
	SourceHost   string
	Receipt      string
}

// SaveVacancy saves one vacancy bound to a trusted receipt with the
// tool path's validation (required fields, URL shape, receipt carrying a
// capture). A page URL already saved in this run reuses its existing ref
// (reused=true) instead of forking a second identity: resume turns that
// re-report an earlier sighting converge on the same ref.
func (s *Server) SaveVacancy(ctx context.Context, in SaveVacancyInput) (musecode.PublicVacancy, bool, error) {
	if strings.TrimSpace(in.EmployerName) == "" {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "employer_name",
			Detail: "employer_name is required; record the employer exactly as the listing names it"}
	}
	if strings.TrimSpace(in.PageURL) == "" {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "page_url", Detail: "page_url is required"}
	}
	page, err := url.Parse(in.PageURL)
	if err != nil || page.Hostname() == "" || (page.Scheme != "https" && page.Scheme != "http") {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "page_url",
			Detail: "page_url must be an http(s) URL with a host"}
	}
	if strings.TrimSpace(in.Title) == "" {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "title", Detail: "title is required"}
	}
	if strings.TrimSpace(in.Receipt) == "" {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "receipt",
			Detail: "receipt is required; a model assertion is never evidence"}
	}
	host := in.SourceHost
	if host == "" {
		host = page.Hostname()
	}
	if reject, ok := s.reserve(); !ok {
		return musecode.PublicVacancy{}, false, &SaveError{Field: strField(reject, "field"),
			Detail: strField(reject, "detail"), Limited: true}
	}
	receiptID := in.Receipt
	receipt, err := s.captures.ResolveReceipt(ctx, receiptID)
	if err != nil {
		if fallback, ok := s.receiptFallback(ctx, receiptID, err); ok {
			receipt, receiptID = fallback, fallback.ID
		} else {
			return musecode.PublicVacancy{}, false, &SaveError{Field: "receipt",
				Detail: fmt.Sprintf("untrusted receipt %q: %s", shortRef(receiptID), saveCause(err))}
		}
	}
	if receipt.CaptureID == "" {
		return musecode.PublicVacancy{}, false, &SaveError{Field: "receipt",
			Detail: "receipt carries no capture; a vacancy needs captured evidence"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vacancyURLKey(page)
	if ref, dup := s.vacByURL[key]; dup {
		if vac, ok := s.vacancies[ref]; ok {
			return vac, true, nil
		}
	}
	s.vacSeq++
	vac := musecode.PublicVacancy{
		VacancyRef:   fmt.Sprintf("vac-%04d", s.vacSeq),
		SourceHost:   host,
		PageURL:      in.PageURL,
		EmployerName: in.EmployerName,
		Title:        in.Title,
		LocationText: in.LocationText,
		PostedText:   in.PostedText,
		WorkPattern:  musecode.SanitizeWorkPattern(in.WorkPattern),
		CapturedAt:   s.now().UTC().Format(time.RFC3339),
		ReceiptRef:   receiptID,
	}
	if reject, ok := s.chargeLocked(outputSize(map[string]any{"outcome": "ok", "vacancy": vac})); !ok {
		s.vacSeq--
		return musecode.PublicVacancy{}, false, &SaveError{Field: strField(reject, "field"),
			Detail: strField(reject, "detail"), Limited: true}
	}
	s.vacancies[vac.VacancyRef] = vac
	s.vacOrder = append(s.vacOrder, vac.VacancyRef)
	s.vacByURL[key] = vac.VacancyRef
	return vac, false, nil
}

// SaveQuestionInput mirrors the question saver fields. The caller must
// have verified PromptText verbatim against the cited capture; this save
// records that verified question under one already-saved vacancy.
type SaveQuestionInput struct {
	VacancyRef string
	PromptText string
	Required   bool
	SourceURL  string
}

// SaveQuestion records one verified employer question under an
// already-saved vacancy, with the tool path's validation.
func (s *Server) SaveQuestion(_ context.Context, in SaveQuestionInput) (musecode.PublicQuestion, error) {
	if strings.TrimSpace(in.VacancyRef) == "" {
		return musecode.PublicQuestion{}, &SaveError{Field: "vacancy_ref", Detail: "vacancy_ref is required"}
	}
	if strings.TrimSpace(in.PromptText) == "" {
		return musecode.PublicQuestion{}, &SaveError{Field: "prompt_text",
			Detail: "prompt_text is required; questions are never invented"}
	}
	if strings.TrimSpace(in.SourceURL) == "" {
		return musecode.PublicQuestion{}, &SaveError{Field: "source_url", Detail: "source_url is required"}
	}
	if reject, ok := s.reserve(); !ok {
		return musecode.PublicQuestion{}, &SaveError{Field: strField(reject, "field"),
			Detail: strField(reject, "detail"), Limited: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vac, ok := s.vacancies[in.VacancyRef]
	if !ok {
		return musecode.PublicQuestion{}, &SaveError{Field: "vacancy_ref", Detail: "unknown vacancy " + in.VacancyRef}
	}
	s.qSeq++
	q := musecode.PublicQuestion{
		QuestionRef: fmt.Sprintf("q-%04d", s.qSeq),
		VacancyRef:  vac.VacancyRef,
		PromptText:  in.PromptText,
		Required:    in.Required,
		SourceURL:   in.SourceURL,
	}
	if reject, ok := s.chargeLocked(outputSize(map[string]any{"outcome": "ok", "question": q})); !ok {
		s.qSeq--
		return musecode.PublicQuestion{}, &SaveError{Field: strField(reject, "field"),
			Detail: strField(reject, "detail"), Limited: true}
	}
	s.questions[q.QuestionRef] = q
	s.qOrder = append(s.qOrder, q.QuestionRef)
	return q, nil
}

// VacancyByURL returns the run's saved vacancy for a page URL, if any.
// URLs normalize by scheme/host case folding only; path, query and
// fragment stay significant.
func (s *Server) VacancyByURL(rawURL string) (musecode.PublicVacancy, bool) {
	if s == nil {
		return musecode.PublicVacancy{}, false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return musecode.PublicVacancy{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.vacByURL[vacancyURLKey(parsed)]
	if !ok {
		return musecode.PublicVacancy{}, false
	}
	vac, ok := s.vacancies[ref]
	return vac, ok
}

// SaveError is a deterministic saver failure. Field/Detail reuse the tool
// outcome vocabulary; Limited reports a run-bound refusal rather than bad
// input.
type SaveError struct {
	Field   string
	Detail  string
	Limited bool
}

func (e *SaveError) Error() string {
	return fmt.Sprintf("publicresearch: save %s: %s", e.Field, e.Detail)
}

func saveCause(err error) string {
	if err == nil {
		return "unknown error"
	}
	return strings.TrimSpace(err.Error())
}

func strField(out map[string]any, key string) string {
	raw, _ := out[key].(string)
	if raw == "" {
		return key
	}
	return raw
}

// vacancyURLKey folds scheme/host case; everything else is significant.
func vacancyURLKey(parsed *url.URL) string {
	dup := *parsed
	dup.Scheme = strings.ToLower(dup.Scheme)
	dup.Host = strings.ToLower(dup.Host)
	return dup.String()
}

// chargeLocked is charge under the already-held store lock.
func (s *Server) chargeLocked(opBytes int64) (map[string]any, bool) {
	if opBytes > s.bounds.MaxBytesPerOp {
		return limitOutcome("bytes_per_op", "per-operation byte bound exceeded"), false
	}
	if s.bytesOut+opBytes > s.bounds.MaxBytesTotal {
		return limitOutcome("bytes_total", "total byte bound exceeded; the run stops here"), false
	}
	s.bytesOut += opBytes
	return nil, true
}
