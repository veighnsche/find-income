package materialprep_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestExportArtifactPreservesIdentity(t *testing.T) {
	view := store.ArtifactView{ID: "art-1", OpportunityID: "opp-1", Type: store.ArtifactCV,
		Version: 2, Content: "Go engineer, six years.",
		Basis: store.ArtifactBasis{CheckID: "check-1"}}
	export := materialprep.ExportArtifact(view, "Backend Engineer", "Harbour Systems")
	if export.Filename != "harbour-systems-backend-engineer-cv-v2.md" {
		t.Fatalf("filename: %q", export.Filename)
	}
	if export.MediaType != "text/markdown; charset=utf-8" {
		t.Fatalf("media type: %q", export.MediaType)
	}
	sum := sha256.Sum256([]byte("Go engineer, six years."))
	checksum := hex.EncodeToString(sum[:])
	if export.ContentSHA256 != checksum {
		t.Fatalf("checksum: %q", export.ContentSHA256)
	}
	for _, want := range []string{"art-1", "artifact-version: 2", checksum, "basis-check: check-1", "Go engineer, six years."} {
		if !strings.Contains(export.Body, want) {
			t.Fatalf("body misses %q", want)
		}
	}
	subject := store.ArtifactView{ID: "art-2", Type: store.ArtifactEmailSubject, Version: 1, Content: "Application: Go engineer"}
	mail := materialprep.ExportArtifact(subject, "Backend Engineer", "Harbour Systems")
	if !strings.HasSuffix(mail.Filename, ".txt") || mail.MediaType != "text/plain; charset=utf-8" {
		t.Fatalf("subject export: %+v", mail)
	}
	if !strings.Contains(mail.Body, "Application: Go engineer") {
		t.Fatalf("subject body: %q", mail.Body)
	}
}

func TestExportFormValuesDistinguishesUploads(t *testing.T) {
	values := []store.ArtifactFormValue{
		{QuestionID: "q1", QuestionText: "Why do you want this role?", Required: store.CheckRequired,
			Kind: store.CheckQuestionFreeText, State: "answered", Text: "I like Go."},
		{QuestionID: "q2", QuestionText: "Upload your curriculum vitae.", Required: store.CheckRequired,
			Kind: store.CheckQuestionAttachment, State: "answered", Text: "cv-v2.md"},
	}
	export := materialprep.ExportFormValues("Backend Engineer", "Harbour Systems", "check-1", values)
	if !strings.HasSuffix(export.Filename, ".txt") {
		t.Fatalf("filename: %q", export.Filename)
	}
	if !strings.Contains(export.Body, "I like Go.") || !strings.Contains(export.Body, "upload: cv-v2.md") {
		t.Fatalf("body: %q", export.Body)
	}
	if !strings.Contains(export.Body, "kind: attachment") || !strings.Contains(export.Body, "basis-check: check-1") {
		t.Fatalf("body metadata: %q", export.Body)
	}
}
