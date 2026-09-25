package materialprep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// stubRewriteTurns is a recording Standard runner for constructor tests.
type stubRewriteTurns struct {
	calls  int
	inputs []musecode.StandardInput
}

func (f *stubRewriteTurns) RunStandard(_ context.Context, input musecode.StandardInput) (StandardResult, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	return StandardResult{}, nil
}

// stubForeignDrafter implements Drafter without the shared Standard runner.
type stubForeignDrafter struct{}

func (stubForeignDrafter) DraftRequiredAnswers(context.Context, DraftRequest) ([]RequiredDraft, error) {
	return nil, nil
}

func TestRewriteRunnerCases(t *testing.T) {
	turns := &stubRewriteTurns{}
	got, err := standardRewriteRunner(&StandardDrafter{Runner: turns})
	if err != nil || got != turns {
		t.Fatalf("shared runner: %v %v", got, err)
	}
	var nilDrafter *StandardDrafter
	for name, draft := range map[string]Drafter{
		"nil interface":   nil,
		"nil concrete":    nilDrafter,
		"nil runner":      &StandardDrafter{},
		"foreign drafter": stubForeignDrafter{},
	} {
		if _, err := standardRewriteRunner(draft); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func rewriteTestBase() store.MaterialVersionView {
	return store.MaterialVersionView{CheckID: "check-1", QuestionSetSHA256: strings.Repeat("a", 64)}
}

func TestParsePriorMaterialTexts(t *testing.T) {
	base := rewriteTestBase()
	set := strings.Repeat("a", 64)
	prepared := `{"role":{},"material":{"checkId":"check-1","questionSetSha256":"` + set +
		`","origin":"prepared","answers":[{"questionId":"q-a","state":"answered","answerVersion":2,"textSha256":"` +
		strings.Repeat("b", 64) + `","text":"Saved answer text."},{"questionId":"q-b","state":"held","answerVersion":0,"textSha256":"` +
		strings.Repeat("c", 64) + `","text":""}]}}`
	rewrite := `{"material":{"checkId":"check-1","questionSetSha256":"` + set +
		`","origin":"rewrite","answers":[{"questionId":"q-a","text":"Rewritten."},{"questionId":"q-b","text":""}]}}`
	edit := `{"material":{"checkId":"check-1","questionSetSha256":"` + set +
		`","origin":"direct_edit","text":"Combined owner text.","answers":[{"questionId":"q-a"},{"questionId":"q-b"}]}}`
	cases := []struct {
		name     string
		manifest string
		wantErr  bool
		origin   string
		texts    map[string]string
		combined string
	}{
		{"prepared", prepared, false, store.MaterialOriginPrepared, map[string]string{"q-a": "Saved answer text.", "q-b": ""}, ""},
		{"rewrite", rewrite, false, store.MaterialOriginRewrite, map[string]string{"q-a": "Rewritten.", "q-b": ""}, ""},
		{"direct edit", edit, false, store.MaterialOriginDirectEdit, map[string]string{"q-a": "", "q-b": ""}, "Combined owner text."},
		{"missing material", `{"role":{}}`, true, "", nil, ""},
		{"malformed", `{nope}`, true, "", nil, ""},
		{"check mismatch", strings.Replace(prepared, "check-1", "check-2", 1), true, "", nil, ""},
		{"question set mismatch", strings.Replace(prepared, set, strings.Repeat("d", 64), 1), true, "", nil, ""},
		{"unknown origin", strings.Replace(prepared, `"prepared"`, `"forged"`, 1), true, "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePriorMaterialTexts([]byte(tc.manifest), base)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("tampered manifest accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.origin != tc.origin || got.combined != tc.combined || len(got.byQuestion) != len(tc.texts) {
				t.Fatalf("parsed: %+v", got)
			}
			for id, want := range tc.texts {
				if got.byQuestion[id] != want {
					t.Fatalf("text %q: %q", id, got.byQuestion[id])
				}
			}
		})
	}
}

func rewriteTestQuestions() []store.CheckQuestionView {
	return []store.CheckQuestionView{{ID: "q-a", Ordinal: 0}, {ID: "q-b", Ordinal: 1}}
}

func TestCheckModelRewrite(t *testing.T) {
	questions := rewriteTestQuestions()
	long := strings.Repeat("x", 20001)
	cases := []struct {
		name     string
		messages []string
		wantErr  bool
	}{
		{"full coverage", []string{`{"texts":[{"questionId":"q-a","text":"Alpha."},{"questionId":"q-b","text":""}]}`}, false},
		{"shuffled order", []string{`{"texts":[{"questionId":"q-b","text":"Beta."},{"questionId":"q-a","text":"Alpha."}]}`}, false},
		{"fenced", []string{"```json\n" + `{"texts":[{"questionId":"q-a","text":"A"},{"questionId":"q-b","text":"B"}]}` + "\n```"}, false},
		{"missing", []string{`{"texts":[{"questionId":"q-a","text":"A"}]}`}, true},
		{"extra", []string{`{"texts":[{"questionId":"q-a","text":"A"},{"questionId":"q-b","text":"B"},{"questionId":"q-c","text":"C"}]}`}, true},
		{"duplicate", []string{`{"texts":[{"questionId":"q-a","text":"A"},{"questionId":"q-a","text":"B"}]}`}, true},
		{"empty id", []string{`{"texts":[{"questionId":"","text":"A"},{"questionId":"q-b","text":"B"}]}`}, true},
		{"overlong", []string{`{"texts":[{"questionId":"q-a","text":"` + long + `"},{"questionId":"q-b","text":"B"}]}`}, true},
		{"nul", []string{`{"texts":[{"questionId":"q-a","text":"a` + "\\u0000" + `b"},{"questionId":"q-b","text":"B"}]}`}, true},
		{"malformed", []string{`{nope}`}, true},
		{"trailing", []string{`{"texts":[{"questionId":"q-a","text":"A"},{"questionId":"q-b","text":"B"}]} trailing`}, true},
		{"unknown field", []string{`{"texts":[],"extra":1}`}, true},
		{"unknown entry field", []string{`{"texts":[{"questionId":"q-a","text":"A","note":"x"},{"questionId":"q-b","text":"B"}]}`}, true},
		{"empty", nil, true},
		{"blank", []string{"  "}, true},
		{"unclosed fence", []string{"```json\n" + `{"texts":[]}`}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkModelRewrite(questions, tc.messages)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("bad output accepted")
				}
				if errors.Is(err, store.ErrInvalid) {
					t.Fatalf("model failure misclassified as client-invalid: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].QuestionID != "q-a" || got[1].QuestionID != "q-b" {
				t.Fatalf("coverage order: %+v", got)
			}
			for _, entry := range got {
				if entry.TextSHA256 != textSHA256(entry.Text) {
					t.Fatalf("sha mismatch for %q", entry.QuestionID)
				}
			}
		})
	}
}

func TestTruncateBytes(t *testing.T) {
	if got := truncateBytes("hello", 1200); got != "hello" {
		t.Fatalf("short: %q", got)
	}
	if got := truncateBytes("hello", 3); got != "hel" {
		t.Fatalf("ascii: %q", got)
	}
	// "é" is two bytes: cutting at 2 must back off, not split.
	if got := truncateBytes("aé", 2); got != "a" || !utf8.ValidString(got) {
		t.Fatalf("rune boundary: %q", got)
	}
	// Four-byte emoji at the cut backs off up to three bytes.
	if got := truncateBytes("ab🎯", 4); got != "ab" || !utf8.ValidString(got) {
		t.Fatalf("emoji boundary: %q", got)
	}
	if got := truncateBytes("ab🎯", 6); got != "ab🎯" {
		t.Fatalf("exact fit: %q", got)
	}
	if got := truncateBytes("abc", 0); got != "" {
		t.Fatalf("zero max: %q", got)
	}
}

func TestHeldUnknowns(t *testing.T) {
	questions := []store.CheckQuestionView{
		{ID: "q-a", Ordinal: 0, Text: "First?"},
		{ID: "q-b", Ordinal: 1, Text: "Second?"},
		{ID: "q-c", Ordinal: 2, Text: "Third?"},
	}
	got, err := heldUnknowns(questions, []string{"q-c", "q-a"})
	if err != nil || len(got) != 2 {
		t.Fatalf("held: %+v %v", got, err)
	}
	if !strings.Contains(got[0], "First?") || !strings.Contains(got[1], "Third?") {
		t.Fatalf("held order: %+v", got)
	}
	if _, err := heldUnknowns(questions, []string{"q-unknown"}); err == nil {
		t.Fatalf("unknown held id accepted")
	}
	if got, err := heldUnknowns(questions, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty held: %+v %v", got, err)
	}
}

func TestRewritePromptComposition(t *testing.T) {
	questions := []store.CheckQuestionView{
		{ID: "q-a", Text: "Why this role?", Required: store.CheckRequired},
		{ID: "q-b", Text: "Anything else?", Required: store.CheckOptional},
	}
	current := map[string]string{"q-a": "Current answer."}
	answered := []AnsweredFact{{QuestionID: "q-a", Question: "Why this role?", Text: "Current answer.", AnswerVersion: 1}}
	profile := store.Preferences{PreferredLocation: "Amsterdam", SalaryCurrency: "EUR", Timezone: "Europe/Amsterdam"}
	prompt := rewritePrompt("Tighten it.", questions, current, "Combined context.",
		answered, nil, []applicationpacks.Source{{ID: "cv", Name: "CV", Body: "Go services.", Approved: true}}, profile)
	for _, want := range []string{"Tighten it.", "q-a", "q-b",
		"Why this role?", "Current answer.", "Combined context.", "Go services.", "Amsterdam"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
	blank := rewritePrompt("", questions, nil, "", nil, nil, nil, store.Preferences{})
	if !strings.Contains(blank, "(none") || !strings.Contains(blank, "(blank)") || !strings.Contains(blank, "(blank)") {
		t.Fatalf("blank prompt: %q", blank[:200])
	}
}
