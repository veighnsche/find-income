package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func fixturePosting(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"text":"Backend Engineer","descriptionPlain":"Build Go services.","hostedUrl":%q}`,
		id, "https://jobs.lever.co/example/"+id))
}

func leverBoard() store.CollectorBoard {
	return store.CollectorBoard{ID: "board-1", CollectorBoardInput: store.CollectorBoardInput{
		Provider: "lever", Site: "example", Region: "global"}}
}

func TestCommissionedLeverResumesInsideExactPage(t *testing.T) {
	postings := make([]json.RawMessage, 26)
	for i := range postings {
		postings[i] = fixturePosting(strconv.Itoa(i + 1))
	}
	var mu sync.Mutex
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "25" ||
			r.URL.Query().Get("mode") != "json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		skip, err := strconv.Atoi(r.URL.Query().Get("skip"))
		if err != nil || skip < 0 || skip > len(postings) {
			t.Errorf("bad offset: %q", r.URL.Query().Get("skip"))
			return
		}
		mu.Lock()
		offsets = append(offsets, skip)
		mu.Unlock()
		end := skip + pageLimit
		if end > len(postings) {
			end = len(postings)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(postings[skip:end])
	}))
	defer server.Close()
	c := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL },
		Now: func() time.Time { return time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC) }}
	request := Request{Board: leverBoard(), MaxPages: 1, MaxItems: 3}
	first, err := c.AcquireLever(context.Background(), request)
	if err != nil || first.PagesFetched != 1 || first.ItemsExamined != 3 || len(first.Postings) != 3 ||
		first.Next == nil || len(first.Next.Pending) != 22 || first.Next.NextOffset != 25 {
		t.Fatalf("first bounded batch: %+v %v", first, err)
	}
	encoded, err := json.Marshal(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	var saved Cursor
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	request.Cursor, request.MaxPages, request.MaxItems = saved, 0, 22
	second, err := c.AcquireLever(context.Background(), request)
	if err != nil || second.PagesFetched != 0 || second.ItemsExamined != 22 || len(second.Postings) != 22 ||
		second.Next == nil || len(second.Next.Pending) != 0 || second.Next.NextOffset != 25 {
		t.Fatalf("buffer continuation: %+v %v", second, err)
	}
	request.Cursor, request.MaxPages, request.MaxItems = *second.Next, 1, 2
	third, err := c.AcquireLever(context.Background(), request)
	if err != nil || third.PagesFetched != 1 || third.ItemsExamined != 1 || len(third.Postings) != 1 || third.Next != nil {
		t.Fatalf("final page: %+v %v", third, err)
	}
	mu.Lock()
	gotOffsets := append([]int(nil), offsets...)
	mu.Unlock()
	if len(gotOffsets) != 2 || gotOffsets[0] != 0 || gotOffsets[1] != 25 {
		t.Fatalf("page was fetched again or allowance exceeded: %v", gotOffsets)
	}
	all := append(append(first.Postings, second.Postings...), third.Postings...)
	if len(all) != 26 {
		t.Fatalf("lost items: %d", len(all))
	}
	for i, item := range all {
		id := strconv.Itoa(i + 1)
		raw := fixturePosting(id)
		digest := sha256.Sum256(raw)
		if item.ExternalID != id || string(item.OriginalText) != string(raw) ||
			item.ContentSHA256 != hex.EncodeToString(digest[:]) ||
			item.SourceURL != "https://jobs.lever.co/example/"+id ||
			item.BoardID != "board-1" || item.Provider != "lever" || item.ObservedAt == "" {
			t.Fatalf("posting %d lost source identity or exact evidence: %+v", i, item)
		}
	}
}

func TestInvalidPostingConsumesItemAllowanceAndDoesNotBlockNext(t *testing.T) {
	valid := fixturePosting("good")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"bad","text":"Role","descriptionPlain":"Text","hostedUrl":"https://other.example/bad"},` + string(valid) + `]`))
	}))
	defer server.Close()
	c := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	first, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), MaxPages: 1, MaxItems: 1})
	if err != nil || first.ItemsExamined != 1 || first.Rejected != 1 || len(first.Postings) != 0 ||
		first.WarningCode != "lever_invalid_posting" || first.Next == nil || len(first.Next.Pending) != 1 {
		t.Fatalf("invalid item bypassed allowance: %+v %v", first, err)
	}
	second, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), Cursor: *first.Next, MaxPages: 0, MaxItems: 1})
	if err != nil || second.ItemsExamined != 1 || len(second.Postings) != 1 || second.Postings[0].ExternalID != "good" || second.Next != nil {
		t.Fatalf("valid next item lost: %+v %v", second, err)
	}
}

func TestFailedReadKeepsCursorAndNeverSignalsClosure(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + string(fixturePosting("open")) + "]"))
	}))
	defer server.Close()
	c := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	first, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), MaxPages: 1, MaxItems: 1})
	if err != nil || first.ErrorCode != "lever_http_status" || first.PagesFetched != 1 || first.Next == nil ||
		first.Next.NextOffset != 0 || first.Next.EndOfBoard || len(first.Postings) != 0 {
		t.Fatalf("failed read advanced or closed source: %+v %v", first, err)
	}
	second, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), Cursor: *first.Next, MaxPages: 1, MaxItems: 1})
	if err != nil || len(second.Postings) != 1 || second.Postings[0].ExternalID != "open" || second.Next != nil {
		t.Fatalf("failed read could not retry: %+v %v", second, err)
	}
}

func TestCancellationStopsFurtherAcquisition(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		var page []json.RawMessage
		for i := 0; i < pageLimit; i++ {
			page = append(page, fixturePosting(strconv.Itoa(i)))
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL },
		Now: func() time.Time { cancel(); return time.Now() }}
	batch, err := c.AcquireLever(ctx, Request{Board: leverBoard(), MaxPages: 3, MaxItems: 50})
	if err != nil || !batch.Interrupted || calls != 1 || batch.PagesFetched != 1 ||
		batch.ItemsExamined != 0 || len(batch.Postings) != 0 || batch.Next == nil || len(batch.Next.Pending) != 25 {
		t.Fatalf("cancellation lost fetched page or continued: %+v calls=%d err=%v", batch, calls, err)
	}
}

func TestPersistedCursorPreservesExactProviderBytes(t *testing.T) {
	raw := json.RawMessage(`{ "id": "second", "text": "Engineer", "descriptionPlain": "Build <api> & services", "hostedUrl": "https://jobs.lever.co/example/second" }`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(append(append([]byte("["), fixturePosting("first")...), ','), append(raw, ']')...))
	}))
	defer server.Close()
	c := &Collector{endpoint: func(store.CollectorBoard) string { return server.URL }}
	first, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), MaxPages: 1, MaxItems: 1})
	if err != nil || first.Next == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	encoded, err := json.Marshal(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	var restored Cursor
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	next, err := c.AcquireLever(context.Background(), Request{Board: leverBoard(), Cursor: restored, MaxItems: 1})
	if err != nil || len(next.Postings) != 1 {
		t.Fatalf("next: %+v %v", next, err)
	}
	digest := sha256.Sum256(raw)
	if string(next.Postings[0].OriginalText) != string(raw) || next.Postings[0].ContentSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("cursor JSON roundtrip rewrote provider bytes and their source hash")
	}
	stagedJSON, err := json.Marshal(next.Postings[0])
	if err != nil {
		t.Fatal(err)
	}
	var restoredPosting StagedPosting
	if err := json.Unmarshal(stagedJSON, &restoredPosting); err != nil {
		t.Fatal(err)
	}
	if string(restoredPosting.OriginalText) != string(raw) || restoredPosting.ContentSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("staged posting JSON roundtrip rewrote provider bytes")
	}
}
