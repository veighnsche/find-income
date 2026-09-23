package interviewprep

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

func interviewFixture() Input {
	invitation := "Interview for Platform Engineer on 2026-10-01 at 10:00 +02:00 by video at https://meet.example.test/one. We will discuss Go services."
	role := "Maintain Go services and Linux developer tooling. The role works with a small engineering team."
	employer := "Fixture Employer develops internal engineering tools for its customers."
	career := "Personal project: a Go service manages local environments. This is personal project work, not paid employment."
	quote := func(sourceID, excerpt string) applicationpacks.Citation {
		return applicationpacks.Citation{SourceID: sourceID, Excerpt: excerpt}
	}
	return Input{InterviewID: "interview-one", OpportunityID: "role-one", RoleTitle: "Platform Engineer", EmployerName: "Fixture Employer",
		Context: []ContextSource{
			{ID: "invite", Kind: "invitation", Revision: "owner-paste-1", SHA256: digest(invitation), Body: invitation},
			{ID: "role", Kind: "role", Revision: "2", SHA256: digest(role), Body: role},
			{ID: "employer", Kind: "employer", Revision: "1", SHA256: digest(employer), Body: employer},
		},
		CareerSources: []applicationpacks.Source{{ID: "career", Name: "approved-career.md", SHA256: digest(career), Approved: true, Body: career}},
		Draft: Draft{
			Focus:     []FocusAlternative{{ID: "go-service", Why: CitedText{Text: "Discuss a personal Go service project against the role's service duties.", Citations: []applicationpacks.Citation{quote("role", "Maintain Go services"), quote("career", "Personal project: a Go service manages local environments.")}}}},
			Questions: []Question{{Text: "How does the team review Go service changes?", Why: CitedText{Text: "The saved role mentions a small engineering team.", Citations: []applicationpacks.Citation{quote("role", "The role works with a small engineering team.")}}}},
			Examples: []ExampleOutline{{Title: "Local environment service", ExperienceKind: "personal_project", ContextBasis: quote("career", "This is personal project work, not paid employment."),
				Situation:     CitedText{Text: "A personal environment project needed service support.", Citations: []applicationpacks.Citation{quote("career", "Personal project: a Go service manages local environments.")}},
				Action:        CitedText{Text: "Built a Go service for local environments.", Citations: []applicationpacks.Citation{quote("career", "a Go service manages local environments")}},
				UnknownResult: "No measured result is present in the approved source."}},
			ScheduleClaims: []ScheduleClaim{{StartRFC3339: "2026-10-01T10:00:00+02:00", Mode: "video", Venue: "https://meet.example.test/one",
				Citation: quote("invite", "2026-10-01 at 10:00 +02:00 by video at https://meet.example.test/one")}},
			Unknowns: []string{"Interviewer names and evaluation format are not stated."},
		}}
}

func TestPrepareGroundedInterviewAndFocusOptions(t *testing.T) {
	brief, err := Prepare(interviewFixture())
	if err != nil || brief.InputSHA256 == "" || len(brief.Input.Draft.ScheduleClaims) != 1 {
		t.Fatalf("brief=%+v err=%v", brief, err)
	}
	focus, err := brief.FocusInput(1000)
	if err != nil || len(focus.Candidates) != 1 || len(focus.Evidence) != 2 || len(focus.Context) != 3 ||
		focus.Context[0].Body != brief.Input.Context[0].Body || focus.Candidates[0].ID != "go-service" {
		t.Fatalf("focus input=%+v err=%v", focus, err)
	}
	if focus.Evidence[0].SourceSHA256 == "" || focus.Evidence[1].Excerpt == "" {
		t.Fatal("focus lost source provenance")
	}
	selectionSHA, err := jev.InterviewFocusInputDigest(focus)
	if err != nil {
		t.Fatal(err)
	}
	selection := jev.InterviewFocusResult{Disposition: jev.InterviewFocusSelected, SelectedID: "go-service", InputSHA256: selectionSHA,
		RequestSnapshot: json.RawMessage(`{"state":"synthetic"}`), ProviderResult: jev.Result{Answers: map[string]jev.Answer{
			"interview_focus": {Type: "choice", Choice: &jev.ChoiceAnswer{Choice: "go-service"}},
		}}}
	prepared, err := brief.BindFocusSelection(selection, 1000)
	if err != nil || prepared.Focus == nil || prepared.Focus.ID != "go-service" {
		t.Fatalf("bound focus=%+v err=%v", prepared.Focus, err)
	}
	selection.InputSHA256 = strings.Repeat("0", 64)
	if _, err := brief.BindFocusSelection(selection, 1000); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale Jev choice applied: %v", err)
	}
	brief.Input.Draft.Focus[0].Why.Text = "Changed after validation"
	if _, err := brief.FocusInput(1000); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutated brief accepted: %v", err)
	}
}

func TestPrepareRetainsMissingAndMultipleScheduleClaimsWithoutAdjudication(t *testing.T) {
	input := interviewFixture()
	input.Draft.ScheduleClaims = nil
	brief, err := Prepare(input)
	if err != nil || len(brief.Input.Draft.ScheduleClaims) != 0 {
		t.Fatalf("missing schedule claims=%+v err=%v", brief.Input.Draft.ScheduleClaims, err)
	}
	input = interviewFixture()
	extra := "A later invitation says 2026-10-02 at 11:00 +02:00 by video at https://meet.example.test/two."
	input.Context = append(input.Context, ContextSource{ID: "invite-later", Kind: "invitation", Revision: "owner-paste-2", SHA256: digest(extra), Body: extra})
	input.Draft.ScheduleClaims = append(input.Draft.ScheduleClaims, ScheduleClaim{StartRFC3339: "2026-10-02T11:00:00+02:00", Mode: "video", Venue: "https://meet.example.test/two",
		Citation: applicationpacks.Citation{SourceID: "invite-later", Excerpt: "2026-10-02 at 11:00 +02:00 by video at https://meet.example.test/two"}})
	brief, err = Prepare(input)
	if err != nil || len(brief.Input.Draft.ScheduleClaims) != 2 || brief.Input.Draft.ScheduleClaims[1].Citation.SourceID != "invite-later" {
		t.Fatalf("multiple attributed schedule claims=%+v err=%v", brief.Input.Draft.ScheduleClaims, err)
	}
}

func TestPrepareOneCompleteOwnerInputWithoutCareerExample(t *testing.T) {
	text := "I have an interview for a platform role at Fixture Employer. They will ask about Go services and team collaboration. The time was not supplied."
	input := Input{InterviewID: "interview-owner-one", Context: []ContextSource{{ID: "owner-paste", Kind: "owner_input", Revision: "paste-1", SHA256: digest(text), Body: text}},
		Draft: Draft{Focus: []FocusAlternative{{ID: "team-work", Why: CitedText{Text: "Prepare for the stated team-collaboration topic.", Citations: []applicationpacks.Citation{{SourceID: "owner-paste", Excerpt: "team collaboration"}}}}},
			Questions: []Question{{Text: "How does this team collaborate on Go services?", Why: CitedText{Text: "The owner-supplied context names Go services.", Citations: []applicationpacks.Citation{{SourceID: "owner-paste", Excerpt: "Go services"}}}}},
			Unknowns:  []string{"No verified career example or interview time was supplied."}}}
	brief, err := Prepare(input)
	if err != nil || len(brief.Input.Draft.Examples) != 0 {
		t.Fatalf("single-input brief=%+v err=%v", brief, err)
	}
	focus, err := brief.FocusInput(1000)
	if err != nil || len(focus.Context) != 1 || focus.Context[0].Body != text || focus.Role != "" || focus.Employer != "" {
		t.Fatalf("single-input Jev context=%+v err=%v", focus, err)
	}
	input.Draft.Unknowns = nil
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing example without explicit unknown accepted: %v", err)
	}
}

func TestPrepareRejectsOversizedCompleteContextRatherThanDroppingIt(t *testing.T) {
	input := interviewFixture()
	large := strings.Repeat("x", 30000)
	input.Context = append(input.Context, ContextSource{ID: "extra", Kind: "owner_input", Revision: "paste-2", SHA256: digest(large), Body: large})
	if _, err := Prepare(input); !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("oversized context was not reported: %v", err)
	}
	input = interviewFixture()
	input.Context[0].Body = strings.Repeat("x", 30001)
	input.Context[0].SHA256 = digest(input.Context[0].Body)
	if _, err := Prepare(input); !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("oversized single source was not reported: %v", err)
	}
}

func TestPrepareRejectsUnsupportedClaimAndEmploymentConfusion(t *testing.T) {
	input := interviewFixture()
	input.Draft.Examples[0].ContextBasis = applicationpacks.Citation{SourceID: "role", Excerpt: "Maintain Go services"}
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("role text used as experience basis: %v", err)
	}
	input = interviewFixture()
	input.Draft.Examples[0].ExperienceKind = "employment"
	input.Draft.Examples[0].ContextBasis.Excerpt = "Imaginary paid employment"
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invented employment basis accepted: %v", err)
	}
	input = interviewFixture()
	input.Draft.Questions[0].Why.Citations = nil
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncited rationale accepted: %v", err)
	}
	input = interviewFixture()
	input.CareerSources[0].Body += strings.Repeat("x", 1)
	if _, err := Prepare(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed approved source accepted: %v", err)
	}
}

func TestDebriefRetainsAttributedOwnerNotes(t *testing.T) {
	notes := "We discussed the personal Go project. I could not answer the deployment question. The interviewer promised to send next steps."
	input := DebriefInput{InterviewID: "interview-one", OwnerNotes: notes,
		Observations: []DebriefObservation{{Kind: "unclear", Detail: CitedText{Text: "Deployment answer needs review.", Citations: []applicationpacks.Citation{OwnerNoteCitation("I could not answer the deployment question.")}}}},
		Unknowns:     []string{"No hiring decision or next-step date was supplied."}}
	debrief, err := ValidateDebrief(input)
	if err != nil || debrief.Attribution != "owner_reported" || debrief.Input.OwnerNotes != notes || debrief.InputSHA256 == "" {
		t.Fatalf("debrief=%+v err=%v", debrief, err)
	}
	input.Observations[0].Detail.Citations[0].Excerpt = "The employer offered me the role."
	if _, err := ValidateDebrief(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported owner-note observation accepted: %v", err)
	}
}
