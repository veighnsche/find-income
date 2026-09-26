package materialprep

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Canonical export rendering (M6). Exports render the actual current
// artifact content — the same identities and versions Prepare, exact
// edits, rewrite and Handoff consume — with the identity, version
// and content checksum preserved in a header the owner can verify.
// CV and letter downloads use Markdown; email and form values stay
// copyable plain text. Rendering is pure: no model call, no store
// write, no transition.

// Export is one rendered download: the filename, media type, body
// bytes and the content checksum.
type Export struct {
	Filename      string
	MediaType     string
	Body          string
	ContentSHA256 string
}

// exportSlug lowercases text to filename runs, capped at 40 runes.
func exportSlug(text, fallback string) string {
	var current strings.Builder
	parts := make([]string, 0, 4)
	flush := func() {
		if current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	slug := strings.Join(parts, "-")
	if slug == "" {
		return fallback
	}
	runes := []rune(slug)
	if len(runes) > 40 {
		slug = string(runes[:40])
		slug = strings.TrimRight(slug, "-")
	}
	if slug == "" {
		return fallback
	}
	return slug
}

// exportFilename names one download so the job, item and version
// stay visible outside the app: company-role-type-vN.ext.
func exportFilename(companyName, opportunityTitle, artifactType string, version int64) string {
	extension := ".txt"
	if artifactType == store.ArtifactCV || artifactType == store.ArtifactCoverLetter {
		extension = ".md"
	}
	return fmt.Sprintf("%s-%s-%s-v%d%s",
		exportSlug(companyName, "company"), exportSlug(opportunityTitle, "role"),
		artifactType, version, extension)
}

// contentChecksum hex-encodes the sha256 of one artifact text.
func contentChecksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ExportArtifact renders one stored version for download or copy.
// The header preserves the artifact identity, version, checksum and
// pinned check above the exact stored content.
func ExportArtifact(view store.ArtifactView, opportunityTitle, companyName string) Export {
	check := view.Basis.CheckID
	if check == "" {
		check = "unpinned"
	}
	mediaType := "text/plain; charset=utf-8"
	if view.Type == store.ArtifactCV || view.Type == store.ArtifactCoverLetter {
		mediaType = "text/markdown; charset=utf-8"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# %s v%d — %s at %s\n", view.Type, view.Version, opportunityTitle, companyName)
	fmt.Fprintf(&body, "artifact-id: %s\n", view.ID)
	fmt.Fprintf(&body, "artifact-type: %s\n", view.Type)
	fmt.Fprintf(&body, "artifact-version: %d\n", view.Version)
	fmt.Fprintf(&body, "content-sha256: %s\n", contentChecksum(view.Content))
	fmt.Fprintf(&body, "basis-check: %s\n\n", check)
	body.WriteString(view.Content)
	if !strings.HasSuffix(view.Content, "\n") {
		body.WriteString("\n")
	}
	return Export{
		Filename:      exportFilename(companyName, opportunityTitle, view.Type, view.Version),
		MediaType:     mediaType,
		Body:          body.String(),
		ContentSHA256: contentChecksum(view.Content),
	}
}

// ExportFormValues renders the derived employer-field values as
// copyable text: one labeled block per field with its requiredness,
// kind and saved state. Upload fields name the file to attach rather
// than pasteable text.
func ExportFormValues(opportunityTitle, companyName, checkID string, values []store.ArtifactFormValue) Export {
	if checkID == "" {
		checkID = "unpinned"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# form values — %s at %s\n", opportunityTitle, companyName)
	fmt.Fprintf(&body, "basis-check: %s\n\n", checkID)
	for _, value := range values {
		kind := value.Kind
		if kind == "" {
			kind = "free_text"
		}
		fmt.Fprintf(&body, "## %s\n", value.QuestionText)
		fmt.Fprintf(&body, "question: %s | required: %s | kind: %s | state: %s\n",
			value.QuestionID, value.Required, kind, value.State)
		if value.Kind == store.CheckQuestionAttachment {
			fmt.Fprintf(&body, "upload: %s\n\n", value.Text)
			continue
		}
		fmt.Fprintf(&body, "%s\n\n", value.Text)
	}
	return Export{
		Filename:  exportFilename(companyName, opportunityTitle, store.ArtifactFormValues, 0),
		MediaType: "text/plain; charset=utf-8",
		Body:      body.String(),
	}
}
