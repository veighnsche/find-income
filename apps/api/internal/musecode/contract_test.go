package musecode

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func jsonKeys(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestContributorDTOsExposeNoPrivateFields(t *testing.T) {
	frozen := map[string]struct {
		value any
		keys  []string
	}{
		"criteria": {PublicCriteria{RoleKeywords: []string{"a"}, RegionText: "b", SkillKeywords: []string{"c"}},
			[]string{"region_text", "role_keywords", "skill_keywords"}},
		"vacancy": {PublicVacancy{VacancyRef: "v", SourceHost: "h", PageURL: "u", EmployerName: "e",
			Title: "t", LocationText: "l", PostedText: "p", CapturedAt: "c", ReceiptRef: "r"},
			[]string{"captured_at", "employer_name", "location_text", "page_url", "posted_text",
				"receipt_ref", "source_host", "title", "vacancy_ref", "work_pattern"}},
		"question": {PublicQuestion{QuestionRef: "q", VacancyRef: "v", PromptText: "p", Required: true, SourceURL: "u"},
			[]string{"prompt_text", "question_ref", "required", "source_url", "vacancy_ref"}},
	}
	// Any private-denied key must fail even if the frozen set above is edited.
	denied := []string{"owner", "owner_name", "email", "cv", "cv_text", "profile", "answer",
		"approved_answer", "jev", "jev_assessment", "jev_reason", "private", "secret", "token",
		"password", "session_history", "transcript"}
	for name, dto := range frozen {
		keys := jsonKeys(t, dto.value)
		if !reflect.DeepEqual(keys, dto.keys) {
			t.Errorf("%s DTO keys = %v, want frozen %v", name, keys, dto.keys)
		}
		present := map[string]bool{}
		for _, key := range keys {
			present[key] = true
		}
		for _, bad := range denied {
			if present[bad] {
				t.Errorf("%s DTO exposes denied key %q", name, bad)
			}
		}
	}
}

func TestContributorToolAllowlistFailsClosed(t *testing.T) {
	for _, name := range []string{"web_search", "web_fetch",
		"public_search", "public_fetch", "public_save_vacancy", "public_save_question", "public_list_saved"} {
		if !ContributorToolAllowed(name) {
			t.Errorf("allowlisted tool %q rejected", name)
		}
	}
	for _, name := range []string{"", "context_read", "exec", "shell", "read_memory", "PUBLIC_SEARCH",
		"public_search_extra", "browser_open", "write", "subagent", "delegate", "request_user_input",
		"WEB_SEARCH", "tool.web_search", "mcp__find_income_public__public_search"} {
		if ContributorToolAllowed(name) {
			t.Errorf("non-allowlisted tool %q admitted", name)
		}
	}
}

func readyFacts() Facts {
	return Facts{CLIPath: "/opt/muse/bin/muse", CLIReportVersion: ObservedCLIFullVersion,
		EffectiveModel: "contributor-subscription", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func TestCheckFailsClosedWithoutAICall(t *testing.T) {
	// Check takes hand-supplied facts and has no executor, subprocess or
	// network path, so no table entry below can invoke a model.
	cases := []struct {
		name  string
		tier  Tier
		facts Facts
		code  string
	}{
		{"unknown tier", Tier("ultra"), readyFacts(), CodeNotConfigured},
		{"no CLI path", TierContributor, Facts{}, CodeNotConfigured},
		{"empty version", TierContributor, Facts{CLIPath: "/x"}, CodeVersionMismatch},
		{"wrong version", TierContributor, Facts{CLIPath: "/x", CLIReportVersion: "1.2.0"}, CodeVersionMismatch},
		{"superseded pin", TierContributor, Facts{CLIPath: "/x", CLIReportVersion: "1.3.0"}, CodeVersionMismatch},
		{"lane unverified", TierContributor, Facts{CLIPath: "/x", CLIReportVersion: "1.4.0"}, CodeLaneUnverified},
		{"protocol unverified", TierContributor, func() Facts {
			f := readyFacts()
			f.SessionProtocolProved = false
			return f
		}(), CodeProtocolUnverified},
		{"workspace unverified", TierStandard, func() Facts {
			f := readyFacts()
			f.WorkspaceIsolatedProved = false
			return f
		}(), CodeWorkspaceUnverified},
	}
	for _, tc := range cases {
		status := Check(tc.tier, tc.facts)
		if status.Available || status.Code != tc.code {
			t.Errorf("%s: got available=%v code=%q, want code=%q", tc.name, status.Available, status.Code, tc.code)
		}
	}
	for _, tier := range []Tier{TierContributor, TierStandard} {
		if status := Check(tier, readyFacts()); !status.Available || status.Code != CodeReady {
			t.Errorf("%s ready facts: got %+v, want available", tier, status)
		}
	}
	for _, version := range []string{"1.4.0", "1.4.0-R4161.1", "1.4.0+local"} {
		facts := readyFacts()
		facts.CLIReportVersion = version
		if status := Check(TierContributor, facts); !status.Available {
			t.Errorf("version %q rejected: %+v", version, status)
		}
	}
}

func TestNewSessionEnforcesCheckBeforeInputAndSeparation(t *testing.T) {
	bounds := DefaultBounds()
	ready := Check(TierContributor, readyFacts())
	if _, err := NewSession(Status{Tier: TierContributor, Code: CodeLaneUnverified}, "/tmp/a", bounds, nil); err == nil {
		t.Error("unavailable status admitted a session")
	}
	if _, err := NewSession(ready, "relative/path", bounds, nil); err == nil {
		t.Error("relative workspace admitted")
	}
	if _, err := NewSession(ready, "/tmp/c", Bounds{}, nil); err == nil {
		t.Error("zero bounds admitted")
	}
	for _, peers := range [][]string{{"/tmp/c"}, {"/tmp/c/sub"}, {"/tmp"}} {
		if _, err := NewSession(ready, "/tmp/c", bounds, peers); err == nil {
			t.Errorf("overlapping peer workspace %v admitted", peers)
		}
	}
	contrib, err := NewSession(ready, "/tmp/c", bounds, []string{"/tmp/s"})
	if err != nil {
		t.Fatal(err)
	}
	if !contrib.Public || contrib.Tier != TierContributor {
		t.Errorf("contributor spec = %+v, want public contributor", contrib)
	}
	standard, err := NewSession(Check(TierStandard, readyFacts()), "/tmp/s", bounds, []string{"/tmp/c"})
	if err != nil {
		t.Fatal(err)
	}
	if standard.Public || standard.Tier != TierStandard {
		t.Errorf("standard spec = %+v, want private standard", standard)
	}
}

func TestDefaultBoundsAreFinite(t *testing.T) {
	if err := DefaultBounds().Validate(); err != nil {
		t.Fatalf("default bounds invalid: %v", err)
	}
	bad := DefaultBounds()
	bad.MaxBytesPerOp = bad.MaxBytesTotal + 1
	if err := bad.Validate(); err == nil {
		t.Error("per-operation bytes above total admitted")
	}
}
