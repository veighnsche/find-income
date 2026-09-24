package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRoleCriterionSearchTermsValidation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	current, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.RoleCriteria) == 0 || len(current.RoleCriteria[0].SearchTerms) == 0 {
		t.Fatalf("default criterion misses search terms: %+v", current.RoleCriteria)
	}
	withTerms := func(terms []string) Preferences {
		t.Helper()
		next, err := db.CurrentPreferences(ctx)
		if err != nil {
			t.Fatal(err)
		}
		next.RoleCriteria[0].SearchTerms = terms
		return next
	}
	for _, terms := range [][]string{
		{"backend engineer", "platform engineer", "site reliability engineer", "devops engineer", "data engineer"},
		{"x"},
		{strings.Repeat("y", 121)},
		{" backend engineer"},
		{"backend engineer", "backend engineer"},
	} {
		if _, _, err := db.UpdatePreferences(ctx, current.Version, withTerms(terms), owner); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid terms accepted: %q (%v)", terms, err)
		}
	}
	updated, _, err := db.UpdatePreferences(ctx, current.Version, withTerms([]string{"backend engineer", "golang engineer"}), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RoleCriteria[0].SearchTerms) != 2 || updated.RoleCriteria[0].SearchTerms[1] != "golang engineer" {
		t.Fatalf("terms not persisted: %+v", updated.RoleCriteria[0])
	}
	reread, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.RoleCriteria[0].SearchTerms) != 2 {
		t.Fatalf("terms not reread: %+v", reread.RoleCriteria[0])
	}
}
