package musewire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// Standard model purposes. They select the hardcoded drafting discipline;
// only these three are admitted, and the discipline never comes from
// caller input. Values match materialprep.StandardDraftPurpose,
// materialprep.StandardRewritePurpose, and
// materialprep.StandardArtifactPurpose without importing that package:
// the transport must not depend on the preparation adapter.
const (
	standardDraftPurpose    = "prepare-draft-required-answers"
	standardRewritePurpose  = "prepare-rewrite-materials"
	standardArtifactPurpose = "prepare-draft-artifacts"
)

// standardDiscipline renders the trusted instructions for one purpose,
// equivalent to the legacy DraftInstructions/RewriteInstructions plus the
// exact accepted output shape. Unknown purposes are refused.
func standardDiscipline(purpose string) (string, error) {
	switch purpose {
	case standardDraftPurpose:
		return `You draft employer-question answers from supplied verified facts only. ` +
			`Use only the facts in the prompt: never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Cite an exact approved-source excerpt for every line. Omit any question the facts cannot support. ` +
			`Reply with exactly one JSON object ` +
			`{"drafts":[{"questionId":"...","lines":[{"text":"...","citations":[{"sourceId":"...","excerpt":"..."}]}]}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	case standardRewritePurpose:
		return `Rewrite each listed employer-question answer according to the owner instruction, using only the verified facts in this prompt. ` +
			`Never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Every listed question id gets exactly one text; use the empty string for any question the facts cannot support. ` +
			`Reply with exactly one JSON object {"texts":[{"questionId":"...","text":"..."}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	case standardArtifactPurpose:
		return `Draft the listed application artifacts from the supplied verified facts only. ` +
			`Use only the facts in the prompt: never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Draft only the listed artifact types, at most once each; omit any type the facts cannot support. ` +
			`Reply with exactly one JSON object ` +
			`{"artifacts":[{"type":"...","content":"...","facts":["source-id"],"answers":["question-id"]}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	default:
		return "", fmt.Errorf("musewire: unknown standard purpose %q", purpose)
	}
}

// standardSchemaJSON renders the --output-schema document for one
// purpose, mirroring the discipline's exact JSON shape. Unknown purposes
// are refused, like the discipline itself.
func standardSchemaJSON(purpose string) (string, error) {
	switch purpose {
	case standardDraftPurpose:
		return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "drafts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "questionId": {"type": "string"},
          "lines": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "text": {"type": "string"},
                "citations": {
                  "type": "array",
                  "items": {
                    "type": "object",
                    "properties": {
                      "sourceId": {"type": "string"},
                      "excerpt": {"type": "string"}
                    },
                    "required": ["sourceId", "excerpt"],
                    "additionalProperties": false
                  }
                }
              },
              "required": ["text", "citations"],
              "additionalProperties": false
            }
          }
        },
        "required": ["questionId", "lines"],
        "additionalProperties": false
      }
    }
  },
  "required": ["drafts"],
  "additionalProperties": false
}`, nil
	case standardRewritePurpose:
		return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "texts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "questionId": {"type": "string"},
          "text": {"type": "string"}
        },
        "required": ["questionId", "text"],
        "additionalProperties": false
      }
    }
  },
  "required": ["texts"],
  "additionalProperties": false
}`, nil
	case standardArtifactPurpose:
		return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "artifacts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string"},
          "content": {"type": "string"},
          "facts": {"type": "array", "items": {"type": "string"}},
          "answers": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["type", "content", "facts", "answers"],
        "additionalProperties": false
      }
    }
  },
  "required": ["artifacts"],
  "additionalProperties": false
}`, nil
	default:
		return "", fmt.Errorf("musewire: unknown standard purpose %q", purpose)
	}
}

// StandardTransport conducts private Standard preparation turns on the
// live CLI through the same direct `muse exec` invocation as
// Contributor, with web tools disabled. It accepts StandardInput only:
// verified owner facts travel in the prompt (allowed for Standard),
// while discovery criteria can never arrive because PublicInput is
// refused. The turn makes no retrieval calls and any tool call fails the
// run closed; the collected model text returns via EventModelText for
// adapter validation.
type StandardTransport struct {
	CLIPath    string
	ModelID    string
	ProviderID string
	Trace      io.Writer
	// Provider selects the model backend. Production uses "meta"; the
	// echo provider exists for zero-spend plumbing fixtures.
	Provider string
	// ValidationPrompt replaces the preparation prompt for harness-only
	// validation turns. Production leaves it empty; any non-empty value
	// is recorded in the trace.
	ValidationPrompt string
}

var _ musecode.Transport = (*StandardTransport)(nil)

// maxStandardPromptBytes caps the verified-fact prompt of one Standard
// turn. Oversize prompts are refused before any session starts so a
// runaway prompt can never spend Standard budget.
const maxStandardPromptBytes = 65536

// standardPrompt renders the hardcoded discipline plus the verified-fact
// prompt bytes. Targets scope logging only and are never expanded here.
func standardPrompt(input musecode.StandardInput) (string, error) {
	discipline, err := standardDiscipline(input.Purpose)
	if err != nil {
		return "", err
	}
	facts := strings.TrimSpace(input.Context["prompt"])
	if facts == "" {
		return "", errors.New("musewire: standard input carries no verified-fact prompt")
	}
	if len(facts) > maxStandardPromptBytes {
		return "", fmt.Errorf("musewire: standard prompt is %d bytes, over the %d-byte turn budget",
			len(facts), maxStandardPromptBytes)
	}
	return discipline + "\n\n" + facts, nil
}

// Run conducts one Standard preparation turn to transport-terminal state.
func (t *StandardTransport) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	standard, ok := input.(musecode.StandardInput)
	if !ok {
		return errors.New("musewire: standard transport conducts private preparation only")
	}
	if resume.RunRef != "" {
		return errors.New("musewire: resume arrives with the first live run")
	}
	if t.CLIPath == "" || t.ModelID == "" || t.ProviderID == "" {
		return errors.New("musewire: standard transport needs CLI path, model and provider")
	}
	provider := t.Provider
	if provider == "" {
		provider = "meta"
	}
	runRef := path.Base(spec.Workspace)
	if !validRunRef(runRef) {
		return fmt.Errorf("musewire: workspace %q names no run", spec.Workspace)
	}
	traceSink, traceCloser, err := openRunTrace(t.Trace, spec.Workspace)
	if err != nil {
		return err
	}
	if traceCloser != nil {
		defer traceCloser.Close()
	}
	trace := &traceWriter{w: traceSink}
	trace.note("run %s workspace %s model %s provider %s backend %s purpose %s targets %d bundle %s",
		runRef, spec.Workspace, t.ModelID, t.ProviderID, provider, standard.Purpose, len(standard.Targets), standard.BundleRef)

	prompt, err := standardPrompt(standard)
	if err != nil {
		return err
	}
	if t.ValidationPrompt != "" {
		prompt = t.ValidationPrompt
		trace.note("validation prompt override active")
	}
	schema, err := standardSchemaJSON(standard.Purpose)
	if err != nil {
		return err
	}
	promptFile := filepath.Join(spec.Workspace, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return err
	}
	schemaFile := filepath.Join(spec.Workspace, "schema.json")
	if err := os.WriteFile(schemaFile, []byte(schema), 0o600); err != nil {
		return err
	}
	args := execArgsFor(promptFile, schemaFile, provider, t.ModelID, "high",
		spec.Bounds.MaxModelSteps, spec.Bounds.MaxBytesPerOp, spec.Workspace, false)
	proc, reader, err := startExec(t.CLIPath, args, spec.Workspace, trace)
	if err != nil {
		return err
	}
	defer proc.stop()

	folder := newExecFolder(t.ModelID, t.ProviderID)
	var deltas strings.Builder
	deltaBytes := int64(0)
	finalText := ""
	textSet := false
	finished := false
	terminal := ""
	detail := ""
loop:
	for {
		select {
		case <-ctx.Done():
			proc.stop()
			return ctx.Err()
		default:
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trace.hostLine(line)
			if kind, text := execText(line); kind == "run.output.delta" {
				deltaBytes += int64(len(text))
				if deltaBytes > spec.Bounds.MaxBytesTotal {
					detail = "standard turn exceeded the total text bound"
					proc.stop()
					break loop
				}
				deltas.WriteString(text)
			} else if text != "" && strings.HasPrefix(kind, "run.terminal.") {
				finalText, textSet = text, true
			}
			kind, tool, bytes, step, done, terr := folder.fold(line)
			switch {
			case terr != "":
				detail = terr
				proc.stop()
				break loop
			case step:
				sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
			case kind == "toolCall":
				detail = "standard session called a tool: " + bareToolName(tool)
				proc.stop()
				break loop
			case kind == "toolResult":
				detail = "standard session observed a tool result: " + bareToolName(tool)
				proc.stop()
				break loop
			case kind == "forbidden":
				detail = "forbidden item kind " + tool
				proc.stop()
				break loop
			case done:
				terminal = kind
				finished = true
				break loop
			}
			_ = bytes
		}
		if err != nil {
			break
		}
	}
	detail = exitDetail(detail, terminal, proc.stop())
	if detail != "" {
		runErr := errors.New("musewire: " + detail)
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	if !finished || terminal != "completed" {
		runErr := fmt.Errorf("musewire: turn ended %q without completion", terminal)
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	text := finalText
	if !textSet || text == "" {
		text = deltas.String()
	}
	if text == "" {
		runErr := errors.New("musewire: standard turn returned no text")
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	if int64(len(text)) > spec.Bounds.MaxBytesPerOp {
		runErr := errors.New("musewire: standard turn exceeded the per-operation text bound")
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	sink.Emit(musecode.Event{Kind: musecode.EventModelText, Text: text, BytesOut: int64(len(text))})
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}
