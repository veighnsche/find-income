package replydraft

import (
	"errors"
	"testing"
)

func TestValidateDraftRequiresExactThreadExcerpts(t *testing.T) {
	messages := map[string]string{"m-1": "We invite you to interview on Tuesday."}
	input := DraftInput{ProcessingID: "p-1", ThreadID: "t-1", Body: "Thank you for the Tuesday invitation.", Citations: []DraftCitation{{MessageID: "m-1", Excerpt: "interview on Tuesday"}}, Unknowns: []string{"Interview format unknown."}}
	draft, err := ValidateDraft(input, messages)
	if err != nil || draft.Attribution != "codex_draft" || draft.InputSHA256 == "" {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	invented := input
	invented.Citations = []DraftCitation{{MessageID: "m-1", Excerpt: "we offer the role"}}
	if _, err := ValidateDraft(invented, messages); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invented excerpt accepted: %v", err)
	}
	missing := input
	missing.Citations = []DraftCitation{{MessageID: "m-9", Excerpt: "interview"}}
	if _, err := ValidateDraft(missing, messages); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown message accepted: %v", err)
	}
	uncited := input
	uncited.Citations = nil
	if _, err := ValidateDraft(uncited, messages); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncited draft accepted: %v", err)
	}
}
