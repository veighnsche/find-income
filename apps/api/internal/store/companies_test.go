package store

import (
	"context"
	"errors"
	"testing"
)

func ownerActor() Actor { return Actor{Kind: "administrator", ID: "owner"} }

func TestCompanyRoundTripArchiveAndDuplicateWarning(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	first, firstChange, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{
		Name: "Harbour Systems", Website: "https://harbour.example", Notes: "Synthetic company fixture",
	})
	if err != nil || first.ID == "" || first.Revision != 1 || firstChange == "" {
		t.Fatalf("create: %+v change=%q err=%v", first, firstChange, err)
	}
	read, err := s.Company(ctx, first.ID)
	if err != nil || read.Name != first.Name || read.Website != first.Website || read.Notes != first.Notes {
		t.Fatalf("roundtrip: %+v err=%v", read, err)
	}
	duplicates, err := s.LikelyDuplicateCompanies(ctx, CompanyInput{Name: "Other", Website: "https://harbour.example"}, "")
	if err != nil || len(duplicates) != 1 || duplicates[0].Reason != "same_website" {
		t.Fatalf("website warning: %+v err=%v", duplicates, err)
	}
	second, _, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{Name: "Harbour Systems"})
	if err != nil || second.ID == first.ID {
		t.Fatalf("duplicate should remain separate: %+v err=%v", second, err)
	}
	duplicates, err = s.LikelyDuplicateCompanies(ctx, CompanyInput{Name: "harbour systems"}, first.ID)
	if err != nil || len(duplicates) != 1 || duplicates[0].Company.ID != second.ID {
		t.Fatalf("name warning: %+v err=%v", duplicates, err)
	}
	name := "Harbour Platform"
	updated, patchChange, err := s.PatchCompany(ctx, ownerActor(), first.ID, CompanyPatch{
		ExpectedRevision: 1, Name: &name,
	})
	if err != nil || updated.Revision != 2 || updated.Name != name || patchChange == firstChange {
		t.Fatalf("patch: %+v change=%q err=%v", updated, patchChange, err)
	}
	if _, _, err := s.PatchCompany(ctx, ownerActor(), first.ID, CompanyPatch{
		ExpectedRevision: 1, Name: &name,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale company patch: %v", err)
	}
	archived, archiveChange, err := s.ArchiveCompany(ctx, ownerActor(), first.ID, 2)
	if err != nil || archived.Revision != 3 || archived.ArchivedAt == "" || archiveChange == "" {
		t.Fatalf("archive: %+v change=%q err=%v", archived, archiveChange, err)
	}
	read, err = s.Company(ctx, first.ID)
	if err != nil || read.ArchivedAt == "" {
		t.Fatalf("archived record lost: %+v err=%v", read, err)
	}
	active, err := s.ListCompanies(ctx, CompanyListOptions{})
	if err != nil || len(active.Items) != 1 || active.Items[0].ID != second.ID {
		t.Fatalf("active company list: %+v err=%v", active, err)
	}
	all, err := s.ListCompanies(ctx, CompanyListOptions{IncludeArchived: true})
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("archived company list: %+v err=%v", all, err)
	}
	change, err := s.RecordChange(ctx, archiveChange)
	if err != nil || change.Operation != "company.archive" || change.Actor.ID != "owner" ||
		change.RevisionBefore == nil || *change.RevisionBefore != 2 ||
		change.RevisionAfter == nil || *change.RevisionAfter != 3 || change.SnapshotState != "captured" {
		t.Fatalf("company audit change: %+v err=%v", change, err)
	}
}

func TestCompanyValidationAndPagination(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	if _, _, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{Name: " "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty company accepted: %v", err)
	}
	if _, _, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{Name: "A", Website: "file:///secret"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid website accepted: %v", err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if _, _, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListCompanies(ctx, CompanyListOptions{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %+v err=%v", page, err)
	}
	next, err := s.ListCompanies(ctx, CompanyListOptions{Cursor: page.NextCursor, Limit: 2})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second page: %+v err=%v", next, err)
	}
	if _, err := s.ListCompanies(ctx, CompanyListOptions{Cursor: page.NextCursor, IncludeArchived: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed company filter accepted: %v", err)
	}
	if _, err := s.ListCompanies(ctx, CompanyListOptions{Limit: 101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded limit accepted: %v", err)
	}
	if _, err := s.ListCompanies(ctx, CompanyListOptions{Cursor: "not-base64"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor accepted: %v", err)
	}
}
