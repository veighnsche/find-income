package replydraft

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

var ErrInvalid = errors.New("reply draft invalid")

type DraftCitation struct {
	MessageID string `json:"messageId"`
	Excerpt   string `json:"excerpt"`
}

type DraftInput struct {
	ProcessingID string          `json:"processingId"`
	ThreadID     string          `json:"threadId"`
	Body         string          `json:"body"`
	Citations    []DraftCitation `json:"citations"`
	Unknowns     []string        `json:"unknowns,omitempty"`
}

type Draft struct {
	Input       DraftInput `json:"input"`
	InputSHA256 string     `json:"inputSha256"`
	Attribution string     `json:"attribution"`
}

// ValidateDraft keeps a follow-up draft grounded in exact thread excerpts. It
// records neither employer receipt nor authority to send the draft.
func ValidateDraft(input DraftInput, messages map[string]string) (Draft, error) {
	if len(input.ProcessingID) == 0 || len(input.ProcessingID) > 100 || len(input.ThreadID) == 0 || len(input.ThreadID) > 100 ||
		len(input.Body) == 0 || len(input.Body) > 8000 || len(input.Citations) < 1 || len(input.Citations) > 20 || len(input.Unknowns) > 20 {
		return Draft{}, ErrInvalid
	}
	for _, citation := range input.Citations {
		body, ok := messages[citation.MessageID]
		if !ok || len(citation.Excerpt) == 0 || len(citation.Excerpt) > 2000 || !strings.Contains(body, citation.Excerpt) {
			return Draft{}, ErrInvalid
		}
	}
	for _, unknown := range input.Unknowns {
		if len(unknown) == 0 || len(unknown) > 500 {
			return Draft{}, ErrInvalid
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 60000 {
		return Draft{}, ErrInvalid
	}
	var snapshot DraftInput
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return Draft{}, err
	}
	sum := sha256.Sum256(encoded)
	return Draft{Input: snapshot, InputSHA256: hex.EncodeToString(sum[:]), Attribution: "codex_draft"}, nil
}
