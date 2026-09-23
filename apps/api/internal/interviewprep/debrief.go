package interviewprep

import (
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
)

const OwnerNoteSourceID = "owner_interview_notes"

type DebriefObservation struct {
	Kind   string    `json:"kind"` // discussed, went_well, unclear, or follow_up
	Detail CitedText `json:"detail"`
}

type DebriefInput struct {
	InterviewID  string               `json:"interviewId"`
	OwnerNotes   string               `json:"ownerNotes"`
	Observations []DebriefObservation `json:"observations"`
	Unknowns     []string             `json:"unknowns,omitempty"`
}

type Debrief struct {
	Input       DebriefInput `json:"input"`
	InputSHA256 string       `json:"inputSha256"`
	Attribution string       `json:"attribution"`
}

// ValidateDebrief keeps supplied owner notes as owner-reported context. It
// records neither an employer decision nor authority to send a follow-up.
func ValidateDebrief(input DebriefInput) (Debrief, error) {
	if !bounded(input.InterviewID, 100) || !validBody(input.OwnerNotes, 30000) ||
		len(input.Observations) < 1 || len(input.Observations) > 20 || len(input.Unknowns) > 20 {
		return Debrief{}, ErrInvalid
	}
	sources := map[string]sourceIndex{OwnerNoteSourceID: {body: input.OwnerNotes, sha: digest(input.OwnerNotes), kind: "owner_note", revision: digest(input.OwnerNotes)}}
	for _, observation := range input.Observations {
		if (observation.Kind != "discussed" && observation.Kind != "went_well" && observation.Kind != "unclear" && observation.Kind != "follow_up") ||
			!validCited(observation.Detail, sources) {
			return Debrief{}, ErrInvalid
		}
		for _, citation := range observation.Detail.Citations {
			if citation.SourceID != OwnerNoteSourceID {
				return Debrief{}, ErrInvalid
			}
		}
	}
	for _, unknown := range input.Unknowns {
		if !bounded(unknown, 500) {
			return Debrief{}, ErrInvalid
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 60000 {
		return Debrief{}, ErrInvalid
	}
	var snapshot DebriefInput
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return Debrief{}, err
	}
	return Debrief{Input: snapshot, InputSHA256: digestBytes(encoded), Attribution: "owner_reported"}, nil
}

// OwnerNoteCitation makes the provenance field explicit for Codex drafting.
func OwnerNoteCitation(excerpt string) applicationpacks.Citation {
	return applicationpacks.Citation{SourceID: OwnerNoteSourceID, Excerpt: excerpt}
}
