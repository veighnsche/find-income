package publicresearch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// SeedVacancy mirrors the public_save_vacancy tool args for direct seeding.
// SourceHost is always derived from PageURL, exactly as the tool does when
// the caller leaves source_host empty; this path carries no idempotency key.
type SeedVacancy struct {
	PageURL, EmployerName, Title, LocationText, PostedText, ReceiptRef string
}

// SeedError reports which vacancy of a SeedVacancies batch failed and why.
// Index is the input position of the first failure; Field and Detail reuse
// the same vocabulary as the matching public_save_vacancy tool outcome.
type SeedError struct {
	Index  int
	Field  string
	Detail string
}

// Error renders the failure with its batch index.
func (e *SeedError) Error() string {
	return fmt.Sprintf("publicresearch: seed vacancy %d: %s: %s", e.Index, e.Field, e.Detail)
}

// SeedVacancies saves each vacancy with IDENTICAL validation to the MCP tool
// (URL shape, trusted receipt, bounds). First failure aborts with the index.
// The batch is atomic: every entry is validated before anything is saved, so
// a failure leaves no partial saves from that call. Empty input returns
// empty with no error and consumes no allowance. It is safe for concurrent
// use with the tool paths and other seed calls.
func (s *Server) SeedVacancies(vacancies []SeedVacancy) ([]musecode.PublicVacancy, error) {
	if len(vacancies) == 0 {
		return []musecode.PublicVacancy{}, nil
	}
	ctx := context.Background()
	hosts := make([]string, len(vacancies))
	for i, v := range vacancies {
		host, err := s.validateSeed(ctx, i, v)
		if err != nil {
			return nil, err
		}
		hosts[i] = host
	}
	return s.commitSeeds(vacancies, hosts)
}

// validateSeed mirrors saveVacancyTool's checks in order: required fields,
// URL shape, then trusted-receipt resolution. It mutates nothing and
// consumes no allowance, so a batch failure costs zero budget.
func (s *Server) validateSeed(ctx context.Context, index int, v SeedVacancy) (string, error) {
	fail := func(field, detail string) (string, error) {
		return "", &SeedError{Index: index, Field: field, Detail: detail}
	}
	if strings.TrimSpace(v.PageURL) == "" {
		return fail("page_url", "page_url is required")
	}
	page, err := url.Parse(v.PageURL)
	if err != nil || page.Hostname() == "" || (page.Scheme != "https" && page.Scheme != "http") {
		return fail("page_url", "page_url must be an http(s) URL with a host")
	}
	if strings.TrimSpace(v.Title) == "" {
		return fail("title", "title is required")
	}
	if strings.TrimSpace(v.ReceiptRef) == "" {
		return fail("receipt", "receipt is required; a model assertion is never evidence")
	}
	receipt, err := s.captures.ResolveReceipt(ctx, v.ReceiptRef)
	if err != nil {
		var cerr *researchcontract.Error
		if errors.As(err, &cerr) {
			field, detail := cerr.Field, cerr.Detail
			if field == "" {
				field = "receipt"
			}
			if detail == "" {
				detail = "untrusted receipt"
			}
			return fail(field, detail)
		}
		return fail("receipt", err.Error())
	}
	if receipt.CaptureID == "" {
		return fail("receipt", "receipt carries no capture; a vacancy needs captured evidence")
	}
	return page.Hostname(), nil
}

// commitSeeds applies one fully validated batch atomically under the store
// lock. Bounds mirror reserve/admit per save (wall clock, tool calls, then
// per-operation and total bytes over each per-save response), but every
// breach is detected before the first write, so a failure saves nothing.
func (s *Server) commitSeeds(vacancies []SeedVacancy, hosts []string) ([]musecode.PublicVacancy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(index int, field, detail string) ([]musecode.PublicVacancy, error) {
		return nil, &SeedError{Index: index, Field: field, Detail: detail}
	}
	if s.now().Sub(s.startedAt) > s.bounds.MaxWallClock {
		return fail(0, "wall_clock", "wall-clock bound exceeded; the run stops here")
	}
	if s.toolCalls+len(vacancies) > s.bounds.MaxToolCalls {
		breaching := s.bounds.MaxToolCalls - s.toolCalls
		if breaching < 0 {
			breaching = 0
		}
		if breaching >= len(vacancies) {
			breaching = len(vacancies) - 1
		}
		return fail(breaching, "tool_calls", "tool-call bound exceeded; the run stops here")
	}
	stamped := s.now().UTC().Format(time.RFC3339)
	saved := make([]musecode.PublicVacancy, len(vacancies))
	sizes := make([]int64, len(vacancies))
	for i, v := range vacancies {
		saved[i] = musecode.PublicVacancy{
			VacancyRef:   fmt.Sprintf("vac-%04d", s.vacSeq+i+1),
			SourceHost:   hosts[i],
			PageURL:      v.PageURL,
			EmployerName: v.EmployerName,
			Title:        v.Title,
			LocationText: v.LocationText,
			PostedText:   v.PostedText,
			CapturedAt:   stamped,
			ReceiptRef:   v.ReceiptRef,
		}
		sizes[i] = outputSize(map[string]any{"outcome": "ok", "vacancy": saved[i]})
		if sizes[i] > s.bounds.MaxBytesPerOp {
			return fail(i, "bytes_per_op", "per-operation byte bound exceeded")
		}
	}
	total := s.bytesOut
	for i, size := range sizes {
		if total+size > s.bounds.MaxBytesTotal {
			return fail(i, "bytes_total", "total byte bound exceeded; the run stops here")
		}
		total += size
	}
	for _, vac := range saved {
		s.vacSeq++
		s.vacancies[vac.VacancyRef] = vac
		s.vacOrder = append(s.vacOrder, vac.VacancyRef)
		if parsed, err := url.Parse(vac.PageURL); err == nil && parsed.Hostname() != "" {
			s.vacByURL[vacancyURLKey(parsed)] = vac.VacancyRef
		}
	}
	s.toolCalls += len(saved)
	s.bytesOut = total
	return saved, nil
}
