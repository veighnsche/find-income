package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOwnerInstructionExactReadBeyondRecentList(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner-one"}
	other := Actor{Kind: "administrator", ID: "owner-two"}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var oldest OwnerInstruction
	for i := 0; i < 101; i++ {
		value, _, err := db.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{
			RequestKey: fmt.Sprintf("profile-note-%03d", i), TargetKind: "profile", TargetID: "current",
			ExpectedRevision: profile.Version, Text: fmt.Sprintf("Owner note %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = value
		}
	}
	got, err := db.OwnerInstruction(ctx, owner, oldest.ID)
	if err != nil || got.ID != oldest.ID || got.Text != oldest.Text || got.RevokedAt != "" {
		t.Fatalf("exact owner lookup=%+v err=%v", got, err)
	}
	if _, err := db.OwnerInstruction(ctx, other, oldest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner lookup: %v", err)
	}
	if err := db.RevokeOwnerInstruction(ctx, owner, oldest.ID); err != nil {
		t.Fatal(err)
	}
	got, err = db.OwnerInstruction(ctx, owner, oldest.ID)
	if err != nil || got.RevokedAt == "" {
		t.Fatalf("revoked history lookup=%+v err=%v", got, err)
	}
}
