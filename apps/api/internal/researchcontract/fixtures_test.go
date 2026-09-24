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
