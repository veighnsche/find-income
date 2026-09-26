package publicresearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Frozen Contributor tool names. The registry must always equal the
// musecode allowlist exactly; any addition needs an M contract change.
const (
	ToolSearch       = "public_search"
	ToolFetch        = "public_fetch"
	ToolSaveVacancy  = "public_save_vacancy"
	ToolSaveQuestion = "public_save_question"
	ToolListSaved    = "public_list_saved"
)

// ToolNames returns the five served tools in registration order.
func ToolNames() []string {
	return []string{ToolSearch, ToolFetch, ToolSaveVacancy, ToolSaveQuestion, ToolListSaved}
}

// Deps binds one public discovery server. Executor and Captures are the
// shared research backends (fakes in tests, researchexecute and
// researchmemory in production via E06). Bounds are the frozen session
// ceilings. RunID and Generation scope every execution to the active
// run; Now is an optional clock for tests.
type Deps struct {
	Executor   researchcontract.Executor
	Captures   researchcontract.CaptureReader
	Bounds     musecode.Bounds
	RunID      string
	Generation int64
	Now        func() time.Time
}

// Server is a public-only MCP tool host with request/byte accounting
// and an in-memory saved-evidence store. Saved refs are app-side opaque
// handles; raw receipts stay behind the capture reader.
type Server struct {
	mcp        *mcp.Server
	executor   researchcontract.Executor
	captures   researchcontract.CaptureReader
	bounds     musecode.Bounds
	runID      string
	generation int64
	now        func() time.Time
	startedAt  time.Time

	mu        sync.Mutex
	toolCalls int
	bytesOut  int64

	vacSeq    int
	vacancies map[string]musecode.PublicVacancy
	vacOrder  []string
	// vacByURL dedupes deterministic saves by normalized page URL across
	// turns of one run; every vacancy write path maintains it.
	vacByURL  map[string]string
	qSeq      int
	questions map[string]musecode.PublicQuestion
	qOrder    []string
	idemVac   map[string]string
}

// NewServer builds a server exposing exactly the frozen tools. Invalid
// deps fail closed before any tool is served.
func NewServer(deps Deps) (*Server, error) {
	if deps.Executor == nil || deps.Captures == nil {
		return nil, errors.New("publicresearch: executor and capture reader required")
	}
	if err := deps.Bounds.Validate(); err != nil {
		return nil, err
	}
	if deps.RunID == "" {
		return nil, errors.New("publicresearch: run id required")
	}
	if deps.Generation <= 0 {
		return nil, errors.New("publicresearch: positive generation required")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	s := &Server{
		executor: deps.Executor, captures: deps.Captures,
		bounds: deps.Bounds, runID: deps.RunID, generation: deps.Generation,
		now: now, startedAt: now(),
		vacancies: map[string]musecode.PublicVacancy{},
		vacByURL:  map[string]string{},
		questions: map[string]musecode.PublicQuestion{},
		idemVac:   map[string]string{},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "find-income-public", Version: "0.1.0"}, nil)
	registerTool(server, ToolSearch, "Search unrestricted public sources (web search or public API) and capture the results as reusable evidence.", s.searchTool)
	registerTool(server, ToolFetch, "Fetch one public URL (career page, vacancy page or public API, optionally rendered) and capture the bytes as reusable evidence.", s.fetchTool)
	registerTool(server, ToolSaveVacancy, "Save one public vacancy projection bound to a trusted capture receipt.", s.saveVacancyTool)
	registerTool(server, ToolSaveQuestion, "Save one actual employer question captured from a saved vacancy page.", s.saveQuestionTool)
	registerTool(server, ToolListSaved, "List saved public vacancies and questions.", s.listSavedTool)
	s.mcp = server
	return s, nil
}

func registerTool[I any](server *mcp.Server, name, description string, handler func(context.Context, I) (map[string]any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, input I) (*mcp.CallToolResult, map[string]any, error) {
		out, err := handler(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, out, nil
	})
}

// MCPServer returns the underlying registry for session wiring.
func (s *Server) MCPServer() *mcp.Server { return s.mcp }

// Handler serves the registry over stateless streamable HTTP. It opens
// no listener by itself; transport and authentication stay with M (E06).
func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// Usage reports admitted tool calls and accounted bytes.
func (s *Server) Usage() (calls int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolCalls, s.bytesOut
}

// reserve admits one tool call against the request and wall-clock
// bounds. Invalid input must be rejected before calling reserve so it
// consumes zero allowance.
func (s *Server) reserve() (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now().Sub(s.startedAt) > s.bounds.MaxWallClock {
		return limitOutcome("wall_clock", "wall-clock bound exceeded; the run stops here"), false
	}
	if s.toolCalls >= s.bounds.MaxToolCalls {
		return limitOutcome("tool_calls", "tool-call bound exceeded; the run stops here"), false
	}
	s.toolCalls++
	return nil, true
}

// charge accounts opBytes against the per-operation and total byte
// bounds. Both retrieved bytes and returned bytes are charged, so a
// huge capture and a huge projection each fail closed.
func (s *Server) charge(opBytes int64) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if opBytes > s.bounds.MaxBytesPerOp {
		return limitOutcome("bytes_per_op", "per-operation byte bound exceeded"), false
	}
	if s.bytesOut+opBytes > s.bounds.MaxBytesTotal {
		return limitOutcome("bytes_total", "total byte bound exceeded; the run stops here"), false
	}
	s.bytesOut += opBytes
	return nil, true
}

// admit charges one complete response against the byte bounds.
func (s *Server) admit(out map[string]any) map[string]any {
	if reject, ok := s.charge(outputSize(out)); !ok {
		return reject
	}
	return out
}

// admitEnvelope charges a search/fetch envelope against the total byte
// bound only. The dominant transfers behind it (retrieved bytes and the
// excerpt) already passed the per-operation bound, so the small JSON
// framing must not fail a bounded excerpt.
func (s *Server) admitEnvelope(out map[string]any) map[string]any {
	if reject, ok := s.chargeTotal(outputSize(out)); !ok {
		return reject
	}
	return out
}

// chargeTotal accounts framing bytes against the total bound only.
func (s *Server) chargeTotal(opBytes int64) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytesOut+opBytes > s.bounds.MaxBytesTotal {
		return limitOutcome("bytes_total", "total byte bound exceeded; the run stops here"), false
	}
	s.bytesOut += opBytes
	return nil, true
}

// execBounds derives one execution's request/byte/deadline caps from
// the remaining session bounds.
func (s *Server) execBounds() researchcontract.Bounds {
	s.mu.Lock()
	defer s.mu.Unlock()
	remainingCalls := s.bounds.MaxToolCalls - s.toolCalls
	if remainingCalls < 1 {
		remainingCalls = 1
	}
	remainingBytes := s.bounds.MaxBytesTotal - s.bytesOut
	if remainingBytes > s.bounds.MaxBytesPerOp {
		remainingBytes = s.bounds.MaxBytesPerOp
	}
	if remainingBytes < 1 {
		remainingBytes = 1
	}
	remainingWall := s.bounds.MaxWallClock - s.now().Sub(s.startedAt)
	if remainingWall < time.Millisecond {
		remainingWall = time.Millisecond
	}
	return researchcontract.Bounds{
		MaxBytes:    remainingBytes,
		MaxRequests: remainingCalls,
		DeadlineMs:  int64(remainingWall / time.Millisecond),
	}
}

func outputSize(out map[string]any) int64 {
	raw, err := json.Marshal(out)
	if err != nil {
		return 1 << 62 // Unmeasurable output never ships.
	}
	return int64(len(raw))
}

func limitOutcome(field, detail string) map[string]any {
	return map[string]any{"outcome": string(researchcontract.OutcomeBudgetExhausted),
		"field": field, "detail": detail}
}

func invalidOutcome(field, detail string) map[string]any {
	return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
		"field": field, "detail": detail}
}

func notFoundOutcome(field, detail string) map[string]any {
	return map[string]any{"outcome": string(researchcontract.OutcomeNotFound),
		"field": field, "detail": detail}
}

// contractOutcome maps a backend error to its in-band response.
// ok=false means err is internal and must propagate as a tool error.
func contractOutcome(err error) (map[string]any, bool) {
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	out := map[string]any{"outcome": string(cerr.Code)}
	if cerr.Field != "" {
		out["field"] = cerr.Field
	}
	if cerr.Detail != "" {
		out["detail"] = cerr.Detail
	}
	return out, true
}
