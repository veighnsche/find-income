package publicresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
)

// viewportShape mirrors the executor's browse rule so fixtures and
// production agree on input shape; dispatch re-validates.
var viewportShape = regexp.MustCompile(`\A[0-9]{2,4}x[0-9]{2,4}\z`)

// searchCriteria carries only general, non-identifying criteria. Unknown
// input keys have nowhere to land and are dropped by construction.
type searchCriteria struct {
	RoleKeywords  []string `json:"role_keywords,omitempty"`
	RegionText    string   `json:"region_text,omitempty"`
	SkillKeywords []string `json:"skill_keywords,omitempty"`
}

func (c *searchCriteria) project() musecode.PublicCriteria {
	if c == nil {
		return musecode.PublicCriteria{}
	}
	return musecode.PublicCriteria{
		RoleKeywords:  append([]string(nil), c.RoleKeywords...),
		RegionText:    c.RegionText,
		SkillKeywords: append([]string(nil), c.SkillKeywords...),
	}
}

// searchArgs is the public_search tool input. Query, backend and params
// are free-form: the Contributor chooses sources adaptively and this
// tool never constrains them to a fixed menu.
type searchArgs struct {
	Query          string                   `json:"query"`
	Kind           string                   `json:"kind,omitempty"`
	Backend        string                   `json:"backend,omitempty"`
	Params         []researchcontract.Param `json:"params,omitempty"`
	Page           string                   `json:"page,omitempty"`
	Cursor         string                   `json:"cursor,omitempty"`
	Limit          string                   `json:"limit,omitempty"`
	Locale         string                   `json:"locale,omitempty"`
	Criteria       *searchCriteria          `json:"criteria,omitempty"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
}

// fetchArgs is the public_fetch tool input: one career page, vacancy
// page, public API call or rendered browse of an arbitrary public URL.
type fetchArgs struct {
	URL            string                   `json:"url"`
	Kind           string                   `json:"kind,omitempty"`
	Backend        string                   `json:"backend,omitempty"`
	Method         string                   `json:"method,omitempty"`
	Body           string                   `json:"body,omitempty"`
	ContentType    string                   `json:"content_type,omitempty"`
	Params         []researchcontract.Param `json:"params,omitempty"`
	Page           string                   `json:"page,omitempty"`
	Cursor         string                   `json:"cursor,omitempty"`
	Limit          string                   `json:"limit,omitempty"`
	Locale         string                   `json:"locale,omitempty"`
	Viewport       string                   `json:"viewport,omitempty"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
}

func (s *Server) searchTool(ctx context.Context, args searchArgs) (map[string]any, error) {
	kind := args.Kind
	if kind == "" {
		kind = "search"
	}
	var execKind researchcontract.ExecuteKind
	var op researchcontract.Operation
	switch kind {
	case "search":
		execKind, op = researchcontract.ExecuteSearch, researchcontract.OperationSearch
	case "api":
		execKind, op = researchcontract.ExecuteAPI, researchcontract.OperationAPI
	default:
		return invalidOutcome("kind", "kind must be search|api"), nil
	}
	if strings.TrimSpace(args.Query) == "" {
		return invalidOutcome("query", "query is required"), nil
	}
	backend := args.Backend
	if backend == "" {
		backend = researchexecute.BackendHTTP
	}
	var session map[string]string
	if args.Locale != "" {
		session = map[string]string{"locale": args.Locale}
	}
	desc := researchcontract.RequestDescriptor{
		Operation: op, Backend: backend, Method: http.MethodGet,
		URLOrQuery: args.Query, Params: args.Params, SessionFields: session,
		Pagination: researchcontract.Pagination{Page: args.Page, Cursor: args.Cursor, Limit: args.Limit},
	}
	if err := desc.Validate(); err != nil {
		if out, ok := contractOutcome(err); ok {
			return out, nil
		}
		return nil, err
	}
	if reject, ok := s.reserve(); !ok {
		return reject, nil
	}
	out, err := s.executor.Execute(ctx, researchcontract.ExecuteInput{
		Kind: execKind, Request: desc, Bounds: s.execBounds(),
		IdempotencyKey: args.IdempotencyKey, RunID: s.runID, Generation: s.Generation(),
	})
	if err != nil {
		if o, ok := contractOutcome(err); ok {
			return s.admitEnvelope(o), nil
		}
		return nil, err
	}
	return s.executeResult(ctx, out, args.Criteria)
}

func (s *Server) fetchTool(ctx context.Context, args fetchArgs) (map[string]any, error) {
	kind := args.Kind
	if kind == "" {
		kind = "fetch"
	}
	var execKind researchcontract.ExecuteKind
	var op researchcontract.Operation
	switch kind {
	case "fetch":
		execKind, op = researchcontract.ExecuteFetch, researchcontract.OperationFetch
	case "api":
		execKind, op = researchcontract.ExecuteAPI, researchcontract.OperationAPI
	case "browse":
		execKind, op = researchcontract.ExecuteBrowse, researchcontract.OperationBrowser
	default:
		return invalidOutcome("kind", "kind must be fetch|api|browse"), nil
	}
	if strings.TrimSpace(args.URL) == "" {
		return invalidOutcome("url", "url is required"), nil
	}
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	if kind == "api" {
		if method != http.MethodGet && method != http.MethodPost {
			return invalidOutcome("method", "api method must be GET|POST"), nil
		}
	} else if method != http.MethodGet {
		return invalidOutcome("method", "kind "+kind+" is GET-only"), nil
	}
	if kind != "api" && method == http.MethodPost {
		return invalidOutcome("method", "POST applies to api only"), nil
	}
	if (args.Body != "" || args.ContentType != "") && (kind != "api" || method != http.MethodPost) {
		return invalidOutcome("body", "body and content_type apply to api POST only"), nil
	}
	if args.Viewport != "" && kind != "browse" {
		return invalidOutcome("viewport", "viewport applies to browse only"), nil
	}
	if args.Viewport != "" && !viewportShape.MatchString(args.Viewport) {
		return invalidOutcome("viewport", "viewport must match WxH (e.g. 1280x800)"), nil
	}
	if kind == "browse" && (args.Page != "" || args.Cursor != "" || args.Limit != "") {
		return invalidOutcome("pagination", "kind browse carries no pagination"), nil
	}
	backend := args.Backend
	if backend == "" {
		backend = researchexecute.BackendHTTP
		if kind == "browse" {
			backend = researchexecute.BackendBrowser
		}
	}
	params := args.Params
	if args.ContentType != "" {
		params = append(append([]researchcontract.Param(nil), params...),
			researchcontract.Param{Name: "Content-Type", Value: args.ContentType})
	}
	var session map[string]string
	if args.Locale != "" || args.Viewport != "" {
		session = map[string]string{}
		if args.Locale != "" {
			session["locale"] = args.Locale
		}
		if args.Viewport != "" {
			session["viewport"] = args.Viewport
		}
	}
	var bodySHA string
	if args.Body != "" {
		sum := sha256.Sum256([]byte(args.Body))
		bodySHA = hex.EncodeToString(sum[:])
	}
	desc := researchcontract.RequestDescriptor{
		Operation: op, Backend: backend, Method: method,
		URLOrQuery: args.URL, BodySHA256: bodySHA, Body: args.Body,
		Params: params, SessionFields: session,
		Pagination: researchcontract.Pagination{Page: args.Page, Cursor: args.Cursor, Limit: args.Limit},
	}
	if err := desc.Validate(); err != nil {
		if out, ok := contractOutcome(err); ok {
			return out, nil
		}
		return nil, err
	}
	if reject, ok := s.reserve(); !ok {
		return reject, nil
	}
	out, err := s.executor.Execute(ctx, researchcontract.ExecuteInput{
		Kind: execKind, Request: desc, Bounds: s.execBounds(),
		IdempotencyKey: args.IdempotencyKey, RunID: s.runID, Generation: s.Generation(),
	})
	if err != nil {
		if o, ok := contractOutcome(err); ok {
			return s.admitEnvelope(o), nil
		}
		return nil, err
	}
	return s.executeResult(ctx, out, nil)
}

// executeResult projects one execution into a public-only envelope.
// Non-terminal backend outcomes (reused, claimed_elsewhere, uncertain)
// pass through honestly; excerpts are served only for captured results.
func (s *Server) executeResult(ctx context.Context, out researchcontract.ExecuteOutput, criteria *searchCriteria) (map[string]any, error) {
	if reject, ok := s.charge(out.Usage.Bytes); !ok {
		return reject, nil
	}
	resp := map[string]any{"outcome": string(out.Outcome)}
	if criteria != nil {
		resp["criteria"] = criteria.project()
	}
	if out.ObservationID != "" {
		resp["observation_id"] = out.ObservationID
	}
	if out.Receipt.ID != "" {
		resp["receipt_id"] = out.Receipt.ID
	}
	if out.Receipt.Status != "" {
		resp["status"] = string(out.Receipt.Status)
	}
	if out.Receipt.FinalURL != "" {
		resp["final_url"] = out.Receipt.FinalURL
	}
	resp["bytes"] = out.Usage.Bytes
	if out.Outcome != researchcontract.OutcomeOK && out.Outcome != researchcontract.OutcomeReused {
		return s.admitEnvelope(resp), nil
	}
	if out.CaptureID == "" {
		return s.admitEnvelope(resp), nil
	}
	resp["capture_id"] = out.CaptureID
	desc, text, truncated, err := s.readExcerpt(ctx, out.CaptureID)
	if err != nil {
		if o, ok := contractOutcome(err); ok {
			return s.admitEnvelope(o), nil
		}
		return nil, err
	}
	if desc.ContentType != "" {
		resp["content_type"] = desc.ContentType
	}
	resp["excerpt"] = text
	if truncated {
		resp["excerpt_truncated"] = true
	}
	return s.admitEnvelope(resp), nil
}

// readExcerpt opens one immutable capture and returns at most the
// per-operation byte bound of its stored bytes.
func (s *Server) readExcerpt(ctx context.Context, captureID string) (researchcontract.Capture, string, bool, error) {
	desc, rc, err := s.captures.OpenCapture(ctx, captureID)
	if err != nil {
		return researchcontract.Capture{}, "", false, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, s.bounds.MaxBytesPerOp+1))
	if err != nil {
		return researchcontract.Capture{}, "", false, err
	}
	if int64(len(data)) > s.bounds.MaxBytesPerOp {
		return desc, string(data[:s.bounds.MaxBytesPerOp]), true, nil
	}
	return desc, string(data), false, nil
}
