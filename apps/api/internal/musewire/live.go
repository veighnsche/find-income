package musewire

// Direct Contributor invocation (R1). LiveTransport conducts one
// Contributor turn through `muse exec --json --output-schema <file>
// --max-model-steps N --model muse-spark-1.3`, with the CLI's native web
// tools on by default. Standard inputs are refused: this transport is
// discovery/check-only.
//
// Evidence/save boundary: the CLI is an untrusted proposer. It browses
// freely chosen public sources itself and returns structured sightings or
// findings plus source URLs. It never touches app state. The app then
// fetches every cited URL itself through the bounded run executor,
// verifies statements verbatim against its own captures, and saves only
// verified vacancies/questions bound to trusted receipts (see
// publicresearch/direct.go and materialize.go). Unparsable turns,
// unfetchable citations and mismatched quotes become honest gaps — never
// saved claims, never invented evidence.

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

// discoverySchemaJSON shapes the discovery turn's final answer on the
// meta provider. Every vacancy carries the browsed page URL the app
// re-fetches itself; model-side evidence is never trusted.
const discoverySchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "vacancies": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "page_url": {"type": "string"},
          "employer_name": {"type": "string"},
          "title": {"type": "string"},
          "location_text": {"type": "string"},
          "posted_text": {"type": "string"},
          "work_pattern": {"type": "string", "enum": ["unknown", "onsite", "hybrid", "remote"]}
        },
        "required": ["page_url", "employer_name", "title", "location_text", "posted_text", "work_pattern"],
        "additionalProperties": false
      }
    },
    "sources_searched": {"type": "array", "items": {"type": "string"}},
    "gaps": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["vacancies", "sources_searched", "gaps"],
  "additionalProperties": false
}`

// checkSchemaJSON shapes the single-vacancy check turn's final answer.
// Every statement cites the browsed source URL it was copied from; the
// app re-fetches each URL and verifies the quote verbatim.
const checkSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "requirements": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "source": {"type": "string"}
        },
        "required": ["text", "source"],
        "additionalProperties": false
      }
    },
    "route": {
      "type": "object",
      "properties": {
        "kind": {"type": "string", "enum": ["direct", "referral", "recruiter", "unsupported"]},
        "destination": {"type": "string"},
        "source": {"type": "string"}
      },
      "required": ["kind", "destination", "source"],
      "additionalProperties": false
    },
    "documents": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string"},
          "required": {"type": "boolean"},
          "text": {"type": "string"},
          "source": {"type": "string"}
        },
        "required": ["label", "required", "text", "source"],
        "additionalProperties": false
      }
    },
    "questions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "prompt_text": {"type": "string"},
          "required": {"type": "boolean"},
          "source": {"type": "string"}
        },
        "required": ["prompt_text", "required", "source"],
        "additionalProperties": false
      }
    },
    "gaps": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["requirements", "route", "documents", "questions", "gaps"],
  "additionalProperties": false
}`

// LiveTransport conducts Contributor discovery and check turns on the
// live CLI, one direct `muse exec` turn each.
type LiveTransport struct {
	CLIPath    string
	ModelID    string
	ProviderID string
	Trace      io.Writer
	// Provider selects the model backend. Production uses "meta"; the
	// echo provider exists for zero-spend plumbing fixtures.
	Provider string
	// ValidationPrompt replaces the turn prompt for harness-only
	// validation turns. Production leaves it empty; any non-empty value
	// is recorded in the trace.
	ValidationPrompt string
}

var _ musecode.Transport = (*LiveTransport)(nil)

// discoveryPrompt renders generalized criteria plus operating rules for
// the CLI's native web_search/web_fetch tools. It carries no owner
// identity, profile facts, or private requirements.
func discoveryPrompt(criteria musecode.PublicCriteria) string {
	var b strings.Builder
	b.WriteString("You are the vacancy-discovery researcher for a personal job search. ")
	b.WriteString("Use your native web_search and web_fetch tools to browse public sources yourself. ")
	b.WriteString("Use no other tool, skill, shell, memory, subagent, or background work. ")
	b.WriteString("Freely choose public sources, queries, public APIs and company career pages for this generalized brief:\n")
	if len(criteria.RoleKeywords) > 0 {
		fmt.Fprintf(&b, "- roles: %s\n", strings.Join(criteria.RoleKeywords, ", "))
	}
	if criteria.RegionText != "" {
		fmt.Fprintf(&b, "- region: %s\n", criteria.RegionText)
	}
	if len(criteria.SkillKeywords) > 0 {
		fmt.Fprintf(&b, "- skills: %s\n", strings.Join(criteria.SkillKeywords, ", "))
	}
	b.WriteString("Browse each candidate listing page you report: at most 12 web_search/web_fetch calls total. ")
	b.WriteString("Every reported field must come from a page you browsed; never invent vacancies, employers, titles, or reasons. ")
	b.WriteString("Report each real vacancy with its exact listing page URL: the app re-fetches every URL itself and only saves what it verifies. ")
	b.WriteString("State work_pattern (onsite, hybrid, remote) only when the listing page says so explicitly; otherwise use unknown. ")
	b.WriteString("When the evidence is thin, report what you verified and name the coverage and gaps honestly. ")
	b.WriteString("End with exactly one JSON object, no surrounding prose:\n")
	b.WriteString(`{"vacancies":[{"page_url":"...","employer_name":"...","title":"...","location_text":"...","posted_text":"...","work_pattern":"unknown"}],`)
	b.WriteString(`"sources_searched":["..."],"gaps":["..."]}`)
	return b.String()
}

// checkPrompt scopes one Contributor turn to a single already-saved
// vacancy: browse its detail page with the native web tools, follow
// through to the real application destination, and return structured
// findings. The adapter re-fetches every cited source URL and verifies
// every quote verbatim, so copy text exactly.
func checkPrompt(check musecode.CheckInput) string {
	var b strings.Builder
	b.WriteString("You are the vacancy-detail checker for a personal job search. ")
	b.WriteString("Use your native web_search and web_fetch tools to browse the listing and application pages yourself. ")
	b.WriteString("Use no other tool, skill, shell, memory, subagent, or background work. ")
	b.WriteString("Check only this already-saved opening; never report other vacancies.\n")
	fmt.Fprintf(&b, "- vacancy: %s\n- listing page: %s\n- listing receipt: %s\n",
		check.VacancyRef, check.PageURL, check.ReceiptRef)
	b.WriteString("Fetch the listing page, then follow links to the real application page or destination. ")
	b.WriteString("At most 6 web_fetch calls total. ")
	b.WriteString("Every statement below must be copied exactly from a page you browsed, with that page's URL as its source; ")
	b.WriteString("never invent requirements, routes, documents, or questions. ")
	b.WriteString("End with exactly one JSON object, no surrounding prose:\n")
	b.WriteString(`{"requirements":[{"text":"...","source":"..."}],`)
	b.WriteString(`"route":{"kind":"direct|referral|recruiter|unsupported","destination":"...","source":"..."},`)
	b.WriteString(`"documents":[{"label":"...","required":true,"text":"...","source":"..."}],`)
	b.WriteString(`"questions":[{"prompt_text":"...","required":true,"source":"..."}],`)
	b.WriteString(`"gaps":["..."]}`)
	return b.String()
}

// resumeRefs renders the durable cursor's already-saved openings as
// prompt context. Refs are opaque to the model; the deterministic saver
// dedupes re-reported page URLs onto the same saved vacancy, so repeats
// converge instead of forking identities.
func resumeRefs(resume musecode.Cursor) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThis run CONTINUES an earlier session that already saved %d openings", len(resume.SavedRefs))
	if len(resume.SavedRefs) > 0 {
		shown := resume.SavedRefs
		if len(shown) > 50 {
			shown = shown[:50]
		}
		fmt.Fprintf(&b, " (refs: %s", strings.Join(shown, ", "))
		if len(resume.SavedRefs) > 50 {
			fmt.Fprintf(&b, ", and %d more", len(resume.SavedRefs)-50)
		}
		b.WriteString(")")
	}
	b.WriteString(". Do not report them again; continue discovering more.")
	return b.String()
}

// Run conducts one Contributor turn to transport-terminal state: a
// discovery sweep for PublicInput, or a single-vacancy deep check for
// CheckInput. Both collect the structured final text for app-side
// fetch-and-verify; the transport itself saves nothing.
func (t *LiveTransport) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	public, isDiscovery := input.(musecode.PublicInput)
	check, isCheck := input.(musecode.CheckInput)
	if !isDiscovery && !isCheck {
		return errors.New("musewire: live transport conducts contributor discovery and checks only")
	}
	if t.CLIPath == "" || t.ModelID == "" || t.ProviderID == "" {
		return errors.New("musewire: live transport needs CLI path, model and provider")
	}
	provider := t.Provider
	if provider == "" {
		provider = "meta"
	}
	runRef := path.Base(spec.Workspace)
	if !validRunRef(runRef) {
		return fmt.Errorf("musewire: workspace %q names no run", spec.Workspace)
	}
	// The exec JSONL is the only record of host-side terminals: without a
	// configured trace sink, persist it to the run workspace so failed
	// runs stay debuggable after the host exits.
	traceSink, traceCloser, err := openRunTrace(t.Trace, spec.Workspace)
	if err != nil {
		return err
	}
	if traceCloser != nil {
		defer traceCloser.Close()
	}
	trace := &traceWriter{w: traceSink}
	trace.note("run %s workspace %s model %s provider %s backend %s", runRef, spec.Workspace, t.ModelID, t.ProviderID, provider)

	prompt := discoveryPrompt(public.Criteria)
	schema := discoverySchemaJSON
	if isCheck {
		prompt = checkPrompt(check)
		schema = checkSchemaJSON
	}
	if t.ValidationPrompt != "" {
		prompt = t.ValidationPrompt
		trace.note("validation prompt override active")
	}
	// Exec is one-shot: a resumed run continues as a fresh CLI session
	// carrying the durable cursor's already-saved refs as prompt
	// context, so the model keeps discovering instead of re-reporting.
	// The saver's URL dedupe stays the hard backstop.
	if resume.RunRef != "" {
		prompt += resumeRefs(resume)
		trace.note("resume continuation with %d prior saves", len(resume.SavedRefs))
	}
	promptFile := filepath.Join(spec.Workspace, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return err
	}
	schemaFile := filepath.Join(spec.Workspace, "schema.json")
	if err := os.WriteFile(schemaFile, []byte(schema), 0o600); err != nil {
		return err
	}
	args := execArgsFor(promptFile, schemaFile, provider, t.ModelID, "max",
		spec.Bounds.MaxModelSteps, spec.Bounds.MaxBytesPerOp, spec.Workspace, true)
	proc, reader, err := startExec(t.CLIPath, args, spec.Workspace, trace)
	if err != nil {
		return err
	}
	defer proc.stop()

	folder := newExecFolder(t.ModelID, t.ProviderID)
	finished := false
	terminal := ""
	detail := ""
	var deltas strings.Builder
	deltaBytes := int64(0)
	finalText := ""
	textSet := false
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
					detail = "contributor turn exceeded the total text bound"
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
				// Native tool names pass through verbatim; the
				// supervisor allowlist admits web_search/web_fetch
				// and fails anything else closed.
				name := bareToolName(tool)
				trace.note("tool call %q", name)
				sink.Emit(musecode.Event{Kind: musecode.EventToolCall, Tool: name})
			case kind == "toolResult":
				sink.Emit(musecode.Event{Kind: musecode.EventToolResult, Tool: bareToolName(tool), BytesOut: bytes})
			case kind == "forbidden":
				detail = "forbidden item kind " + tool
				proc.stop()
				break loop
			case done:
				terminal = kind
				finished = true
				break loop
			}
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
	// The structured final text is the turn's product: discovery and
	// check callers parse it, fetch its cited URLs themselves, and save
	// only what their own captures verify. Empty text is not a
	// transport failure; the caller records the honest gap.
	text := finalText
	if !textSet || text == "" {
		text = deltas.String()
	}
	if int64(len(text)) > spec.Bounds.MaxBytesPerOp {
		runErr := errors.New("musewire: contributor turn exceeded the per-operation text bound")
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	if text != "" {
		sink.Emit(musecode.Event{Kind: musecode.EventModelText, Text: text, BytesOut: int64(len(text))})
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}
