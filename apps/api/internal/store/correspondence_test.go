package store

import (
	"context"
	"errors"
	"testing"
)

func TestCorrespondenceSyncDeduplicatesAndStaysInert(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	other := Actor{Kind: "administrator", ID: "other"}
	account, created, err := db.ConnectCorrespondenceAccount(ctx, owner, CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Owner"})
	if err != nil || !created {
		t.Fatalf("connect: %v %v", created, err)
	}
	replay, created, err := db.ConnectCorrespondenceAccount(ctx, owner, CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Renamed"})
	if err != nil || created || replay.ID != account.ID {
		t.Fatalf("connect replay: %+v %v %v", replay, created, err)
	}
	snapshot := CorrespondenceThreadSnapshot{ProviderThreadID: "thread-1", Subject: "Interview invitation", LastMessageAt: "2026-09-24T10:00:00Z", Provenance: map[string]any{"source": "fake"},
		Messages: []CorrespondenceMessageSnapshot{
			{ProviderMessageID: "m-1", Sender: "recruiter@example.test", Recipients: []string{"owner@example.test"}, SentAt: "2026-09-24T09:00:00Z", Body: "We would like to invite you to interview.", Provenance: map[string]any{"source": "fake"}},
			{ProviderMessageID: "m-2", Sender: "owner@example.test", Recipients: []string{"recruiter@example.test"}, SentAt: "2026-09-24T10:00:00Z", Body: "Thank you; I am available Tuesday.", Provenance: map[string]any{"source": "fake"}},
		}}
	threads, messages, err := db.SyncCorrespondenceThreads(ctx, owner, account.ID, []CorrespondenceThreadSnapshot{snapshot})
	if err != nil || threads != 1 || messages != 2 {
		t.Fatalf("sync: %d %d %v", threads, messages, err)
	}
	threads, messages, err = db.SyncCorrespondenceThreads(ctx, owner, account.ID, []CorrespondenceThreadSnapshot{snapshot})
	if err != nil || threads != 0 || messages != 0 {
		t.Fatalf("resync must deduplicate: %d %d %v", threads, messages, err)
	}
	items, err := db.OwnerCorrespondenceThreads(ctx, owner.ID)
	if err != nil || len(items) != 1 || items[0].MessageCount != 2 {
		t.Fatalf("list: %+v %v", items, err)
	}
	read, err := db.OwnerCorrespondenceThread(ctx, owner.ID, items[0].ID)
	if err != nil || read.ProviderThreadID != "thread-1" {
		t.Fatalf("read: %+v %v", read, err)
	}
	stored, err := db.CorrespondenceThreadMessages(ctx, owner.ID, items[0].ID)
	if err != nil || len(stored) != 2 || stored[0].ProviderMessageID != "m-1" || stored[0].BodySHA256 == "" {
		t.Fatalf("messages: %+v %v", stored, err)
	}
	if _, err := db.OwnerCorrespondenceThread(ctx, other.ID, items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if _, err := db.CorrespondenceThreadMessages(ctx, other.ID, items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner messages: %v", err)
	}
	notice, err := db.RecordCorrespondenceNotification(ctx, owner, account.ID, CorrespondenceNotificationInput{Kind: "incoming", ThreadID: items[0].ID, Payload: map[string]any{"providerMessageId": "m-2"}})
	if err != nil || notice.ID == "" {
		t.Fatalf("notification: %v", err)
	}
	if err := db.SetCorrespondenceAccountStatus(ctx, owner, account.ID, "auth_lost"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SyncCorrespondenceThreads(ctx, owner, account.ID, []CorrespondenceThreadSnapshot{snapshot}); !errors.Is(err, ErrFenced) {
		t.Fatalf("auth-lost sync: %v", err)
	}
}
