package publicresearch

import (
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// Vacancy returns the saved public vacancy for a vac-* ref.
func (s *Server) Vacancy(ref string) (musecode.PublicVacancy, bool) {
	if s == nil || ref == "" {
		return musecode.PublicVacancy{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vac, ok := s.vacancies[ref]
	return vac, ok
}

// Question returns the saved employer question for a q-* ref.
func (s *Server) Question(ref string) (musecode.PublicQuestion, bool) {
	if s == nil || ref == "" {
		return musecode.PublicQuestion{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.questions[ref]
	return q, ok
}

// SavedVacancyRefs returns saved vacancy refs in save order.
func (s *Server) SavedVacancyRefs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.vacOrder...)
}

// SavedQuestionRefs returns saved question refs in save order.
func (s *Server) SavedQuestionRefs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.qOrder...)
}

// Generation reports the run generation every retrieval presents.
func (s *Server) Generation() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

// RebindGeneration moves the server to a revived run generation. Resume
// calls it once, while no conduction is active, so post-resume retrieval
// presents the live generation instead of failing the authority fence
// stale. Saved refs are untouched: the binding moves, the evidence stays.
func (s *Server) RebindGeneration(generation int64) error {
	if s == nil {
		return errors.New("publicresearch: server required")
	}
	if generation <= 0 {
		return errors.New("publicresearch: positive generation required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation = generation
	return nil
}
