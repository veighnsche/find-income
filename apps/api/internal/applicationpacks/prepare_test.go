package applicationpacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Input {
	t.Helper()
	root, err := filepath.Abs("../../../../../")
	if err != nil {
		t.Fatal(err)
	}
	sources, template, err := LoadApprovedCareerSources(root, []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"})
	if err != nil {
		t.Fatal(err)
	}
	project := Citation{SourceID: "cv-vince-liem.md", Excerpt: "Recent work spans Go services, a compiler targeting TypeScript/Bun, Rust systems utilities and Python MCP tooling."}
	return Input{
		Role:    Role{OpportunityID: "fixture-role", OpportunityRevision: 1, ProfileRevision: 1, Title: "Platform Engineer", Company: "Fixture Systems", SourceURL: "https://example.invalid/fixture", Description: "Maintain Go services and Linux developer platforms.", Destination: "Unknown application route; inspect before sending"},
		Sources: sources, CVTemplate: template, TemplateSHA256: hash(template),
		Draft: Draft{
			Focus:            Line{Text: "Platform engineering focus: Go services, Linux developer environments and service integration.", Citations: []Citation{project}},
			Cover:            []Line{{Text: "My recent projects include Go services and Linux developer tooling.", Citations: []Citation{project}}},
			Answers:          []Answer{{Question: "What systems experience is relevant?", Lines: []Line{{Text: "My personal SodaOS project uses a Go environment and access API.", Citations: []Citation{{SourceID: "github-evidence-review.md", Excerpt: "The project adds a Go environment/access service, OAuth and SQLite-backed state."}}}}}},
			MaterialUnknowns: []string{"Actual application destination and required employer questions are unknown in this fixture."},
		},
	}
}

func TestCorrectedPackPreservesPriorBytesAndRecordsOwnerInstruction(t *testing.T) {
	typst, err := exec.LookPath("typst")
	if err != nil {
		t.Skip("Typst not installed")
	}
	renderer := Renderer{TypstPath: typst, PrivateTempDir: t.TempDir(), Timeout: 10 * time.Second}
	priorInput := fixture(t)
	prior, err := renderer.Prepare(context.Background(), priorInput)
	if err != nil {
		t.Fatal(err)
	}
	oldManifest, oldPDF, oldSource, oldHash := bytes.Clone(prior.ManifestJSON), bytes.Clone(prior.PDF), bytes.Clone(prior.TypstSource), prior.SHA256
	corrected := fixture(t)
	corrected.Draft.Focus.Text = "Corrected focus on cited personal Go service projects."
	corrected.Correction = &Correction{PriorPackID: "prior-pack", PriorVersion: 1, PriorContentSHA256: oldHash,
		OwnerInstructionID: "owner-instruction", OwnerInstructionRequestKey: "correct-focus", OwnerInstructionExpectedRevision: 1,
		OwnerInstructionText: "Please distinguish my personal project work from paid employment in the focus."}
	next, err := renderer.Prepare(context.Background(), corrected)
	if err != nil {
		t.Fatal(err)
	}
	if next.SHA256 == oldHash || !bytes.Equal(prior.ManifestJSON, oldManifest) || !bytes.Equal(prior.PDF, oldPDF) || !bytes.Equal(prior.TypstSource, oldSource) {
		t.Fatal("corrected preparation altered prior material or retained its digest")
	}
	var captured Input
	if err := json.Unmarshal(next.ManifestJSON, &captured); err != nil || captured.Correction == nil ||
		*captured.Correction != *corrected.Correction || captured.Draft.Focus.Text != corrected.Draft.Focus.Text {
		t.Fatalf("correction provenance missing: %+v %v", captured.Correction, err)
	}
	corrected.Correction.OwnerInstructionExpectedRevision = 2
	if !errors.Is(ValidateInput(corrected), ErrInvalid) {
		t.Fatal("instruction revision different from prior pack version accepted")
	}
}

func TestPrepareRealCVAndLiteralHostileMarkup(t *testing.T) {
	typst, err := exec.LookPath("typst")
	if err != nil {
		t.Skip("Typst not installed")
	}
	input := fixture(t)
	input.Draft.Focus.Text = `Platform work #pagebreak() and <script>alert(1)</script> is literal role data.`
	prepared, err := (Renderer{TypstPath: typst, PrivateTempDir: t.TempDir(), Timeout: 10 * time.Second}).Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.PDF) < 1000 || !strings.Contains(string(prepared.TypstSource), `json("focus.json")`) ||
		strings.Contains(string(prepared.TypstSource), input.Draft.Focus.Text) {
		t.Fatal("draft leaked into executable Typst source")
	}
	var captured Input
	if err := json.Unmarshal(prepared.ManifestJSON, &captured); err != nil || captured.Draft.Focus.Text != input.Draft.Focus.Text {
		t.Fatal("review text not captured")
	}
	if len(prepared.SHA256) != 64 {
		t.Fatal("missing digest")
	}
	if artifactDir := os.Getenv("PACK_ARTIFACT_DIR"); artifactDir != "" {
		if err := os.MkdirAll(artifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifactDir, "hostile-literal-cv.pdf"), prepared.PDF, 0600); err != nil {
			t.Fatal(err)
		}
		ordinary, err := (Renderer{TypstPath: typst, PrivateTempDir: t.TempDir(), Timeout: 10 * time.Second}).Prepare(context.Background(), fixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifactDir, "fixture-cv.pdf"), ordinary.PDF, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifactDir, "fixture-manifest.json"), ordinary.ManifestJSON, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectBrokenProvenanceAndTemplate(t *testing.T) {
	input := fixture(t)
	input.Sources[1].Body += "changed"
	if !errors.Is(validate(input), ErrInvalid) {
		t.Fatal("changed source accepted")
	}
	input = fixture(t)
	input.Draft.Cover[0].Citations[0].Excerpt = "unsupported paid backend tenure"
	if !errors.Is(validate(input), ErrInvalid) {
		t.Fatal("unsupported excerpt accepted")
	}
	input = fixture(t)
	input.CVTemplate = []byte("#import \"@preview/unsafe\": *")
	if !errors.Is(validate(input), ErrInvalid) {
		t.Fatal("untrusted template accepted")
	}
	input = fixture(t)
	input.Draft.Relevance = []Relevance{{Requirement: "Go service", SourceID: "cv-vince-liem.md", Scope: "relevant", Confidence: math.NaN(), InputSHA256: strings.Repeat("a", 64), Model: "fixture"}}
	if !errors.Is(validate(input), ErrInvalid) {
		t.Fatal("NaN Jev confidence accepted")
	}
	input.Draft.Relevance[0].Confidence = math.Inf(1)
	if !errors.Is(validate(input), ErrInvalid) {
		t.Fatal("infinite Jev confidence accepted")
	}
}

func TestRenderFailureAndDeadline(t *testing.T) {
	input := fixture(t)
	if _, err := (Renderer{TypstPath: "/no/such/typst", PrivateTempDir: t.TempDir(), Timeout: time.Second}).Prepare(context.Background(), input); err == nil {
		t.Fatal("missing renderer accepted")
	}
	path := filepath.Join(t.TempDir(), "slow-typst")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := (Renderer{TypstPath: path, PrivateTempDir: t.TempDir(), Timeout: 20 * time.Millisecond}).Prepare(context.Background(), input)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}
