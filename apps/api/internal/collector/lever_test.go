package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func fixturePosting(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"text":"Backend Engineer","descriptionPlain":"Build Go services.","lists":[{"text":"Requirements","content":"<li>Go</li>"}],"hostedUrl":%q}`,
		id, "https://jobs.lever.co/example/"+id))
}

func setupCollector(t *testing.T, endpoint string) (*store.Store, store.CollectorBoard, *Collector) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seeded, err := db.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range seeded {
		if _, err := db.UpdateCollectorBoard(ctx, store.Actor{Kind: "administrator", ID: "owner"},
			item.ID, item.Revision, false, item.IntervalMinutes); err != nil {
			t.Fatal(err)
		}
	}
	board, err := db.CreateCollectorBoard(ctx, store.Actor{Kind: "administrator", ID: "owner"},
		store.CollectorBoardInput{Provider: "lever", Site: "example", Region: "global", Enabled: true, IntervalMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)
	c := &Collector{Store: db, PollInterval: time.Second, Lease: time.Minute, Now: func() time.Time { return now },
		endpoint: func(store.CollectorBoard) string { return endpoint }}
	return db, board, c
}

func TestLeverCollectorSubmitsExactSourceAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	raw := fixturePosting("posting-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("skip") != "0" ||
			r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("mode") != "json" ||
			r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + string(raw) + "]"))
	}))
	defer server.Close()
	db, board, collector := setupCollector(t, server.URL)
	report, err := collector.RunDueOnce(ctx)
	if err != nil || !report.Found || report.Submitted != 1 || report.NextOffset != 0 || report.ErrorCode != "" {
		t.Fatalf("initial scan: %+v %v", report, err)
	}
	page, err := db.ListIngestions(ctx, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("shared ingestion queue: %+v %v", page, err)
	}
	item := page.Items[0]
	if item.OriginalText != string(raw) || item.SourceURL != "https://jobs.lever.co/example/posting-1" ||
		item.ExternalID != "posting-1" || item.ConnectorID != "lever:"+board.ID ||
		item.Actor != (store.Actor{Kind: "system", ID: "collector:" + board.ID}) || item.Status != "pending" {
		t.Fatalf("source or attribution changed: %+v", item)
	}
	if again, err := collector.RunDueOnce(ctx); err != nil || again.Found {
		t.Fatalf("board rescanned before due: %+v %v", again, err)
	}
	previous := collector.Now()
	collector.Now = func() time.Time { return previous.Add(16 * time.Minute) }
	report, err = collector.RunDueOnce(ctx)
	if err != nil || report.Submitted != 0 || report.Duplicates != 1 || report.ErrorCode != "" {
		t.Fatalf("repeat scan duplicated intake: %+v %v", report, err)
	}
}

func TestLeverCollectorResumesBoundedPages(t *testing.T) {
	ctx := context.Background()
	postings := make([]json.RawMessage, 51)
	for i := range postings {
		postings[i] = fixturePosting(strconv.Itoa(i + 1))
	}
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		offsets = append(offsets, skip)
		end := skip + pageLimit
		if end > len(postings) {
			end = len(postings)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(postings[skip:end])
	}))
	defer server.Close()
	db, board, collector := setupCollector(t, server.URL)
	report, err := collector.RunDueOnce(ctx)
	if err != nil || report.Submitted != 50 || report.NextOffset != 50 || report.ErrorCode != "" {
		t.Fatalf("first bounded batch: %+v %v", report, err)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 25 {
		t.Fatalf("unbounded or wrong requests: %+v", offsets)
	}
	boards, err := db.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var current store.CollectorBoard
	for _, item := range boards {
		if item.ID == board.ID {
			current = item
		}
	}
	if current.ID == "" || current.NextScanAt.Sub(collector.Now()) != time.Minute {
		t.Fatalf("partial sweep waited full interval: %+v", current)
	}
	previous := collector.Now()
	collector.Now = func() time.Time { return previous.Add(2 * time.Minute) }
	report, err = collector.RunDueOnce(ctx)
	if err != nil || report.Submitted != 1 || report.NextOffset != 0 || report.ErrorCode != "" {
		t.Fatalf("resumed batch: %+v %v", report, err)
	}
	boards, err = db.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current = store.CollectorBoard{}
	for _, item := range boards {
		if item.ID == board.ID {
			current = item
		}
	}
	if current.ID == "" || current.NextOffset != 0 {
		t.Fatalf("cursor not persisted: %+v %v", boards, err)
	}
	page, err := db.ListIngestions(ctx, "", 100)
	if err != nil || len(page.Items) != 51 {
		t.Fatalf("bounded scans lost postings: count=%d err=%v", len(page.Items), err)
	}
}

func TestLeverCollectorSkipsUntrustedPostingAndSubmitsNext(t *testing.T) {
	ctx := context.Background()
	valid := fixturePosting("good-123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"123","text":"Backend Engineer","descriptionPlain":"Build Go services.","hostedUrl":"https://other.example/123"},` + string(valid) + `]`))
	}))
	defer server.Close()
	db, board, collector := setupCollector(t, server.URL)
	report, err := collector.RunDueOnce(ctx)
	if err != nil || report.ErrorCode != "" || report.WarningCode != "lever_invalid_posting" ||
		report.Rejected != 1 || report.Submitted != 1 {
		t.Fatalf("bad posting blocked valid next entry: %+v %v", report, err)
	}
	page, err := db.ListIngestions(ctx, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ExternalID != "good-123" {
		t.Fatalf("wrong source queued: %+v %v", page, err)
	}
	boards, err := db.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var warning string
	for _, item := range boards {
		if item.ID == board.ID {
			warning = item.LastErrorCode
		}
	}
	if warning != "lever_invalid_posting" {
		t.Fatalf("skipped posting not reported safely: %+v %v", boards, err)
	}
}
