// Package applicationpacks prepares one immutable, reviewable application pack.
// The caller owns Codex drafting, Jev relevance assessment, and the round-charged
// database mutation. This package checks bounds and provenance and renders bytes.
package applicationpacks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid application pack")

const templateSHA256 = "e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57"

// Source is a private, exact snapshot. An approval is an owner/workflow decision,
// not a conclusion that every possible paraphrase is true.
type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Approved bool   `json:"approved"`
	Body     string `json:"body"`
}

type Role struct {
	OpportunityID       string `json:"opportunityId"`
	OpportunityRevision int64  `json:"opportunityRevision"`
	ProfileRevision     int64  `json:"profileRevision"`
	Title               string `json:"title"`
	Company             string `json:"company"`
	SourceURL           string `json:"sourceUrl"`
	Description         string `json:"description"`
	Destination         string `json:"destination"`
}

// Line is Codex-written text. A citation gives the exact source excerpt used
// for a factual line. Structure checks cannot establish semantic entailment.
type Line struct {
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
}

type Citation struct {
	SourceID string `json:"sourceId"`
	Excerpt  string `json:"excerpt"`
}

type Answer struct {
	Question string `json:"question"`
	Lines    []Line `json:"lines"`
}

type Relevance struct {
	Requirement string  `json:"requirement"`
	SourceID    string  `json:"sourceId"`
	Scope       string  `json:"scope"` // relevant, uncertain, or unrelated
	Confidence  float64 `json:"confidence"`
	InputSHA256 string  `json:"inputSha256"`
	Model       string  `json:"model"`
}

type Draft struct {
	// Focus is one role-specific CV line supplied by Codex with citation.
	Focus            Line        `json:"focus"`
	Cover            []Line      `json:"cover"`
	Answers          []Answer    `json:"answers"`
	MaterialUnknowns []string    `json:"materialUnknowns"`
	Relevance        []Relevance `json:"relevance"`
}

type Input struct {
	Role                     Role     `json:"role"`
	Sources                  []Source `json:"sources"`
	Draft                    Draft    `json:"draft"`
	CVTemplate               []byte   `json:"-"`
	TemplateSHA256           string   `json:"templateSha256"`
	PreparationRequestSHA256 string   `json:"preparationRequestSha256,omitempty"`
}

type Prepared struct {
	ManifestJSON []byte
	TypstSource  []byte
	PDF          []byte
	SHA256       string
}

type Renderer struct {
	TypstPath      string
	PrivateTempDir string
	Timeout        time.Duration
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func bounded(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, 0)
}

func validateLine(line Line, sources map[string]Source) error {
	if !bounded(line.Text, 1200) || len(line.Citations) == 0 || len(line.Citations) > 5 {
		return ErrInvalid
	}
	for _, citation := range line.Citations {
		source, ok := sources[citation.SourceID]
		if !ok || !bounded(citation.Excerpt, 1200) || !strings.Contains(source.Body, citation.Excerpt) {
			return fmt.Errorf("%w: citation missing from approved source", ErrInvalid)
		}
	}
	return nil
}

func validate(input Input) error {
	r := input.Role
	if !bounded(r.OpportunityID, 100) || r.OpportunityRevision < 1 || r.ProfileRevision < 1 ||
		!bounded(r.Title, 200) || !bounded(r.Company, 200) || !bounded(r.SourceURL, 2000) ||
		!bounded(r.Description, 30000) || len(r.Destination) > 2000 || strings.ContainsRune(r.Destination, 0) ||
		len(input.Sources) < 2 || len(input.Sources) > 8 ||
		input.TemplateSHA256 != templateSHA256 || hash(input.CVTemplate) != input.TemplateSHA256 {
		return ErrInvalid
	}
	if input.PreparationRequestSHA256 != "" {
		if len(input.PreparationRequestSHA256) != 64 {
			return ErrInvalid
		}
		for _, char := range input.PreparationRequestSHA256 {
			if char < '0' || char > '9' && char < 'a' || char > 'f' {
				return ErrInvalid
			}
		}
	}
	sources := make(map[string]Source, len(input.Sources))
	for _, source := range input.Sources {
		if !bounded(source.ID, 100) || !bounded(source.Name, 100) || !source.Approved ||
			!bounded(source.Body, 100000) || hash([]byte(source.Body)) != source.SHA256 ||
			len(source.SHA256) != 64 || sources[source.ID].ID != "" {
			return ErrInvalid
		}
		sources[source.ID] = source
	}
	templateSnapshot := false
	for _, source := range input.Sources {
		if source.SHA256 == input.TemplateSHA256 && source.Body == string(input.CVTemplate) {
			templateSnapshot = true
		}
	}
	if !templateSnapshot {
		return ErrInvalid
	}
	if err := validateLine(input.Draft.Focus, sources); err != nil {
		return err
	}
	if len(input.Draft.Cover) < 1 || len(input.Draft.Cover) > 12 || len(input.Draft.Answers) > 12 || len(input.Draft.MaterialUnknowns) > 20 {
		return ErrInvalid
	}
	coverLength := 0
	for _, line := range input.Draft.Cover {
		if err := validateLine(line, sources); err != nil {
			return err
		}
		coverLength += len(line.Text)
	}
	if coverLength > 2400 {
		return ErrInvalid
	}
	for _, answer := range input.Draft.Answers {
		if !bounded(answer.Question, 1000) || len(answer.Lines) == 0 || len(answer.Lines) > 8 {
			return ErrInvalid
		}
		answerLength := 0
		for _, line := range answer.Lines {
			if err := validateLine(line, sources); err != nil {
				return err
			}
			answerLength += len(line.Text)
		}
		if answerLength > 2000 {
			return ErrInvalid
		}
	}
	for _, unknown := range input.Draft.MaterialUnknowns {
		if !bounded(unknown, 500) {
			return ErrInvalid
		}
	}
	for _, relevance := range input.Draft.Relevance {
		if !bounded(relevance.Requirement, 1000) || sources[relevance.SourceID].ID == "" ||
			(relevance.Scope != "relevant" && relevance.Scope != "uncertain" && relevance.Scope != "unrelated") ||
			math.IsNaN(relevance.Confidence) || math.IsInf(relevance.Confidence, 0) || relevance.Confidence < 0 || relevance.Confidence > 1 || len(relevance.InputSHA256) != 64 || !bounded(relevance.Model, 100) {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateInput checks deterministic structure and exact citation excerpts
// before a caller spends Jev allowance or starts rendering.
func ValidateInput(input Input) error { return validate(input) }

// Prepare does no database write. Save its exact bytes through the guarded
// round mutation after rechecking the role/profile revisions in one transaction.
func (renderer Renderer) Prepare(ctx context.Context, input Input) (Prepared, error) {
	if err := validate(input); err != nil {
		return Prepared{}, err
	}
	if renderer.TypstPath == "" || !filepath.IsAbs(renderer.PrivateTempDir) || renderer.Timeout <= 0 || renderer.Timeout > 30*time.Second {
		return Prepared{}, ErrInvalid
	}
	if err := os.MkdirAll(renderer.PrivateTempDir, 0700); err != nil {
		return Prepared{}, err
	}
	if err := os.Chmod(renderer.PrivateTempDir, 0700); err != nil {
		return Prepared{}, err
	}
	root, err := os.MkdirTemp(renderer.PrivateTempDir, "application-pack-")
	if err != nil {
		return Prepared{}, err
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0700); err != nil {
		return Prepared{}, err
	}
	// The trusted CV supplies layout and dated career history. Role-specific
	// content arrives through a JSON string, which Typst renders as text.
	const marker = "#section[Professional Summary]"
	if bytes.Count(input.CVTemplate, []byte(marker)) != 1 {
		return Prepared{}, ErrInvalid
	}
	source := bytes.Replace(input.CVTemplate, []byte(marker), []byte("#let application_focus = json(\"focus.json\").focus\n"+marker+"\n#application_focus\n"), 1)
	focus, _ := json.Marshal(struct {
		Focus string `json:"focus"`
	}{input.Draft.Focus.Text})
	if err := os.WriteFile(filepath.Join(root, "focus.json"), focus, 0600); err != nil {
		return Prepared{}, err
	}
	if err := os.WriteFile(filepath.Join(root, "cv.typ"), source, 0600); err != nil {
		return Prepared{}, err
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, renderer.Timeout)
	defer cancel()
	command := exec.CommandContext(deadlineCtx, renderer.TypstPath, "compile", "--root", root, filepath.Join(root, "cv.typ"), filepath.Join(root, "cv.pdf"))
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if deadlineCtx.Err() != nil {
			return Prepared{}, deadlineCtx.Err()
		}
		return Prepared{}, fmt.Errorf("typst compile failed: %w: %.500s", err, stderr.String())
	}
	info, err := os.Stat(filepath.Join(root, "cv.pdf"))
	if err != nil {
		return Prepared{}, err
	}
	if info.Size() < 100 || info.Size() > 3<<20 {
		return Prepared{}, ErrInvalid
	}
	pdf, err := os.ReadFile(filepath.Join(root, "cv.pdf"))
	if err != nil {
		return Prepared{}, err
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return Prepared{}, ErrInvalid
	}
	manifest, err := json.Marshal(input)
	if err != nil {
		return Prepared{}, err
	}
	content, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, source, pdf})
	return Prepared{ManifestJSON: manifest, TypstSource: source, PDF: pdf, SHA256: hash(content)}, nil
}
