package applicationpacks

import (
	"fmt"
	"os"
	"path/filepath"
)

var approvedCareerDigests = map[string]string{
	"cv-vince-liem.typ":         templateSHA256,
	"cv-vince-liem.md":          "eaf82b8442ac51007b83397279d7f16e0f4a8547bd63f340253ad8263a9ddf61",
	"github-evidence-review.md": "4bf7467279e2440d7a1e870274726055bd1d33550e7272a7731aa568e0f6c4f3",
	"portfolio-case-studies.md": "67f5355ae303479a363b0040fe9b18732fce308612791f0666bbf7250a33d1c0",
}

// LoadApprovedCareerSources reads only the pinned source files for this first
// pack. Later asset approval needs its own reviewed source registry; this
// loader never silently accepts a changed career document.
func LoadApprovedCareerSources(projectRoot string, names []string) ([]Source, []byte, error) {
	if !filepath.IsAbs(projectRoot) || len(names) == 0 || len(names) > 4 {
		return nil, nil, ErrInvalid
	}
	seen := make(map[string]bool)
	sources := make([]Source, 0, len(names))
	var template []byte
	for _, name := range names {
		approvedDigest, ok := approvedCareerDigests[name]
		if !ok || seen[name] {
			return nil, nil, ErrInvalid
		}
		seen[name] = true
		body, err := os.ReadFile(filepath.Join(projectRoot, name))
		if err != nil {
			return nil, nil, err
		}
		if len(body) > 100000 || hash(body) != approvedDigest {
			return nil, nil, fmt.Errorf("%w: approved source changed: %s", ErrInvalid, name)
		}
		sources = append(sources, Source{ID: name, Name: name, SHA256: approvedDigest, Approved: true, Body: string(body)})
		if name == "cv-vince-liem.typ" {
			template = body
		}
	}
	if template == nil {
		return nil, nil, ErrInvalid
	}
	return sources, template, nil
}

// SameApprovedSourceSnapshots fences a correction when its prior pack and the
// newly loaded approved career material do not have the same source digests.
func SameApprovedSourceSnapshots(prior, current []Source) bool {
	if len(prior) == 0 || len(prior) != len(current) {
		return false
	}
	byID := make(map[string]Source, len(current))
	for _, source := range current {
		if !source.Approved || source.ID == "" || source.Name == "" || len(source.SHA256) != 64 || byID[source.ID].ID != "" {
			return false
		}
		byID[source.ID] = source
	}
	for _, source := range prior {
		match, ok := byID[source.ID]
		if !ok || !source.Approved || source.Name != match.Name || source.SHA256 != match.SHA256 {
			return false
		}
		delete(byID, source.ID)
	}
	return len(byID) == 0
}
