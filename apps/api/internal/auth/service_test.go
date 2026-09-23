package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const syntheticPassword = "synthetic-owner-password-2026"

func TestPasswordHashAndSetupRace(t *testing.T) {
	encoded, err := HashPassword([]byte(syntheticPassword))
	if err != nil {
		t.Fatal(err)
	}
	if encoded == syntheticPassword || !VerifyPassword(encoded, []byte(syntheticPassword)) || VerifyPassword(encoded, []byte("incorrect-password")) {
		t.Fatal("password hash verification failed")
	}
	if VerifyPassword("$argon2id$v=19$m=999999999,t=3,p=1$bad$bad", []byte(syntheticPassword)) {
		t.Fatal("accepted malicious hash parameters")
	}
	if _, err := HashPassword([]byte("short")); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}

	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := NewService(database)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); results <- service.SetupAdministrator(ctx, []byte(syntheticPassword)) }()
	}
	wait.Wait()
	close(results)
	successes, duplicates := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, store.ErrAlreadyConfigured):
			duplicates++
		default:
			t.Fatalf("setup result: %v", result)
		}
	}
	if successes != 1 || duplicates != 1 {
		t.Fatalf("setup race: successes=%d duplicates=%d", successes, duplicates)
	}
	if err := service.SetupAdministrator(ctx, []byte("different-synthetic-password")); !errors.Is(err, store.ErrAlreadyConfigured) {
		t.Fatalf("setup retry changed credential: %v", err)
	}
	if _, err := service.Login(ctx, []byte("different-synthetic-password")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("retry password worked: %v", err)
	}
	if _, err := service.Login(ctx, []byte(syntheticPassword)); err != nil {
		t.Fatalf("original password failed: %v", err)
	}
	var count int
	err = database.Read(ctx, func(reader store.Reader) error {
		return reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_changes WHERE operation='admin.setup'").Scan(&count)
	})
	if err != nil || count != 1 {
		t.Fatalf("admin setup audit count=%d err=%v", count, err)
	}
}

func TestSessionExpiryAndPrincipalActor(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := NewService(database)
	if err := service.SetupAdministrator(ctx, []byte(syntheticPassword)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	service.SetClock(func() time.Time { return now })
	session, err := service.Login(ctx, []byte(syntheticPassword))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.SessionPrincipal(ctx, session.Token)
	if err != nil || !principal.IsOwner() || principal.Actor().ID != "owner" || !ValidateCSRF(principal, session.CSRFToken) || ValidateCSRF(principal, "wrong") {
		t.Fatalf("owner principal/CSRF: %+v %v", principal, err)
	}
	service.SetClock(func() time.Time { return now.Add(13 * time.Hour) })
	if _, err := service.SessionPrincipal(ctx, session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestAgentCannotAdministerCredentialsAtServiceBoundary(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := NewService(database)
	agent := Principal{Kind: "agent", ID: "synthetic-agent", Scopes: []string{"preferences:read"}}
	if _, _, err := service.CreateAgent(ctx, agent, "unauthorized", []string{"preferences:read"}, time.Now().Add(time.Hour)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent created credential: %v", err)
	}
	if _, err := service.ListAgents(ctx, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent listed credentials: %v", err)
	}
	if err := service.RevokeAgent(ctx, agent, "some-id"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent revoked credential: %v", err)
	}
}
