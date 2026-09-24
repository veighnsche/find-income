package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func assessmentFixture(t *testing.T) (*store.Store, jevservice.Binding, store.Opportunity, store.EvidenceSource, store.Preferences) {
	return assessmentFixtureWithDeadline(t, time.Hour)
}

func assessmentFixtureWithDeadline(t *testing.T, duration time.Duration) (*store.Store, jevservice.Binding, store.Opportunity, store.EvidenceSource, store.Preferences) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "assessment-owner"}
	company, _, err := s.CreateCompany(ctx, owner, store.CompanyInput{Name: "Assessment Test"})
	if err != nil {
		t.Fatal(err)
	}
	text := "Build Go services for the platform."
	opportunity, changeID, err := s.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.test/job", OriginalText: text, Stage: "new", WorkPattern: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.QualificationInputVersions(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := s.AddEvidenceSource(ctx, owner, store.SourceInput{OpportunityID: opportunity.ID, ExpectedContextVersion: versions.ContextVersion, VacancyChangeID: &changeID})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := s.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "assessment-round", Intent: "Review sourced vacancies", Outcome: "process_input", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"campaign:test"}, Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 5}, Deadline: time.Now().Add(duration).UTC().Round(0)})
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", ProfileVersion: profile.Version, RequestKeyPrefix: "screen-fixture"}, opportunity, source, profile
}

func assessmentServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		answers := map[string]any{}
		for id, q := range request.Questions {
			choice := ""
			switch {
			case id == "scope_0":
				choice = jev.ScopePresent
			case len(id) >= 6 && id[:6] == "scope_":
				choice = jev.ScopeNoRelevantEvidence
			case id == "support_0":
				choice = "span_0"
			case id == "organisation_category":
				for option := range q.Criteria {
					if option != "__uncertain__" {
						choice = option
						break
					}
				}
			default:
				t.Errorf("unexpected question %q", id)
				return
			}
			p := map[string]float64{}
			for option := range q.Criteria {
				p[option] = 0
			}
			p[choice] = 1
			answers[id] = map[string]any{"type": "choice", "choice": choice, "probabilities": p, "confidence": 0.8}
		}
		body, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}})
		_, _ = w.Write(body)
	}))
}

func assessmentService(t *testing.T, s *store.Store, server *httptest.Server) jevservice.Service {
	t.Helper()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-assessment-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = server.URL + "/v1/systemone"
	cfg.Timeout = time.Second
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return jevservice.Service{Store: s, Client: client}
}

func screeningInput(profile store.Preferences, source store.EvidenceSource) jev.ScreeningInput {
	criteria := make([]jev.ScreeningCriterion, 0, len(profile.RoleCriteria))
	for _, c := range profile.RoleCriteria {
		criteria = append(criteria, jev.ScreeningCriterion{ID: c.ID, Label: c.Label, Description: c.Description, Kind: c.Kind, Mode: c.Mode})
	}
	return jev.ScreeningInput{PreferenceVersion: profile.Version, MaxTotalTokens: 200, Criteria: criteria,
		Spans: []jev.ScreeningSpan{{ID: "span-1", SourceID: source.ID, SourceRevision: source.ContentSHA256, SourceKind: "vacancy_snapshot", Excerpt: source.OriginalText}}}
}

func assessmentBinding(binding jevservice.Binding, o store.Opportunity, source store.EvidenceSource, ids []string) store.RoundAssessmentInput {
	return store.RoundAssessmentInput{Actor: binding.Actor, RoundID: binding.RoundID, RoundGeneration: 1, ResourceID: binding.ResourceID,
		OpportunityID: o.ID, OpportunityRevision: o.Revision, SourceID: source.ID, SourceRevision: source.ContentSHA256, ProfileVersion: binding.ProfileVersion, JevAttemptIDs: ids}
}

func TestRoundScreeningAppliesCurrentProposalAndRejectsStale(t *testing.T) {
	ctx := context.Background()
	s, b, o, source, profile := assessmentFixture(t)
	defer s.Close()
	server := assessmentServer(t)
	defer server.Close()
	input := screeningInput(profile, source)
	result, err := assessmentService(t, s, server).RunScreening(ctx, b, input)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, b.RoundID, b.RequestKeyPrefix)
	if err != nil || len(ids) != 2 {
		t.Fatalf("attempt ids %#v %v", ids, err)
	}
	binding := assessmentBinding(b, o, source, ids)
	applied, err := s.ApplyRoundScreening(ctx, binding, input, result)
	if err != nil || applied.Status != "unresolved" || applied.InputSHA256 != result.InputSHA256 {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	current, err := s.CurrentRoundJevAssessment(ctx, o.ID, "screening")
	if err != nil || current.ID != applied.ID {
		t.Fatalf("current: %#v %v", current, err)
	}
	if again, err := s.ApplyRoundScreening(ctx, binding, input, result); err != nil || again.ID != applied.ID {
		t.Fatalf("idempotent apply: %#v %v", again, err)
	}
	changed := binding
	changed.OmittedBytes = 1
	if _, err := s.ApplyRoundScreening(ctx, changed, input, result); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed repeat accepted: %v", err)
	}
	qualification, err := s.Qualification(ctx, o.ID)
	if err != nil || qualification.Current == nil || qualification.Current.Overall == "qualified" {
		t.Fatalf("model proposal became qualification: %#v %v", qualification, err)
	}
	// A changed opportunity revision fences a late apply of the old source.
	title := "Updated title"
	_, _, err = s.PatchOpportunity(ctx, b.Actor, o.ID, store.OpportunityPatch{ExpectedRevision: o.Revision, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyRoundScreening(ctx, binding, input, result)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale write accepted: %v", err)
	}
	_, err = s.CurrentRoundJevAssessment(ctx, o.ID, "screening")
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale current view accepted: %v", err)
	}
}

func TestRoundScreeningRejectsAlteredCurrentCriteria(t *testing.T) {
	ctx := context.Background()
	s, b, o, source, profile := assessmentFixture(t)
	defer s.Close()
	server := assessmentServer(t)
	defer server.Close()
	input := screeningInput(profile, source)
	input.Criteria[0].Description = "An invented requirement"
	result, err := assessmentService(t, s, server).RunScreening(ctx, b, input)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, b.RoundID, b.RequestKeyPrefix)
	if err != nil || len(ids) != 2 {
		t.Fatalf("attempt ids %#v %v", ids, err)
	}
	if _, err := s.ApplyRoundScreening(ctx, assessmentBinding(b, o, source, ids), input, result); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("altered profile criteria accepted: %v", err)
	}
}

func TestRoundScreeningRejectsExpiredApplication(t *testing.T) {
	ctx := context.Background()
	s, b, o, source, profile := assessmentFixtureWithDeadline(t, 2*time.Second)
	defer s.Close()
	server := assessmentServer(t)
	defer server.Close()
	input := screeningInput(profile, source)
	result, err := assessmentService(t, s, server).RunScreening(ctx, b, input)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, b.RoundID, b.RequestKeyPrefix)
	if err != nil || len(ids) != 2 {
		t.Fatalf("attempt ids %#v %v", ids, err)
	}
	round, err := s.Round(ctx, b.RoundID)
	if err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(round.Deadline); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	if _, err := s.ApplyRoundScreening(ctx, assessmentBinding(b, o, source, ids), input, result); !errors.Is(err, store.ErrExpired) {
		t.Fatalf("expired round applied: %v", err)
	}
}

func TestRoundScreeningRejectsIncompleteAndStoppedApplication(t *testing.T) {
	ctx := context.Background()
	s, b, o, source, profile := assessmentFixture(t)
	defer s.Close()
	server := assessmentServer(t)
	defer server.Close()
	input := screeningInput(profile, source)
	result, err := assessmentService(t, s, server).RunScreening(ctx, b, input)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, b.RoundID, b.RequestKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	binding := assessmentBinding(b, o, source, ids)
	binding.OmittedBytes = 7
	partial, err := s.ApplyRoundScreening(ctx, binding, input, result)
	if err != nil || partial.Status != "incomplete_source" {
		t.Fatalf("partial: %#v %v", partial, err)
	}
	if _, err := s.CurrentRoundJevAssessment(ctx, o.ID, "screening"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("incomplete source became current: %v", err)
	}
	if _, _, err := s.StopRound(ctx, b.Actor, b.RoundID); err != nil {
		t.Fatal(err)
	}
	binding.OmittedBytes = 0
	if _, err := s.ApplyRoundScreening(ctx, binding, input, result); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stopped round applied: %v", err)
	}
}

func TestRoundOrganisationAppliesOnlyCurrentCategoryAndSource(t *testing.T) {
	ctx := context.Background()
	s, b, o, source, _ := assessmentFixture(t)
	defer s.Close()
	server := assessmentServer(t)
	defer server.Close()
	set, err := s.CurrentOrganisationCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := jev.OrganisationInput{CategorySetVersion: set.Version, Facts: []jev.OrganisationFact{{ID: "fact-1", SourceID: source.ID, SourceRevision: source.ContentSHA256, SourceKind: "vacancy_snapshot", Excerpt: source.OriginalText}}}
	for _, category := range set.Categories {
		input.Categories = append(input.Categories, jev.OrganisationCategory{ID: category.ID, Description: category.Description})
	}
	b.RequestKeyPrefix = "org-fixture"
	result, err := assessmentService(t, s, server).RunOrganisation(ctx, b, input)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, b.RoundID, b.RequestKeyPrefix)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids %#v %v", ids, err)
	}
	binding := assessmentBinding(b, o, source, ids)
	applied, err := s.ApplyRoundOrganisation(ctx, binding, input, result)
	if err != nil || applied.Status != "selected" {
		t.Fatalf("org apply: %#v %v", applied, err)
	}
	current, err := s.CurrentRoundJevAssessment(ctx, o.ID, "organisation")
	if err != nil || current.ID != applied.ID {
		t.Fatalf("org current: %#v %v", current, err)
	}
	projection, err := s.CurrentRoundOrganisation(ctx, o.ID)
	if err != nil || projection.Assessment.ID != applied.ID || projection.Status != "selected" ||
		projection.CategoryID != result.CategoryID || projection.CategoryDescription == "" {
		t.Fatalf("org projection: %#v %v", projection, err)
	}
	// A swapped category set under the same result is rejected even before an
	// owner category edit changes the current version.
	input.Categories[0].Description = "Invented definition"
	if _, err := s.ApplyRoundOrganisation(ctx, binding, input, result); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("fabricated category accepted: %v", err)
	}
	updated := append([]store.OrganisationCategory(nil), set.Categories...)
	updated[0].Description += " Revised"
	if _, err := s.UpdateOrganisationCategories(ctx, set.Version, updated, b.Actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentRoundOrganisation(ctx, o.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale category proposal exposed: %v", err)
	}
}
