package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/discovery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type verificationTransport func(*http.Request) (*http.Response, error)

func (f verificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func protocolText(t string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": t}}}})
	return "event: message\ndata: " + string(b) + "\n\n"
}

func TestDiscoveredOfficialLinkRegistersAndCollects(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "agent"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "discover-newco", Intent: "Find a platform role", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{Resources: []string{"discovery:himalayas"}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource, store.RoundStageDiscovery, store.RoundFetchSource, store.RoundRegisterDiscoveryBoard, store.RoundCollectorPage}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 7, Items: 3, Tools: 10, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	searchText := "Found 1 jobs matching 'platform' (showing page 1)\n🚀 **Platform Engineer**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/newco/jobs/platform-engineer\n"
	detailText := "# NewCo ✅ Verified\n\n## Links\n🌐 **Website:** https://newco.com?ref=himalayas\n"
	discoveryHTTP := &http.Client{Transport: verificationTransport(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		text := searchText
		if strings.Contains(string(body), "get_company_details") {
			text = detailText
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(protocolText(text))), Request: req}, nil
	})}
	reader := &discovery.Reader{Store: db, Client: discoveryHTTP}
	search, err := reader.Read(ctx, discovery.Input{RoundID: r.ID, Capability: capability, RequestKey: "search", ResourceID: "discovery:himalayas", Method: "search_jobs", Keyword: "platform", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	quote := "🚀 **Platform Engineer**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/newco/jobs/platform-engineer"
	stage, err := db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: r.ID, Capability: capability, RequestKey: "candidate", Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform Engineer", URL: "https://himalayas.app/companies/newco/jobs/platform-engineer", EvidenceQuote: quote}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := reader.Read(ctx, discovery.Input{RoundID: r.ID, Capability: capability, RequestKey: "company-detail", ResourceID: "discovery:himalayas", Method: "get_company_details", CompanySlug: "newco"})
	if err != nil {
		t.Fatal(err)
	}
	otherDetail, err := reader.Read(ctx, discovery.Input{RoundID: r.ID, Capability: capability, RequestKey: "other-company-detail", ResourceID: "discovery:himalayas", Method: "get_company_details", CompanySlug: "otherco"})
	if err != nil {
		t.Fatal(err)
	}
	var officialCalls int
	svc := &Service{db: db, linkFetch: func(_ context.Context, url string, _ SourceLinkPage) (SourceLinksSnapshot, error) {
		officialCalls++
		var pending int
		if err := db.Read(ctx, func(rd store.Reader) error {
			return rd.QueryRowContext(ctx, `SELECT count(*) FROM discovery_official_reads WHERE source_url=? AND status='pending'`, url).Scan(&pending)
		}); err != nil || pending != 1 {
			t.Errorf("official URL not durable before GET: url=%s pending=%d err=%v", url, pending, err)
		}
		switch url {
		case "https://newco.com/":
			if officialCalls == 1 {
				return SourceLinksSnapshot{SourceURL: url, Status: "redirect", RedirectURL: "https://www.newco.com/"}, nil
			}
			return SourceLinksSnapshot{SourceURL: url, Status: "ok", Links: []SourceLink{{URL: "https://newco.com/careers", Text: "Careers"}}}, nil
		case "https://newco.com/careers":
			return SourceLinksSnapshot{SourceURL: url, Status: "ok", Links: []SourceLink{{URL: "https://boards.greenhouse.io/newco", Text: "Other board"}, {URL: "https://jobs.lever.co/newco/role-1", Text: "Platform Engineer"}}}, nil
		default:
			t.Errorf("unexpected official read %s", url)
			return SourceLinksSnapshot{}, errors.New("unexpected URL")
		}
	}}
	if _, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "wrong-detail", CandidateID: stage.CandidateID, CompanyDetailAttemptID: otherDetail.AttemptID}); !errors.Is(err, store.ErrFenced) || officialCalls != 0 {
		t.Fatalf("wrong company provenance %v calls=%d", err, officialCalls)
	}
	redirect, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "redirect-home", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID})
	if err != nil || redirect.Status != "redirect" {
		t.Fatalf("redirect %+v: %v", redirect, err)
	}
	if _, err := db.RegisterDiscoveryBoard(ctx, store.DiscoveryBoardInput{RoundID: r.ID, Capability: capability, RequestKey: "reject-redirect", CandidateID: stage.CandidateID, OfficialLinksAttemptID: redirect.AttemptID, LeverURL: "https://jobs.lever.co/newco/role-1"}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("redirect registered: %v", err)
	}
	first, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "official-home", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID})
	if err != nil || first.Status != "ok" {
		t.Fatalf("home %+v: %v", first, err)
	}
	replayedHome, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "official-home", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID})
	if err != nil || replayedHome.AttemptID != first.AttemptID || officialCalls != 2 {
		t.Fatalf("official replay %+v %v calls=%d", replayedHome, err, officialCalls)
	}
	beforeUnseen := officialCalls
	if _, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "unseen-careers", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID, ParentAttemptID: first.AttemptID, LinkURL: "https://newco.com/hidden"}); !errors.Is(err, store.ErrFenced) || officialCalls != beforeUnseen {
		t.Fatalf("unseen URL read %v calls=%d", err, officialCalls)
	}
	second, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "official-careers", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID, ParentAttemptID: first.AttemptID, LinkURL: "https://newco.com/careers"})
	if err != nil || second.Status != "ok" {
		t.Fatalf("careers %+v: %v", second, err)
	}
	if _, err := db.RegisterDiscoveryBoard(ctx, store.DiscoveryBoardInput{RoundID: r.ID, Capability: capability, RequestKey: "unsupported-provider", CandidateID: stage.CandidateID, OfficialLinksAttemptID: second.AttemptID, LeverURL: "https://boards.greenhouse.io/newco"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unsupported provider: %v", err)
	}
	boardResult, err := db.RegisterDiscoveryBoard(ctx, store.DiscoveryBoardInput{RoundID: r.ID, Capability: capability, RequestKey: "register", CandidateID: stage.CandidateID, OfficialLinksAttemptID: second.AttemptID, LeverURL: "https://jobs.lever.co/newco/role-1"})
	if err != nil {
		t.Fatal(err)
	}
	replayedBoard, err := db.RegisterDiscoveryBoard(ctx, store.DiscoveryBoardInput{RoundID: r.ID, Capability: capability, RequestKey: "register", CandidateID: stage.CandidateID, OfficialLinksAttemptID: second.AttemptID, LeverURL: "https://jobs.lever.co/newco/role-1"})
	if err != nil || replayedBoard.BoardID != boardResult.BoardID {
		t.Fatalf("board replay %+v: %v", replayedBoard, err)
	}
	boards, err := db.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var board store.CollectorBoard
	for _, b := range boards {
		if b.ID == boardResult.BoardID {
			board = b
			break
		}
	}
	if board.ID == "" || board.Site != "newco" || board.VerifiedAt == "" || board.OfficialCareersURL != "https://newco.com/careers" {
		t.Fatalf("verified board %+v", board)
	}
	acquisition, created, err := db.ReserveCollectorAcquisition(ctx, agent, r.ID, store.RoundCollectorAcquisitionInput{RequestKey: "collect-newco", BoardID: board.ID, MaxPages: 1, MaxItems: 1})
	if err != nil || !created {
		t.Fatalf("new board scope: %+v %v", acquisition, err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, acquisition.ID); err != nil {
		t.Fatal(err)
	}
	collectorHTTP := &http.Client{Transport: verificationTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.lever.co" || !strings.Contains(req.URL.Path, "/newco") {
			t.Errorf("unexpected collector URL %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`[{"id":"role-1","text":"Platform Engineer","descriptionPlain":"Build platform services.","hostedUrl":"https://jobs.lever.co/newco/role-1"}]`)), Request: req}, nil
	})}
	batch, err := (&collector.Collector{HTTPClient: collectorHTTP}).AcquireLever(ctx, collector.Request{Board: board, MaxPages: 1, MaxItems: 1})
	if err != nil || batch.ErrorCode != "" || len(batch.Postings) != 1 {
		t.Fatalf("collector %+v: %v", batch, err)
	}
	current, err := db.Round(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(batch)
	if _, err = db.SaveRoundCollectorBatch(ctx, owner, r.ID, acquisition.ID, current.Revision, payload); err != nil {
		t.Fatal(err)
	}
	outcomes, err := db.RoundCollectorOutcomes(ctx, r.ID, acquisition.ID)
	if err != nil || len(outcomes) != 1 || outcomes[0].SourceOpeningID == "" {
		t.Fatalf("full vacancy evidence %+v: %v", outcomes, err)
	}
	priorCalls := officialCalls
	if _, _, err := db.StopRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: capability, RequestKey: "stale-read", CandidateID: stage.CandidateID, CompanyDetailAttemptID: detail.AttemptID}); !errors.Is(err, store.ErrFenced) || officialCalls != priorCalls {
		t.Fatalf("stale capability %v calls=%d", err, officialCalls)
	}
}

func TestExpiredDiscoveryOfficialReadReleasesRound(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "expire-official", Intent: "inspect", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(500 * time.Millisecond), Scope: store.RoundScope{Resources: []string{"discovery:himalayas"}, Operations: []string{store.RoundFetchSource}}, Limits: store.RoundAllowance{Requests: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	var calls int
	svc := &Service{db: db, linkFetch: func(context.Context, string, SourceLinkPage) (SourceLinksSnapshot, error) {
		calls++
		return SourceLinksSnapshot{}, nil
	}}
	_, err = svc.RoundDiscoveryOfficialLinks(ctx, DiscoveryOfficialLinksArgs{RoundID: r.ID, Capability: "expired", RequestKey: "read", CandidateID: "candidate", CompanyDetailAttemptID: "detail"})
	if !errors.Is(err, store.ErrExpired) || calls != 0 {
		t.Fatalf("expired read %v calls=%d", err, calls)
	}
	current, err := db.Round(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State == store.RoundRunning {
		t.Fatalf("active slot retained after expiry: %s", current.State)
	}
}
