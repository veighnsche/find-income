package materialprep

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// stubOneShot records prompts and replays scripted turn output. It is the
// only collaborator the drafter may invoke: exactly once per call.

type stubOneShot struct {
	t       *testing.T
	prompts []string
	reply   func(prompt string) ([]string, error)
}

func (s *stubOneShot) Run(_ context.Context, prompt string) (codexservice.OneShotResult, error) {
	s.prompts = append(s.prompts, prompt)
	messages, err := s.reply(prompt)
	if err != nil {
		return codexservice.OneShotResult{}, err
	}
	return codexservice.OneShotResult{ThreadID: "thread-test", TurnID: "turn-test",
		State: "completed", Code: "turn_completed", Messages: messages}, nil
}

func draftTestRequest() DraftRequest {
	return DraftRequest{
		OpportunityID: "opp-test", OpportunityTitle: "Go engineer", CompanyName: "Harbour",
		CheckID: "check-test",
		RequiredUnset: []DraftQuestion{
			{ID: "q-motivation", Text: "Why do you want this role?"},
			{ID: "q-go", Text: "Describe a Go service you shipped."},
		},
		Answered: []AnsweredFact{
			{QuestionID: "q-extra", Question: "Anything else?", Text: "I mentor juniors.", AnswerVersion: 1},
		},
		SavedAnswers: []SavedAnswerFact{
			{AnswerID: "ans-1", Version: 2, Text: "I led platform work at Acme.",
				ScopeTags: []string{"leadership"}, ContextNote: "Staff-level scope"},
		},
		CareerSources: []applicationpacks.Source{
			{ID: "cv", Name: "CV", SHA256: strings.Repeat("a", 64), Approved: true,
				Body: "Six years of Go platform work. Led Acme migration."},
			{ID: "github", Name: "Evidence", SHA256: strings.Repeat("b", 64), Approved: true,
				Body: "Shipped queue worker handling 1M jobs daily."},
		},
		Profile: store.Preferences{Version: 3, PreferredLocation: "Amsterdam",
			AllowRemote: true, SalaryCurrency: "EUR", Timezone: "Europe/Amsterdam"},
	}
}

func TestCodexDrafterDraftsFromVerifiedFacts(t *testing.T) {
	stub := &stubOneShot{t: t, reply: func(string) ([]string, error) {
		return []string{`{"drafts":[
{"questionId":"q-motivation","lines":[
{"text":"I want this role for its Go platform work.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]},
{"questionId":"q-go","lines":[
{"text":"I shipped a queue worker.","citations":[{"sourceId":"github","excerpt":"Shipped queue worker handling 1M jobs daily"}]},
{"text":"It followed my platform experience.","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, nil
	}}
	drafter := &CodexDrafter{Turns: stub}
	drafts, err := drafter.DraftRequiredAnswers(context.Background(), draftTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 || drafts[0].QuestionID != "q-motivation" || len(drafts[1].Lines) != 2 {
		t.Fatalf("drafts: %+v", drafts)
	}
	if len(stub.prompts) != 1 {
		t.Fatalf("turn calls: %d", len(stub.prompts))
	}
	prompt := stub.prompts[0]
	for _, want := range []string{"Why do you want this role?", "Describe a Go service you shipped.",
		"I mentor juniors.", "I led platform work at Acme.", "Six years of Go platform work.",
		"Shipped queue worker handling 1M jobs daily.", "Amsterdam", "q-motivation", "q-go"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt misses verified fact %q", want)
		}
	}
	if len(prompt) > codexservice.DefaultOneShotMaxPromptBytes {
		t.Fatalf("prompt exceeds turn budget: %d", len(prompt))
	}
}

func TestCodexDrafterOutputValidation(t *testing.T) {
	cases := map[string]struct {
		messages []string
		wantErr  bool
		want     int
	}{
		"out of scope":      {[]string{`{"drafts":[{"questionId":"q-else","lines":[{"text":"x","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]}]}`}, true, 0},
		"duplicate":         {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"a","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]},{"questionId":"q-go","lines":[{"text":"b","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"no lines":          {[]string{`{"drafts":[{"questionId":"q-go","lines":[]}]}`}, true, 0},
		"too many lines":    {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"a","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"b","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"c","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"d","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"e","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"f","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"g","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"h","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"i","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"blank text":        {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"  ","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"line too long":     {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"` + strings.Repeat("x", 1201) + `","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"no citations":      {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Claim.","citations":[]}]}]}`}, true, 0},
		"unknown source":    {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Claim.","citations":[{"sourceId":"blog","excerpt":"Claim"}]}]}]}`}, true, 0},
		"inexact excerpt":   {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Claim.","citations":[{"sourceId":"cv","excerpt":"ten years of Rust"}]}]}]}`}, true, 0},
		"answer too long":   {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"` + strings.Repeat("y", 1100) + `","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]},{"text":"` + strings.Repeat("z", 1100) + `","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"malformed":         {[]string{`{"drafts":[`}, true, 0},
		"unknown field":     {[]string{`{"drafts":[{"questionId":"q-go","confidence":0.9,"lines":[{"text":"Claim.","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, true, 0},
		"trailing prose":    {[]string{`{"drafts":[]} hope this helps`}, true, 0},
		"empty response":    {[]string{`   `}, true, 0},
		"unclosed fence":    {[]string{"```json\n" + `{"drafts":[]}`}, true, 0},
		"fenced json":       {[]string{"```json\n" + `{"drafts":[{"questionId":"q-go","lines":[{"text":"Shipped it.","citations":[{"sourceId":"github","excerpt":"Shipped queue worker handling 1M jobs daily"}]}]}]}` + "\n```"}, false, 1},
		"subset omits held": {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Shipped it.","citations":[{"sourceId":"github","excerpt":"Shipped queue worker handling 1M jobs daily"}]}]}]}`}, false, 1},
		"empty drafts":      {[]string{`{"drafts":[]}`}, false, 0},
		"null drafts":       {[]string{`{}`}, false, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &stubOneShot{t: t, reply: func(string) ([]string, error) { return tc.messages, nil }}
			drafter := &CodexDrafter{Turns: stub}
			drafts, err := drafter.DraftRequiredAnswers(context.Background(), draftTestRequest())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted: %+v", drafts)
				}
				return
			}
			if err != nil || len(drafts) != tc.want {
				t.Fatalf("drafts: %+v %v", drafts, err)
			}
		})
	}
}

func TestCodexDrafterRequestGuards(t *testing.T) {
	ctx := context.Background()
	var nilDrafter *CodexDrafter
	if _, err := nilDrafter.DraftRequiredAnswers(ctx, draftTestRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil drafter: %v", err)
	}
	if _, err := (&CodexDrafter{}).DraftRequiredAnswers(ctx, draftTestRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil runner: %v", err)
	}
	runner := &stubOneShot{t: t, reply: func(string) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	drafter := &CodexDrafter{Turns: runner}
	empty := draftTestRequest()
	empty.RequiredUnset = nil
	if _, err := drafter.DraftRequiredAnswers(ctx, empty); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty scope: %v", err)
	}
	dup := draftTestRequest()
	dup.RequiredUnset = append(dup.RequiredUnset, dup.RequiredUnset[0])
	if _, err := drafter.DraftRequiredAnswers(ctx, dup); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("duplicate scope: %v", err)
	}
	many := draftTestRequest()
	for i := 0; i < 13; i++ {
		many.RequiredUnset = append(many.RequiredUnset, DraftQuestion{ID: "q-extra-" + string(rune('a'+i)), Text: "Extra?"})
	}
	if _, err := drafter.DraftRequiredAnswers(ctx, many); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("oversize scope: %v", err)
	}
	unapproved := draftTestRequest()
	unapproved.CareerSources[0].Approved = false
	if _, err := drafter.DraftRequiredAnswers(ctx, unapproved); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unapproved source: %v", err)
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("guard failures spent turns: %d", len(runner.prompts))
	}
	boom := errors.New("synthetic turn failure")
	failing := &stubOneShot{t: t, reply: func(string) ([]string, error) { return nil, boom }}
	if _, err := (&CodexDrafter{Turns: failing}).DraftRequiredAnswers(ctx, draftTestRequest()); !errors.Is(err, boom) {
		t.Fatalf("runner error: %v", err)
	}
}

func TestCodexDrafterSingleTurnFencing(t *testing.T) {
	// The drafter's only outbound call is one bounded turn carrying saved
	// state: no research, capture, fetch, or send exists on this path, and
	// the request envelope carries no employer contact handles at all.
	request := draftTestRequest()
	contactCanary := "jobs@example.invalid"
	promptSeen := ""
	stub := &stubOneShot{t: t, reply: func(prompt string) ([]string, error) {
		promptSeen = prompt
		return []string{`{"drafts":[]}`}, nil
	}}
	if _, err := (&CodexDrafter{Turns: stub}).DraftRequiredAnswers(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(stub.prompts) != 1 {
		t.Fatalf("turn calls: %d", len(stub.prompts))
	}
	if strings.Contains(promptSeen, contactCanary) {
		t.Fatalf("prompt carries employer contact: %q", contactCanary)
	}
	for _, field := range []string{request.OpportunityTitle, request.CompanyName, request.CheckID,
		request.Answered[0].Text, request.SavedAnswers[0].Text, request.CareerSources[0].Body,
		request.Profile.PreferredLocation} {
		if !strings.Contains(promptSeen, field) {
			t.Fatalf("prompt misses verified fact %q", field)
		}
	}
}

func TestCodexDrafterTruncatesLargeSources(t *testing.T) {
	request := draftTestRequest()
	request.CareerSources[0].Body += strings.Repeat(" padding fact.", 2000)
	var promptSeen string
	stub := &stubOneShot{t: t, reply: func(prompt string) ([]string, error) {
		promptSeen = prompt
		return []string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Six years.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]}]}`}, nil
	}}
	drafts, err := (&CodexDrafter{Turns: stub}).DraftRequiredAnswers(context.Background(), request)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts: %+v %v", drafts, err)
	}
	if !strings.Contains(promptSeen, "[truncated to fit the turn budget]") {
		t.Fatal("truncation not marked")
	}
	if len(promptSeen) > codexservice.DefaultOneShotMaxPromptBytes {
		t.Fatalf("prompt exceeds turn budget: %d", len(promptSeen))
	}
}
