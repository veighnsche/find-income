package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func evidenceVersions(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	versions, ok := value["currentInputVersions"].(map[string]any)
	if !ok {
		t.Fatalf("missing current versions: %#v", value)
	}
	return versions
}

func evidenceFixture(t *testing.T, h *recordHTTP) (string, string, auth.Principal) {
	t.Helper()
	owner := h.login()
	status, body := h.owner("POST", "/companies", `{"name":"Synthetic Evidence Co"}`)
	requireStatus(t, status, 201, body)
	companyID := decodeObject(t, body)["company"].(map[string]any)["id"].(string)
	status, body = h.owner("POST", "/opportunities", fmt.Sprintf(`{"companyId":%q,"title":"Backend Engineer","kind":"employment","stage":"saved","sourceUrl":"https://jobs.example.test/evidence","originalText":"Backend role at 32 hours"}`, companyID))
	requireStatus(t, status, 201, body)
	result := decodeObject(t, body)
	return result["opportunity"].(map[string]any)["id"].(string), result["changeId"].(string), owner
}

func TestEvidenceHTTPScopesQuotesConflictsAndHistory(t *testing.T) {
	h := newRecordHTTP(t)
	opportunityID, _, owner := evidenceFixture(t, h)
	path := "/opportunities/" + opportunityID
	_, writerAToken, err := h.service.CreateAgent(context.Background(), owner, "evidence-a", []string{"evidence:write", "opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, writerBToken, err := h.service.CreateAgent(context.Background(), owner, "evidence-b", []string{"evidence:write", "opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, readerToken, err := h.service.CreateAgent(context.Background(), owner, "evidence-reader", []string{"opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.do("GET", path+"/qualification", "", "", "", "", nil)
	requireStatus(t, status, 401, body)
	status, body = h.do("GET", path+"/qualification", "", writerAToken, "", "", nil)
	requireStatus(t, status, 200, body)
	initial := decodeObject(t, body)
	versions := evidenceVersions(t, initial)
	if initial["status"] != "current" || initial["current"] == nil {
		t.Fatalf("initial qualification: %s", body)
	}
	statement := "Monthly base €5,000 for 32 hours; backend platform role."
	create := fmt.Sprintf(`{"expectedContextVersion":%.0f,"statement":{"speakerAffiliation":"employer_representative","speakerName":"Synthetic Hiring Lead","speakerRole":"Hiring Lead","speakerOrganisation":"Synthetic Evidence Co","channel":"email","occurredAt":"2026-09-23T10:00:00Z","originalText":%q}}`, versions["contextVersion"].(float64), statement)
	status, body = h.do("POST", path+"/evidence-sources", create, readerToken, "", "", nil)
	requireStatus(t, status, 403, body)
	status, body = h.do("POST", path+"/evidence-sources", create, writerAToken, "", "", nil)
	requireStatus(t, status, 201, body)
	sourceResult := decodeObject(t, body)
	source := sourceResult["source"].(map[string]any)
	sourceID := source["id"].(string)
	if source["sourceKind"] != "employer_statement" || source["actorId"] == "owner" || source["originalText"] != statement {
		t.Fatalf("source attribution: %#v", source)
	}
	version := evidenceVersions(t, sourceResult)["evidenceVersion"].(float64)
	start := strings.Index(statement, "€5,000")
	end := start + len("€5,000")
	missingFacts := []string{
		fmt.Sprintf(`{"sourceId":%q,"criterion":"monthly_base_salary","finding":"explicit_match","observedValue":"actual_pay_terms","spanStart":%d,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"salary":{"currency":"EUR","period":"month","basis":"base","actualWeeklyHours":32}}`, sourceID, start, end, version),
		fmt.Sprintf(`{"sourceId":%q,"criterion":"target_hours_available","finding":"explicit_match","observedValue":"weekly_hours_available","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"hours":{"minWeekly":32,"maxWeekly":32}}`, sourceID, version),
		fmt.Sprintf(`{"sourceId":%q,"criterion":"location_arrangement","finding":"explicit_match","observedValue":"work_arrangement","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"arrangement":{"pattern":"remote","remoteGeography":"Belgium"}}`, sourceID, version),
	}
	for _, invalid := range missingFacts {
		status, body = h.do("POST", path+"/evidence", invalid, writerAToken, "", "", nil)
		requireStatus(t, status, 400, body)
	}
	status, body = h.do("GET", path+"/qualification", "", writerAToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if evidenceVersions(t, decodeObject(t, body))["evidenceVersion"] != version {
		t.Fatalf("missing nested fact changed evidence version: %s", body)
	}
	claim := fmt.Sprintf(`{"sourceId":%q,"criterion":"monthly_base_salary","finding":"explicit_match","observedValue":"actual_pay_terms","spanStart":%d,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"salary":{"currency":"EUR","period":"month","basis":"base","amountCents":500000,"actualWeeklyHours":32}}`, sourceID, start, end, version)
	status, body = h.do("POST", path+"/evidence", claim, writerAToken, "", "", nil)
	requireStatus(t, status, 201, body)
	claimResult := decodeObject(t, body)
	item := claimResult["evidence"].(map[string]any)
	if item["sourceExcerpt"] != "€5,000" || item["spanStart"] != float64(start) || item["spanEnd"] != float64(end) || item["hasSpan"] != true || item["legacyUnverified"] != false {
		t.Fatalf("exact quote provenance: %#v", item)
	}
	claimID := item["id"].(string)
	status, body = h.do("POST", path+"/evidence", claim, writerBToken, "", "", nil)
	requireStatus(t, status, 409, body)
	conflict := decodeObject(t, body)["error"].(map[string]any)["details"].(map[string]any)
	if evidenceVersions(t, conflict)["evidenceVersion"].(float64) <= version {
		t.Fatalf("stale conflict details: %#v", conflict)
	}
	status, body = h.do("GET", path+"/qualification", "", readerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	qualified := decodeObject(t, body)
	if qualified["status"] != "current" || qualified["current"].(map[string]any)["salary"].(map[string]any)["confirmedActual"] != true {
		t.Fatalf("sourced salary not in current qualification: %s", body)
	}
	currentID := qualified["current"].(map[string]any)["id"].(string)
	status, body = h.do("GET", path+"/qualification", "", readerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if decodeObject(t, body)["current"].(map[string]any)["id"] != currentID {
		t.Fatalf("current read created an unnecessary evaluation: %s", body)
	}
	status, body = h.do("GET", path+"/qualification/history?limit=1", "", readerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("history page: %s", body)
	}
	status, body = h.do("GET", path+"/evidence/"+claimID, "", readerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if decodeObject(t, body)["sourceExcerpt"] != "€5,000" {
		t.Fatalf("claim read: %s", body)
	}
	status, body = h.owner("POST", path+"/qualification/reevaluate", `{}`)
	requireStatus(t, status, 400, body)
	status, body = h.do("POST", path+"/qualification/reevaluate", "", writerAToken, "", "", nil)
	requireStatus(t, status, 200, body)
	manual := decodeObject(t, body)
	if manual["changeId"] == "" || manual["evaluation"].(map[string]any)["actorKind"] != "agent" {
		t.Fatalf("explicit reevaluation attribution: %s", body)
	}
	status, body = h.owner("PATCH", path, `{"expectedRevision":1,"originalText":"Updated vacancy text at 32 hours"}`)
	requireStatus(t, status, 200, body)
	status, body = h.do("GET", path+"/qualification", "", readerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	refreshed := decodeObject(t, body)
	if refreshed["current"].(map[string]any)["id"] == currentID || refreshed["current"].(map[string]any)["salary"].(map[string]any)["confirmedActual"] != true {
		t.Fatalf("direct statement did not survive material refresh: %s", body)
	}
	currentVersions := evidenceVersions(t, refreshed)
	zeroStatement := "Base €0 for 32 hours."
	zeroSource := fmt.Sprintf(`{"expectedContextVersion":%.0f,"statement":{"speakerAffiliation":"employer_representative","speakerName":"Synthetic Hiring Lead","speakerRole":"Hiring Lead","speakerOrganisation":"Synthetic Evidence Co","channel":"email","occurredAt":"2026-09-23T11:00:00Z","originalText":%q}}`, currentVersions["contextVersion"].(float64), zeroStatement)
	status, body = h.do("POST", path+"/evidence-sources", zeroSource, writerAToken, "", "", nil)
	requireStatus(t, status, 201, body)
	zeroSourceID := decodeObject(t, body)["source"].(map[string]any)["id"].(string)
	zeroVersion := evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	zeroStart := strings.Index(zeroStatement, "€0")
	zeroClaim := fmt.Sprintf(`{"sourceId":%q,"criterion":"monthly_base_salary","finding":"explicit_match","observedValue":"actual_pay_terms","spanStart":%d,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"salary":{"currency":"EUR","period":"month","basis":"base","amountCents":0,"actualWeeklyHours":32}}`, zeroSourceID, zeroStart, zeroStart+len("€0"), zeroVersion)
	status, body = h.do("POST", path+"/evidence", zeroClaim, writerAToken, "", "", nil)
	requireStatus(t, status, 201, body)
	if decodeObject(t, body)["evidence"].(map[string]any)["salary"].(map[string]any)["amountCents"] != float64(0) {
		t.Fatalf("explicit zero salary was not preserved: %s", body)
	}
}

func TestEvidenceHTTPRejectsForgeryCrossOpportunityAndOwnerObservation(t *testing.T) {
	h := newRecordHTTP(t)
	opportunityID, changeID, owner := evidenceFixture(t, h)
	path := "/opportunities/" + opportunityID
	_, writerToken, err := h.service.CreateAgent(context.Background(), owner, "evidence-writer", []string{"evidence:write", "opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.do("GET", path+"/qualification", "", writerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	versions := evidenceVersions(t, decodeObject(t, body))
	contextVersion := versions["contextVersion"].(float64)
	preferencesVersion := versions["preferencesVersion"].(float64)
	vacancy := fmt.Sprintf(`{"expectedContextVersion":%.0f,"vacancySnapshot":{"recordChangeAuditId":%q}}`, contextVersion, changeID)
	status, body = h.do("POST", path+"/evidence-sources", vacancy, "", "", origin, h.cookie)
	requireStatus(t, status, 403, body)
	status, body = h.owner("POST", path+"/evidence-sources", vacancy)
	requireStatus(t, status, 201, body)
	sourceID := decodeObject(t, body)["source"].(map[string]any)["id"].(string)
	version := evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	ownerSource := fmt.Sprintf(`{"expectedContextVersion":%.0f,"ownerObservation":{"occurredAt":"2026-09-23T10:00:00Z","originalText":"I could work with the arrangement.","expectedPreferencesVersion":%.0f}}`, contextVersion, preferencesVersion)
	status, body = h.do("POST", path+"/evidence-sources", ownerSource, writerToken, "", "", nil)
	requireStatus(t, status, 403, body)
	status, body = h.owner("POST", path+"/evidence-sources", ownerSource)
	requireStatus(t, status, 201, body)
	staleContext := fmt.Sprintf(`{"expectedContextVersion":%.0f,"statement":{"speakerAffiliation":"recruiter","speakerName":"Synthetic Recruiter","speakerRole":"Recruiter","speakerOrganisation":"Synthetic Agency","channel":"call","occurredAt":"2026-09-23T10:00:00Z","originalText":"Backend scope."}}`, contextVersion+1)
	status, body = h.do("POST", path+"/evidence-sources", staleContext, writerToken, "", "", nil)
	requireStatus(t, status, 409, body)
	stalePreference := fmt.Sprintf(`{"expectedContextVersion":%.0f,"ownerObservation":{"occurredAt":"2026-09-23T10:00:00Z","originalText":"Workable.","expectedPreferencesVersion":%.0f}}`, contextVersion, preferencesVersion+1)
	status, body = h.owner("POST", path+"/evidence-sources", stalePreference)
	requireStatus(t, status, 409, body)
	badBodies := []string{
		fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"actor":{"kind":"administrator","id":"owner"}}`, sourceID, version),
		fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"authority":"employer"}`, sourceID, version),
		fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"result":"qualified"}`, sourceID, version),
		fmt.Sprintf(`{"sourceId":%q,"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f}`, sourceID, sourceID, version),
		fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f,"hours":null}`, sourceID, version),
	}
	for _, invalid := range badBodies {
		status, body = h.do("POST", path+"/evidence", invalid, writerToken, "", "", nil)
		requireStatus(t, status, 400, body)
	}
	invalidUTF8 := []byte(fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"`, sourceID))
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(fmt.Sprintf(`","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f}`, version))...)
	status, body = h.do("POST", path+"/evidence", string(invalidUTF8), writerToken, "", "", nil)
	requireStatus(t, status, 400, body)
	status, body = h.do("GET", path+"/evidence", "", writerToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 0 {
		t.Fatalf("invalid requests inserted evidence: %s", body)
	}

	status, body = h.owner("POST", "/opportunities", fmt.Sprintf(`{"companyId":%q,"title":"Other","kind":"employment","stage":"saved","originalText":"Other role"}`, versions["companyId"].(string)))
	requireStatus(t, status, 201, body)
	otherID := decodeObject(t, body)["opportunity"].(map[string]any)["id"].(string)
	status, body = h.do("GET", "/opportunities/"+otherID+"/evidence-sources/"+sourceID, "", writerToken, "", "", nil)
	requireStatus(t, status, 404, body)
	claim := fmt.Sprintf(`{"sourceId":%q,"criterion":"backend_platform","finding":"explicit_match","observedValue":"backend_primary","spanStart":0,"spanEnd":7,"expectedEvidenceVersion":%.0f}`, sourceID, version)
	status, body = h.do("POST", "/opportunities/"+otherID+"/evidence", claim, writerToken, "", "", nil)
	requireStatus(t, status, 404, body)
	status, body = h.do("POST", path+"/evidence", claim, writerToken, "", "", nil)
	requireStatus(t, status, 409, body) // owner source changed evidence version
}

func TestEvidenceHTTPSupersessionAndOwnerWorkability(t *testing.T) {
	h := newRecordHTTP(t)
	opportunityID, _, owner := evidenceFixture(t, h)
	path := "/opportunities/" + opportunityID
	_, agentToken, err := h.service.CreateAgent(context.Background(), owner, "evidence-agent", []string{"evidence:write", "opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.do("GET", path+"/qualification", "", agentToken, "", "", nil)
	requireStatus(t, status, 200, body)
	versions := evidenceVersions(t, decodeObject(t, body))
	statement := "Hybrid in Amsterdam with two office days."
	create := fmt.Sprintf(`{"expectedContextVersion":%.0f,"statement":{"speakerAffiliation":"recruiter","speakerName":"Synthetic Recruiter","speakerRole":"Recruiter","speakerOrganisation":"Synthetic Agency","channel":"call","occurredAt":"2026-09-23T10:00:00Z","originalText":%q}}`, versions["contextVersion"].(float64), statement)
	status, body = h.do("POST", path+"/evidence-sources", create, agentToken, "", "", nil)
	requireStatus(t, status, 201, body)
	sourceID := decodeObject(t, body)["source"].(map[string]any)["id"].(string)
	version := evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	arrangement := fmt.Sprintf(`{"sourceId":%q,"criterion":"location_arrangement","finding":"explicit_match","observedValue":"work_arrangement","spanStart":0,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"arrangement":{"pattern":"hybrid","baseLocation":"Amsterdam","remoteGeography":"","onsiteDays":2}}`, sourceID, len(statement), version)
	status, body = h.do("POST", path+"/evidence", arrangement, agentToken, "", "", nil)
	requireStatus(t, status, 201, body)
	arrangementID := decodeObject(t, body)["evidence"].(map[string]any)["id"].(string)
	version = evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	replacement := fmt.Sprintf(`{"sourceId":%q,"criterion":"location_arrangement","finding":"explicit_match","observedValue":"work_arrangement","spanStart":0,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"arrangement":{"pattern":"hybrid","baseLocation":"Amsterdam","remoteGeography":"","onsiteDays":2}}`, sourceID, len(statement), version)
	status, body = h.do("POST", path+"/evidence/"+arrangementID+"/supersede", replacement, agentToken, "", "", nil)
	requireStatus(t, status, 201, body)
	newID := decodeObject(t, body)["evidence"].(map[string]any)["id"].(string)
	version = evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	status, body = h.do("POST", path+"/evidence/"+arrangementID+"/supersede", strings.Replace(replacement, fmt.Sprintf(`"expectedEvidenceVersion":%.0f`, version-1), fmt.Sprintf(`"expectedEvidenceVersion":%.0f`, version), 1), agentToken, "", "", nil)
	requireStatus(t, status, 409, body)
	status, body = h.do("GET", path+"/evidence", "", agentToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 1 {
		t.Fatalf("leaf page: %s", body)
	}
	status, body = h.do("GET", path+"/evidence?includeSuperseded=true", "", agentToken, "", "", nil)
	requireStatus(t, status, 200, body)
	if len(decodeObject(t, body)["items"].([]any)) != 2 {
		t.Fatalf("history page: %s", body)
	}
	ownerText := "The two office days are workable for me."
	ownerSource := fmt.Sprintf(`{"expectedContextVersion":%.0f,"ownerObservation":{"occurredAt":"2026-09-23T11:00:00Z","originalText":%q,"expectedPreferencesVersion":%.0f}}`, versions["contextVersion"].(float64), ownerText, versions["preferencesVersion"].(float64))
	status, body = h.owner("POST", path+"/evidence-sources", ownerSource)
	requireStatus(t, status, 201, body)
	ownerSourceID := decodeObject(t, body)["source"].(map[string]any)["id"].(string)
	version = evidenceVersions(t, decodeObject(t, body))["evidenceVersion"].(float64)
	workable := fmt.Sprintf(`{"sourceId":%q,"criterion":"location_workable","finding":"explicit_match","observedValue":"workable","spanStart":0,"spanEnd":%d,"expectedEvidenceVersion":%.0f,"ownerWorkableForEvidenceId":%q}`, ownerSourceID, len(ownerText), version, newID)
	status, body = h.do("POST", path+"/evidence", workable, agentToken, "", "", nil)
	requireStatus(t, status, 403, body)
	status, body = h.owner("POST", path+"/evidence", workable)
	requireStatus(t, status, 201, body)
	if decodeObject(t, body)["evidence"].(map[string]any)["ownerWorkableForEvidenceId"] != newID {
		t.Fatalf("owner arrangement link: %s", body)
	}
}

func TestEvidenceHTTPRefreshFailureNeverReportsCurrent(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	service := auth.NewService(db)
	if err := service.SetupAdministrator(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(db, service, Options{AllowedOrigins: []string{origin}}))
	t.Cleanup(func() { server.Close(); _ = db.Close() })
	h := &recordHTTP{t: t, client: server.Client(), server: server, db: db, service: service}
	opportunityID, _, _ := evidenceFixture(t, h)
	path := "/opportunities/" + opportunityID
	status, body := h.owner("GET", path+"/qualification")
	requireStatus(t, status, 200, body)
	if decodeObject(t, body)["status"] != "current" {
		t.Fatalf("initial qualification: %s", body)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, "jobseek.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE qualification_input_versions SET material_version=material_version+1 WHERE opportunity_id=?`, opportunityID); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE compensation`); err != nil {
		t.Fatal(err)
	}
	status, body = h.owner("GET", path+"/qualification")
	requireStatus(t, status, 503, body)
	view := decodeObject(t, body)
	if view["status"] != "outdated" || view["current"] != nil || view["latestHistorical"] == nil {
		t.Fatalf("refresh failure promoted stale result: %s", body)
	}
	if view["latestHistorical"].(map[string]any)["id"] == "" {
		t.Fatalf("historical result missing identity: %s", body)
	}
}
