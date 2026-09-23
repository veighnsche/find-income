package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type recordHTTP struct {
	t       *testing.T
	client  *http.Client
	server  *httptest.Server
	db      *store.Store
	service *auth.Service
	cookie  *http.Cookie
	csrf    string
}

func newRecordHTTP(t *testing.T) *recordHTTP {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := auth.NewService(db)
	if err := service.SetupAdministrator(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(db, service, Options{AllowedOrigins: []string{origin}}))
	t.Cleanup(func() { server.Close(); _ = db.Close() })
	return &recordHTTP{t: t, client: server.Client(), server: server, db: db, service: service}
}

func (h *recordHTTP) do(method, path, body, bearer, csrf, requestOrigin string, cookie *http.Cookie) (int, []byte) {
	h.t.Helper()
	request, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if requestOrigin != "" {
		request.Header.Set("Origin", requestOrigin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.StatusCode, data
}

func (h *recordHTTP) owner(method, path string, body ...string) (int, []byte) {
	value := ""
	if len(body) != 0 {
		value = body[0]
	}
	return h.do(method, path, value, "", h.csrf, origin, h.cookie)
}

func (h *recordHTTP) login() auth.Principal {
	h.t.Helper()
	request, err := http.NewRequest("POST", h.server.URL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		h.t.Fatalf("owner cookie login: %d cookies=%d", response.StatusCode, len(response.Cookies()))
	}
	h.cookie = response.Cookies()[0]
	// Keep CSRF from the same session as the retained cookie.
	status, data := h.do("GET", "/auth/session", "", "", "", "", h.cookie)
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if status != 200 {
		h.t.Fatalf("session: %d %s", status, data)
	}
	if err := json.Unmarshal(data, &session); err != nil {
		h.t.Fatal(err)
	}
	h.csrf = session.CSRFToken
	p, err := h.service.SessionPrincipal(context.Background(), h.cookie.Value)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	return value
}

func requireStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status=%d want=%d body=%s", got, want, body)
	}
}

func TestRecordRoutesRealHTTPAuthRevisionsAndAudit(t *testing.T) {
	h := newRecordHTTP(t)
	status, body := h.do("GET", "/companies", "", "", "", "", nil)
	requireStatus(t, status, 401, body)
	status, body = h.do("POST", "/companies", `{"name":"Synthetic Co"}`, "", "", origin, nil)
	requireStatus(t, status, 401, body)
	owner := h.login()
	readAgent, readToken, err := h.service.CreateAgent(context.Background(), owner, "reader", []string{"opportunities:read", "openings:ingest"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	writeAgent, writeToken, err := h.service.CreateAgent(context.Background(), owner, "writer", []string{"opportunities:write"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.do("POST", "/companies", `{"name":"Synthetic Co"}`, "", "", "", h.cookie)
	requireStatus(t, status, 403, body) // cookie mutations still need CSRF + Origin
	status, body = h.owner("POST", "/companies", `{"name":"Synthetic Co","website":"https://example.test"}`)
	requireStatus(t, status, 201, body)
	companyResult := decodeObject(t, body)
	company := companyResult["company"].(map[string]any)
	companyID := company["id"].(string)
	companyChangeID := companyResult["changeId"].(string)
	if company["revision"] != float64(1) || companyChangeID == "" {
		t.Fatalf("company mutation: %+v", companyResult)
	}
	status, body = h.do("POST", "/companies", `{"name":"Reader Cannot Write"}`, readToken, "", "", nil)
	requireStatus(t, status, 403, body)
	status, body = h.do("GET", "/companies", "", writeToken, "", "", nil)
	requireStatus(t, status, 403, body)
	status, body = h.do("GET", "/companies", "", readToken, "", "", nil)
	requireStatus(t, status, 200, body)

	vacancy := fmt.Sprintf(`{"companyId":%q,"title":"Platform Engineer","kind":"employment","stage":"saved","sourceUrl":"https://jobs.example.test/1","originalText":"Platform work at 37.5 hours","notes":"Ask about pay","compensation":{"currency":"EUR","minAmountCents":600000,"period":"month","referenceHours":"37.5","basis":"base"}}`, companyID)
	status, body = h.do("POST", "/opportunities", vacancy, readToken, "", "", nil)
	requireStatus(t, status, 403, body) // openings:ingest does not grant arbitrary CRUD
	status, body = h.do("POST", "/opportunities", vacancy, writeToken, "", "", nil)
	requireStatus(t, status, 201, body)
	opportunityResult := decodeObject(t, body)
	opportunity := opportunityResult["opportunity"].(map[string]any)
	opportunityID := opportunity["id"].(string)
	changeID := opportunityResult["changeId"].(string)
	if opportunity["notes"] != "Ask about pay" || opportunity["revision"] != float64(1) {
		t.Fatalf("created opportunity: %+v", opportunity)
	}
	status, body = h.do("GET", "/changes/"+changeID, "", readToken, "", "", nil)
	requireStatus(t, status, 200, body)
	change := decodeObject(t, body)
	if change["actorKind"] != "agent" || change["actorId"] != writeAgent.ID || change["entityId"] != opportunityID ||
		change["snapshot"].(map[string]any)["originalText"] != "Platform work at 37.5 hours" {
		t.Fatalf("change attribution/snapshot: %+v", change)
	}
	status, body = h.do("GET", "/changes/"+companyChangeID, "", readToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if record := decodeObject(t, body); record["actorId"] != "owner" {
		t.Fatalf("company actor: %+v", record)
	}

	status, body = h.owner("PATCH", "/opportunities/"+opportunityID, `{"expectedRevision":1,"notes":"Confirmed interview date"}`)
	requireStatus(t, status, 200, body)
	updated := decodeObject(t, body)["opportunity"].(map[string]any)
	if updated["revision"] != float64(2) || updated["notes"] != "Confirmed interview date" || updated["originalText"] != "Platform work at 37.5 hours" {
		t.Fatalf("notes patch changed source or revision: %+v", updated)
	}
	status, body = h.owner("PATCH", "/opportunities/"+opportunityID, `{"expectedRevision":1,"title":"Unsaved stale edit"}`)
	requireStatus(t, status, 409, body)
	details := decodeObject(t, body)["error"].(map[string]any)["details"].(map[string]any)
	if details["currentRevision"] != float64(2) || details["currentOpportunity"].(map[string]any)["title"] != "Platform Engineer" {
		t.Fatalf("conflict lacks reconciliable current record: %+v", details)
	}
	status, body = h.do("GET", "/opportunities/"+opportunityID, "", readToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if got := decodeObject(t, body)["opportunity"].(map[string]any)["title"]; got != "Platform Engineer" {
		t.Fatalf("stale patch changed record: %v", got)
	}

	duplicate := strings.Replace(vacancy, `"Ask about pay"`, `"Second sighting"`, 1)
	status, body = h.owner("POST", "/opportunities", duplicate)
	requireStatus(t, status, 201, body)
	duplicateID := decodeObject(t, body)["opportunity"].(map[string]any)["id"].(string)
	status, body = h.do("GET", "/opportunities/"+duplicateID, "", readToken, "", "", nil)
	requireStatus(t, status, 200, body)
	warnings := decodeObject(t, body)["likelyDuplicates"].([]any)
	if len(warnings) != 1 || warnings[0].(map[string]any)["reason"] != "same_source_url" {
		t.Fatalf("duplicate warning: %+v", warnings)
	}
	status, body = h.owner("PATCH", "/opportunities/"+opportunityID,
		`{"expectedRevision":2,"sourceUrl":"https://jobs.example.test/1-updated","originalText":"Revised vacancy source","notes":"","compensation":{}}`)
	requireStatus(t, status, 200, body)
	revised := decodeObject(t, body)["opportunity"].(map[string]any)
	if revised["revision"] != float64(3) || revised["notes"] != "" || revised["compensation"].(map[string]any)["currency"] != "unknown" {
		t.Fatalf("source/clear patch: %+v", revised)
	}
	status, body = h.owner("GET", "/changes/"+changeID)
	requireStatus(t, status, 200, body)
	if old := decodeObject(t, body)["snapshot"].(map[string]any); old["originalText"] != "Platform work at 37.5 hours" || old["sourceUrl"] != "https://jobs.example.test/1" {
		t.Fatalf("earlier source snapshot was overwritten: %+v", old)
	}
	status, body = h.owner("GET", "/opportunities?limit=1")
	requireStatus(t, status, 200, body)
	firstPage := decodeObject(t, body)
	if len(firstPage["items"].([]any)) != 1 || firstPage["nextCursor"] == nil {
		t.Fatalf("opportunity pagination: %+v", firstPage)
	}
	status, body = h.owner("GET", "/opportunities?limit=1&cursor="+firstPage["nextCursor"].(string))
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("second opportunity page: %s", body)
	}
	status, body = h.owner("GET", "/opportunities?limit=1&cursor="+firstPage["nextCursor"].(string)+"&kind=project")
	requireStatus(t, status, 400, body)

	status, body = h.owner("POST", "/opportunities/"+opportunityID+"/archive", `{"expectedRevision":3}`)
	requireStatus(t, status, 200, body)
	if got := decodeObject(t, body)["opportunity"].(map[string]any)["revision"]; got != float64(4) {
		t.Fatalf("archive revision: %v", got)
	}
	status, body = h.owner("GET", "/opportunities")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("archived record in default list: %s", body)
	}
	status, body = h.owner("GET", "/opportunities?includeArchived=true")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 2 {
		t.Fatalf("archived record absent from requested list: %s", body)
	}
	if err := h.service.RevokeAgent(context.Background(), owner, writeAgent.ID); err != nil {
		t.Fatal(err)
	}
	status, body = h.do("POST", "/companies", `{"name":"After revoke"}`, writeToken, "", "", nil)
	requireStatus(t, status, 401, body)
	_ = readAgent
}

func TestRecordRoutesRejectInvalidInputWithoutMutation(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	status, body := h.owner("POST", "/companies", `{"name":"Only company"}`)
	requireStatus(t, status, 201, body)
	companyID := decodeObject(t, body)["company"].(map[string]any)["id"].(string)
	before, err := h.db.ListRecordChanges(context.Background(), store.RecordChangeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	vacancy := fmt.Sprintf(`{"companyId":%q,"title":"Backend Engineer","kind":"employment","stage":"saved","sourceUrl":"https://jobs.example.test/2","originalText":"Source"`, companyID)
	for _, invalid := range []string{
		vacancy + `,"actorId":"forged"}`,
		vacancy + `,"compensation":{"confirmedActualMonthlyCents":999999}}`,
		vacancy + `,"title":null}`,
		vacancy + `,"originalText":"` + strings.Repeat("x", recordBodyLimit) + `"}`,
	} {
		status, body = h.owner("POST", "/opportunities", invalid)
		requireStatus(t, status, 400, body)
	}
	status, body = h.owner("POST", "/opportunities", vacancy+`}`)
	requireStatus(t, status, 201, body)
	id := decodeObject(t, body)["opportunity"].(map[string]any)["id"].(string)
	for _, invalid := range []string{
		`{"expectedRevision":1,"notes":null}`,
		`{"expectedRevision":1,"actorKind":"administrator","notes":"bad"}`,
		`{"expectedRevision":1,"compensation":{"confirmedActualMonthlyCents":900000}}`,
	} {
		status, body = h.owner("PATCH", "/opportunities/"+id, invalid)
		requireStatus(t, status, 400, body)
	}
	status, body = h.owner("GET", "/opportunities/"+id)
	requireStatus(t, status, 200, body)
	if decodeObject(t, body)["opportunity"].(map[string]any)["revision"] != float64(1) {
		t.Fatal("invalid patch changed revision")
	}
	after, err := h.db.ListRecordChanges(context.Background(), store.RecordChangeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != len(before.Items)+1 {
		t.Fatalf("invalid requests left audit rows: before=%d after=%d", len(before.Items), len(after.Items))
	}
	// The records endpoint accepts a large legitimate vacancy while the small
	// authentication body limit is still enforced by the original decoder.
	large := strings.Repeat("source ", 20000)
	valid, _ := json.Marshal(map[string]any{"companyId": companyID, "title": "Large source", "kind": "employment", "stage": "saved", "originalText": large})
	status, body = h.owner("POST", "/opportunities", string(valid))
	requireStatus(t, status, 201, body)
	loginBody := `{"password":"` + strings.Repeat("x", 5000) + `"}`
	status, body = h.do("POST", "/auth/login", loginBody, "", "", origin, nil)
	requireStatus(t, status, 400, body)
	if bytes.Contains(body, []byte(password)) {
		t.Fatal("password reflected in error")
	}
}

func TestCompanyRoutesDuplicatePatchArchiveAndConflict(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	status, body := h.owner("POST", "/companies", `{"name":"Example Works","website":"https://example.test"}`)
	requireStatus(t, status, 201, body)
	id := decodeObject(t, body)["company"].(map[string]any)["id"].(string)
	status, body = h.owner("POST", "/companies", `{"name":"Different name","website":"https://example.test"}`)
	requireStatus(t, status, 201, body)
	status, body = h.owner("GET", "/companies/"+id)
	requireStatus(t, status, 200, body)
	warnings := decodeObject(t, body)["likelyDuplicates"].([]any)
	if len(warnings) != 1 || warnings[0].(map[string]any)["reason"] != "same_website" {
		t.Fatalf("company duplicate warning: %+v", warnings)
	}
	status, body = h.owner("PATCH", "/companies/"+id, `{"expectedRevision":1,"notes":"Recruiter met"}`)
	requireStatus(t, status, 200, body)
	company := decodeObject(t, body)["company"].(map[string]any)
	if company["revision"] != float64(2) || company["notes"] != "Recruiter met" || company["website"] != "https://example.test" {
		t.Fatalf("partial company patch: %+v", company)
	}
	status, body = h.owner("PATCH", "/companies/"+id, `{"expectedRevision":1,"notes":"stale"}`)
	requireStatus(t, status, 409, body)
	details := decodeObject(t, body)["error"].(map[string]any)["details"].(map[string]any)
	if details["currentRevision"] != float64(2) || details["currentCompany"].(map[string]any)["notes"] != "Recruiter met" {
		t.Fatalf("company conflict details: %+v", details)
	}
	status, body = h.owner("POST", "/companies/"+id+"/archive", `{"expectedRevision":2}`)
	requireStatus(t, status, 200, body)
	if got := decodeObject(t, body)["company"].(map[string]any)["revision"]; got != float64(3) {
		t.Fatalf("company archive revision: %v", got)
	}
	status, body = h.owner("GET", "/companies")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("archived company in default list: %s", body)
	}
	status, body = h.owner("GET", "/companies?includeArchived=true")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 2 {
		t.Fatalf("archived company not in requested list: %s", body)
	}
}

func TestChangedSinceWatermarkExcludesLaterMutation(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	for i := 0; i < 2; i++ {
		status, body := h.owner("POST", "/companies", fmt.Sprintf(`{"name":"Company %d"}`, i))
		requireStatus(t, status, 201, body)
	}
	status, body := h.owner("GET", "/changes?after=0&limit=1&entityKind=company")
	requireStatus(t, status, 200, body)
	first := decodeObject(t, body)
	watermark := int64(first["watermark"].(float64))
	cursor := first["nextCursor"].(string)
	status, body = h.owner("POST", "/companies", `{"name":"Later company"}`)
	requireStatus(t, status, 201, body)
	status, body = h.owner("GET", "/changes?cursor="+cursor+"&limit=1&entityKind=company")
	requireStatus(t, status, 200, body)
	second := decodeObject(t, body)
	if int64(second["watermark"].(float64)) != watermark || len(second["items"].([]any)) != 1 || second["nextCursor"] != nil {
		t.Fatalf("finite cursor batch: %+v", second)
	}
	status, body = h.owner("GET", "/changes?after="+strconv.FormatInt(watermark, 10)+"&entityKind=company")
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("later change missing from next poll: %s", body)
	}
	status, body = h.owner("GET", "/changes?cursor="+cursor+"&after=0&entityKind=company")
	requireStatus(t, status, 400, body)
}
