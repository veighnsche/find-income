package publicresearch

import "github.com/veighnsche/find-income-dashboard/api/internal/musecode"

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
