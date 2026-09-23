package applicationpacks

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type savedFixture struct{}

func (savedFixture) Opportunity(_ context.Context, id string) (store.Opportunity, error) {
	return store.Opportunity{ID: id, CompanyID: "c", Title: "Saved Platform Role", SourceURL: "https://example.invalid/role", OriginalText: "Build Go services and Linux tools.", Revision: 3}, nil
}
func (savedFixture) Company(context.Context, string) (store.Company, error) {
	return store.Company{Name: "Example Employer"}, nil
}
func (savedFixture) CurrentPreferences(context.Context) (store.Preferences, error) {
	return store.Preferences{Version: 4}, nil
}

func TestPrepareSavedOpportunitySnapshotsRevisions(t *testing.T) {
	typst, err := exec.LookPath("typst")
	if err != nil {
		t.Skip("Typst not installed")
	}
	root, err := filepath.Abs("../../../../../")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := (Renderer{TypstPath: typst, PrivateTempDir: t.TempDir(), Timeout: 10 * time.Second}).PrepareSavedOpportunity(context.Background(), savedFixture{}, root, "saved-1", "https://example.invalid/apply", []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"}, fixture(t).Draft)
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(prepared.ManifestJSON)
	for _, expected := range []string{`"opportunityId":"saved-1"`, `"opportunityRevision":3`, `"profileRevision":4`, `"company":"Example Employer"`} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("missing snapshot %s", expected)
		}
	}
}
