package researchcontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("%s: invalid JSON", name)
	}
	return raw
}

func TestRunViewFixture(t *testing.T) {
	var v struct {
		RunID          string `json:"runId"`
		State          string `json:"state"`
		Investigations []struct {
			Status string `json:"status"`
		} `json:"investigations"`
		SavedIDs []string `json:"savedIds"`
		Usage    struct {
			Reserved struct {
				Actions int `json:"actions"`
			} `json:"reserved"`
			Unknown bool `json:"unknown"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(loadFixture(t, "run-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.RunID == "" || v.State != "running" {
		t.Fatalf("unexpected run identity %q %q", v.RunID, v.State)
	}
	if len(v.Investigations) != 2 || len(v.SavedIDs) != 1 {
		t.Fatal("run fixture must carry 2 investigations and 1 saved role")
	}
	if v.Usage.Reserved.Actions == 0 || !v.Usage.Unknown {
		t.Fatal("run fixture must show nonzero reserved usage and unknown=true")
	}
}

func TestActivityViewFixture(t *testing.T) {
	var v struct {
		Events []struct {
			EventID string `json:"eventId"`
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		} `json:"events"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(loadFixture(t, "activity-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Events) == 0 || v.NextCursor == "" {
		t.Fatal("activity fixture must carry ordered events and a cursor")
	}
	seen := map[string]bool{}
	for _, e := range v.Events {
		if e.EventID == "" || e.Summary == "" {
			t.Fatal("activity event missing id or summary")
		}
		seen[e.Kind] = true
	}
	if !seen["observation"] || !seen["jev_assessment"] {
		t.Fatal("activity fixture must include a failure observation and a Jev judgment")
	}
}

func TestCaptureViewFixture(t *testing.T) {
	var v []struct {
		CaptureID string `json:"captureId"`
		Extent    struct {
			Bytes      int64  `json:"bytes"`
			Complete   bool   `json:"complete"`
			Truncation string `json:"truncation"`
		} `json:"extent"`
	}
	if err := json.Unmarshal(loadFixture(t, "capture-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 {
		t.Fatalf("capture fixture must carry 2 captures, got %d", len(v))
	}
	if !v[0].Extent.Complete || v[1].Extent.Complete || v[1].Extent.Truncation == "" {
		t.Fatal("capture fixture must pair one complete capture with one explicit truncation")
	}
}

func TestReportViewFixture(t *testing.T) {
	var v struct {
		RunID       string   `json:"runId"`
		Uncertainty []string `json:"uncertainty"`
		Budget      struct {
			Unknown    bool   `json:"unknown"`
			StopReason string `json:"stopReason"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(loadFixture(t, "report-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.RunID == "" || len(v.Uncertainty) == 0 {
		t.Fatal("report fixture must name its run and retain uncertainty")
	}
	if !v.Budget.Unknown || v.Budget.StopReason == "" {
		t.Fatal("report fixture must carry unknown usage and a stop reason")
	}
}

func TestBriefViewFixture(t *testing.T) {
	var v struct {
		ProfileVersion int64  `json:"profileVersion"`
		RubricVersion  string `json:"rubricVersion"`
		CatalogVersion string `json:"catalogVersion"`
		Requirements   []struct {
			ID             string `json:"id"`
			DefinitionHash string `json:"definitionHash"`
		} `json:"requirements"`
		Facts []struct {
			Key string `json:"key"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(loadFixture(t, "brief-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.ProfileVersion != 7 || v.RubricVersion == "" || v.CatalogVersion == "" {
		t.Fatal("brief fixture must carry profile, rubric, and catalog versions")
	}
	if len(v.Requirements) == 0 || v.Requirements[0].DefinitionHash == "" || len(v.Facts) == 0 {
		t.Fatal("brief fixture must retain requirements and facts")
	}
}

func TestCatalogViewFixture(t *testing.T) {
	var v struct {
		CatalogVersion string `json:"catalogVersion"`
		Positive       []struct {
			ID string `json:"id"`
		} `json:"positive"`
		Negative []struct {
			ID string `json:"id"`
		} `json:"negative"`
		MissingInformation []struct {
			ID string `json:"id"`
		} `json:"missingInformation"`
	}
	if err := json.Unmarshal(loadFixture(t, "catalog-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.CatalogVersion == "" || len(v.Positive) == 0 || len(v.Negative) == 0 || len(v.MissingInformation) == 0 {
		t.Fatal("catalog fixture must carry all three reason kinds")
	}
}

func TestFindingViewFixture(t *testing.T) {
	var v struct {
		OpportunityID  string `json:"opportunityId"`
		Group          string `json:"group"`
		AssessmentID   string `json:"assessmentId"`
		CatalogVersion string `json:"catalogVersion"`
		Reasons        []struct {
			ReasonID   string  `json:"reasonId"`
			JevSupport float64 `json:"jevSupport"`
		} `json:"reasons"`
		EvidenceLinks []struct {
			CaptureID string `json:"captureId"`
		} `json:"evidenceLinks"`
		Stale bool `json:"stale"`
	}
	if err := json.Unmarshal(loadFixture(t, "finding-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.OpportunityID == "" || v.Group != "recommended" || v.AssessmentID == "" {
		t.Fatal("finding fixture must name its role, group, and assessment")
	}
	if v.CatalogVersion == "" || len(v.Reasons) == 0 || len(v.EvidenceLinks) == 0 || v.Stale {
		t.Fatal("finding fixture must bind versions, reasons, and evidence without staleness")
	}
}

func TestCheckViewFixture(t *testing.T) {
	var v struct {
		Status         string `json:"status"`
		OpportunityRev int64  `json:"opportunityRevision"`
		QuestionSetSha string `json:"questionSetSha256"`
		Completeness   struct {
			Completeness string `json:"completeness"`
		} `json:"vacancy"`
		Questions []struct {
			Text     string `json:"text"`
			Required string `json:"required"`
		} `json:"questions"`
		Route struct {
			Judgment string `json:"judgment"`
		} `json:"route"`
	}
	if err := json.Unmarshal(loadFixture(t, "check-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != "checked" || v.OpportunityRev != 3 || v.QuestionSetSha == "" {
		t.Fatal("check fixture must pin status, opportunity revision, and question set")
	}
	if len(v.Questions) == 0 || v.Questions[0].Required != "required" {
		t.Fatal("check fixture must carry sourced required questions")
	}
	if v.Route.Judgment != "application_route" || v.Completeness.Completeness != "complete" {
		t.Fatal("check fixture must record route judgment and vacancy completeness")
	}
}

func TestSavedAnswerFixture(t *testing.T) {
	var v struct {
		CurrentVersion int64 `json:"currentVersion"`
		Versions       []struct {
			Version    int64  `json:"version"`
			Text       string `json:"text"`
			TextSha256 string `json:"textSha256"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(loadFixture(t, "saved-answer.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.CurrentVersion != 2 || len(v.Versions) != 2 || v.Versions[1].TextSha256 == "" {
		t.Fatal("answer fixture must carry immutable approved versions with hashes")
	}
}

func TestMatchViewFixture(t *testing.T) {
	var v struct {
		Status  string `json:"status"`
		CheckID string `json:"checkId"`
		Matches []struct {
			QuestionID string `json:"questionId"`
			Choice     struct {
				AnswerID string `json:"answerId"`
				NoneFits bool   `json:"noneFits"`
			} `json:"choice"`
			CandidateSetHash string `json:"candidateSetHash"`
			JevAttemptID     string `json:"jevAttemptId"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(loadFixture(t, "match-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != "matched" || v.CheckID == "" || len(v.Matches) != 2 {
		t.Fatal("match fixture must bind its check with two outcomes")
	}
	if v.Matches[0].Choice.AnswerID == "" || !v.Matches[1].Choice.NoneFits {
		t.Fatal("match fixture must pair a selection with an explicit none-fits")
	}
	for _, m := range v.Matches {
		if m.CandidateSetHash == "" || m.JevAttemptID == "" {
			t.Fatal("match outcomes must carry candidate hash and attempt link")
		}
	}
}

func TestAnswerValuesFixture(t *testing.T) {
	var v struct {
		Values []struct {
			State   string `json:"state"`
			Text    string `json:"text"`
			Version int64  `json:"version"`
		} `json:"values"`
	}
	if err := json.Unmarshal(loadFixture(t, "answer-values.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Values) != 2 {
		t.Fatal("answer values fixture must carry two questions")
	}
	if v.Values[0].State != "answered" || v.Values[0].Text == "" {
		t.Fatal("first value must be an exact answered text")
	}
	if v.Values[1].State != "blank" || v.Values[1].Text != "" || v.Values[1].Version != 1 {
		t.Fatal("second value must be an explicit versioned blank")
	}
}

func TestMaterialViewFixture(t *testing.T) {
	var v struct {
		PackID  string `json:"packId"`
		Version int64  `json:"version"`
		CheckID string `json:"checkId"`
		Answers []struct {
			QuestionID string `json:"questionId"`
		} `json:"answers"`
		Readiness struct {
			Ready bool `json:"ready"`
		} `json:"readiness"`
		Provenance struct {
			Origin string `json:"origin"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(loadFixture(t, "material-view.v1.json"), &v); err != nil {
		t.Fatal(err)
	}
	if v.PackID == "" || v.Version != 1 || v.CheckID == "" {
		t.Fatal("material fixture must bind pack, version, and check")
	}
	if len(v.Answers) == 0 || !v.Readiness.Ready || v.Provenance.Origin != "prepared" {
		t.Fatal("material fixture must pin answers, readiness, and provenance")
	}
}
