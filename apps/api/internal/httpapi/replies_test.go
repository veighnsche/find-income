package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type replyNoopWorker struct{}

func (replyNoopWorker) CheckRound(context.Context, string) error { return nil }
func (replyNoopWorker) LaunchRound(_ context.Context, r store.Round) error {
	if r.Outcome != "process_replies" || r.State != store.RoundRunning {
		return store.ErrFenced
	}
	return nil
}
func (replyNoopWorker) CancelRound(string) {}

func TestCorrespondenceImportPersistsSuppliedThread(t *testing.T) {
	h := newHarness(t)
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}})
	cookie, csrf := h.login()
	paste, _ := json.Marshal(map[string]any{"provider": "manual", "externalAccountId": "owner@example.test", "displayName": "Owner",
		"threads": []any{map[string]any{"providerThreadId": "pasted-1", "subject": "Pasted invite",
			"messages": []any{map[string]any{"providerMessageId": "pm-1", "sender": "recruiter@example.test", "recipients": []string{"owner@example.test"}, "sentAt": "2026-09-24T10:00:00Z", "body": "Pasted invitation body."}}}}})
	first := h.request(http.MethodPost, "/api/v1/correspondence/import", string(paste), cookie, "", csrf, origin)
	if first.Code != http.StatusOK {
		t.Fatalf("import: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		AccountID string `json:"accountId"`
		Threads   int64  `json:"threads"`
		Messages  int64  `json:"messages"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.Threads != 1 || result.Messages != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	second := h.request(http.MethodPost, "/api/v1/correspondence/import", string(paste), cookie, "", csrf, origin)
	var replay struct {
		Threads  int64 `json:"threads"`
		Messages int64 `json:"messages"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil || replay.Threads != 0 || replay.Messages != 0 {
		t.Fatalf("replay must deduplicate: %+v %v", replay, err)
	}
	list := h.request(http.MethodGet, "/api/v1/correspondence/threads", "", cookie, "", "", "")
	var listed struct {
		Items []struct {
			Subject string `json:"subject"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 || listed.Items[0].Subject != "Pasted invite" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func TestReplyCommissionReadsAndReplay(t *testing.T) {
	h := newHarness(t)
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: replyNoopWorker{}, Worker: replyNoopWorker{}}})
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	account, _, err := h.db.ConnectCorrespondenceAccount(ctx, owner, store.CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.db.SyncCorrespondenceThreads(ctx, owner, account.ID, []store.CorrespondenceThreadSnapshot{{ProviderThreadID: "thread-1", Subject: "Interview", LastMessageAt: "2026-09-24T10:00:00Z",
		Messages: []store.CorrespondenceMessageSnapshot{{ProviderMessageID: "m-1", Sender: "recruiter@example.test", Recipients: []string{"owner@example.test"}, SentAt: "2026-09-24T10:00:00Z", Body: "We invite you to interview Tuesday."}}}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := h.db.OwnerCorrespondenceThreads(ctx, owner.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %+v %v", threads, err)
	}
	cookie, csrf := h.login()
	body, _ := json.Marshal(map[string]string{"requestKey": "thread-one"})
	unauth := h.request(http.MethodPost, "/api/v1/correspondence/threads/"+threads[0].ID+"/process", string(body), nil, "", "", origin)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated commission: %d", unauth.Code)
	}
	started := h.request(http.MethodPost, "/api/v1/correspondence/threads/"+threads[0].ID+"/process", string(body), cookie, "", csrf, origin)
	if started.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", started.Code, started.Body.String())
	}
	var response struct {
		ProcessingID string `json:"processingId"`
		ThreadID     string `json:"threadId"`
		Round        struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &response); err != nil || response.ProcessingID == "" || response.Round.ID == "" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	commission, err := h.db.ReplyProcessing(ctx, response.ProcessingID)
	if err != nil || commission.RoundID != response.Round.ID {
		t.Fatalf("round not bound before launch: %+v %v", commission, err)
	}
	replay := h.request(http.MethodPost, "/api/v1/correspondence/threads/"+threads[0].ID+"/process", string(body), cookie, "", csrf, origin)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	list := h.request(http.MethodGet, "/api/v1/correspondence/threads", "", cookie, "", "", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d", list.Code)
	}
	var listed struct {
		Items []struct {
			ID           string `json:"id"`
			MessageCount int64  `json:"messageCount"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 || listed.Items[0].MessageCount != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	detail := h.request(http.MethodGet, "/api/v1/correspondence/threads/"+threads[0].ID, "", cookie, "", "", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail: %d", detail.Code)
	}
	var detailBody struct {
		Messages []struct {
			Body string `json:"body"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil || len(detailBody.Messages) != 1 || detailBody.Messages[0].Body == "" {
		t.Fatalf("detail=%+v err=%v", detailBody, err)
	}
	read := h.request(http.MethodGet, "/api/v1/replies/"+response.ProcessingID, "", cookie, "", "", "")
	if read.Code != http.StatusOK {
		t.Fatalf("read processing: %d", read.Code)
	}
	missing := h.request(http.MethodGet, "/api/v1/replies/missing", "", cookie, "", "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing processing: %d", missing.Code)
	}
}
