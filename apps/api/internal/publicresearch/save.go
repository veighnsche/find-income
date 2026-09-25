package publicresearch

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// saveVacancyArgs is the public_save_vacancy tool input. Only public
// listing fields exist; there is no slot for private owner, Jev or
// session data. Receipt binds the vacancy to trusted captured evidence.
type saveVacancyArgs struct {
	PageURL        string `json:"page_url"`
	EmployerName   string `json:"employer_name,omitempty"`
	Title          string `json:"title"`
	LocationText   string `json:"location_text,omitempty"`
	PostedText     string `json:"posted_text,omitempty"`
	SourceHost     string `json:"source_host,omitempty"`
	Receipt        string `json:"receipt"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// saveQuestionArgs is the public_save_question tool input: one actual
// employer question from an already-saved vacancy page.
type saveQuestionArgs struct {
	VacancyRef string `json:"vacancy_ref"`
	PromptText string `json:"prompt_text"`
	Required   bool   `json:"required,omitempty"`
	SourceURL  string `json:"source_url"`
}

// listSavedArgs is the public_list_saved tool input.
type listSavedArgs struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func (s *Server) saveVacancyTool(ctx context.Context, args saveVacancyArgs) (map[string]any, error) {
	if strings.TrimSpace(args.PageURL) == "" {
		return invalidOutcome("page_url", "page_url is required"), nil
	}
	page, err := url.Parse(args.PageURL)
	if err != nil || page.Hostname() == "" || (page.Scheme != "https" && page.Scheme != "http") {
		return invalidOutcome("page_url", "page_url must be an http(s) URL with a host"), nil
	}
	if strings.TrimSpace(args.Title) == "" {
		return invalidOutcome("title", "title is required"), nil
	}
	if strings.TrimSpace(args.Receipt) == "" {
		return invalidOutcome("receipt", "receipt is required; a model assertion is never evidence"), nil
	}
	host := args.SourceHost
	if host == "" {
		host = page.Hostname()
	}
	if reject, ok := s.reserve(); !ok {
		return reject, nil
	}
	receipt, err := s.captures.ResolveReceipt(ctx, args.Receipt)
	if err != nil {
		if out, ok := contractOutcome(err); ok {
			return s.admit(out), nil
		}
		return nil, err
	}
	if receipt.CaptureID == "" {
		return s.admit(invalidOutcome("receipt", "receipt carries no capture; a vacancy needs captured evidence")), nil
	}
	s.mu.Lock()
	if args.IdempotencyKey != "" {
		if ref, dup := s.idemVac[args.IdempotencyKey]; dup {
			vac := s.vacancies[ref]
			s.mu.Unlock()
			return s.admit(map[string]any{"outcome": "reused", "vacancy": vac}), nil
		}
	}
	s.vacSeq++
	vac := musecode.PublicVacancy{
		VacancyRef:   fmt.Sprintf("vac-%04d", s.vacSeq),
		SourceHost:   host,
		PageURL:      args.PageURL,
		EmployerName: args.EmployerName,
		Title:        args.Title,
		LocationText: args.LocationText,
		PostedText:   args.PostedText,
		CapturedAt:   s.now().UTC().Format(time.RFC3339),
		ReceiptRef:   args.Receipt,
	}
	s.vacancies[vac.VacancyRef] = vac
	s.vacOrder = append(s.vacOrder, vac.VacancyRef)
	if args.IdempotencyKey != "" {
		s.idemVac[args.IdempotencyKey] = vac.VacancyRef
	}
	s.mu.Unlock()
	return s.admit(map[string]any{"outcome": "ok", "vacancy": vac}), nil
}

func (s *Server) saveQuestionTool(_ context.Context, args saveQuestionArgs) (map[string]any, error) {
	if strings.TrimSpace(args.VacancyRef) == "" {
		return invalidOutcome("vacancy_ref", "vacancy_ref is required"), nil
	}
	if strings.TrimSpace(args.PromptText) == "" {
		return invalidOutcome("prompt_text", "prompt_text is required; questions are never invented"), nil
	}
	if strings.TrimSpace(args.SourceURL) == "" {
		return invalidOutcome("source_url", "source_url is required"), nil
	}
	if reject, ok := s.reserve(); !ok {
		return reject, nil
	}
	s.mu.Lock()
	vac, ok := s.vacancies[args.VacancyRef]
	if !ok {
		s.mu.Unlock()
		return s.admit(notFoundOutcome("vacancy_ref", "unknown vacancy "+args.VacancyRef)), nil
	}
	s.qSeq++
	q := musecode.PublicQuestion{
		QuestionRef: fmt.Sprintf("q-%04d", s.qSeq),
		VacancyRef:  vac.VacancyRef,
		PromptText:  args.PromptText,
		Required:    args.Required,
		SourceURL:   args.SourceURL,
	}
	s.questions[q.QuestionRef] = q
	s.qOrder = append(s.qOrder, q.QuestionRef)
	s.mu.Unlock()
	return s.admit(map[string]any{"outcome": "ok", "question": q}), nil
}

func (s *Server) listSavedTool(_ context.Context, args listSavedArgs) (map[string]any, error) {
	limit := args.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 0 {
		return invalidOutcome("limit", "limit must not be negative"), nil
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if args.Cursor != "" {
		at, err := strconv.Atoi(args.Cursor)
		if err != nil || at < 0 {
			return invalidOutcome("cursor", "cursor must be a decimal offset from a prior page"), nil
		}
		offset = at
	}
	if reject, ok := s.reserve(); !ok {
		return reject, nil
	}
	s.mu.Lock()
	vacancies := pageRefs(s.vacOrder, s.vacancies, offset, limit)
	questions := pageRefs(s.qOrder, s.questions, offset, limit)
	next := offset + limit
	s.mu.Unlock()
	out := map[string]any{
		"outcome":   "ok",
		"vacancies": vacancies,
		"questions": questions,
	}
	if next < len(s.vacOrder) || next < len(s.qOrder) {
		out["next_cursor"] = strconv.Itoa(next)
	}
	return s.admit(out), nil
}

func pageRefs[T any](order []string, byRef map[string]T, offset, limit int) []T {
	out := []T{}
	for _, ref := range order {
		if offset > 0 {
			offset--
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, byRef[ref])
	}
	return out
}
