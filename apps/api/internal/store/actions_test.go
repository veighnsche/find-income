package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func mustClock(t *testing.T, value string) time.Time {
	t.Helper()
	clock, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return clock
}

func createActionFixture(t *testing.T, s *Store, description string, due ActionDue) Action {
	t.Helper()
	action, _, err := s.CreateAction(context.Background(), ownerActor(), ActionInput{
		Description: description, Due: due,
	})
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func actionIDs(page ActionPage) map[string]bool {
	ids := map[string]bool{}
	for _, item := range page.Items {
		ids[item.ID] = true
	}
	return ids
}

func TestActionRoundTripAuditArchiveAndPagination(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	created, createChange, err := s.CreateAction(ctx, ownerActor(), ActionInput{
		OpportunityID: opportunity.ID, Description: "Ask about duties",
		Due: ActionDue{Date: "2026-10-25"},
	})
	if err != nil || created.ID == "" || created.Revision != 1 || created.DueDate != "2026-10-25" ||
		created.DueAt != "" || createChange == "" {
		t.Fatalf("create date action: %+v change=%q err=%v", created, createChange, err)
	}
	read, err := s.Action(ctx, created.ID)
	if err != nil || read.OpportunityID != opportunity.ID || read.DueDate != "2026-10-25" {
		t.Fatalf("roundtrip: %+v err=%v", read, err)
	}
	var operation, actorKind, actorID string
	var revisionAfter int64
	if err := s.db.QueryRowContext(ctx, `SELECT operation,actor_kind,actor_id,revision_after
  FROM audit_changes WHERE id=?`, createChange).Scan(&operation, &actorKind, &actorID, &revisionAfter); err != nil ||
		operation != "action.create" || actorKind != "administrator" || actorID != "owner" || revisionAfter != 1 {
		t.Fatalf("action audit: %q %q %q rev=%d err=%v", operation, actorKind, actorID, revisionAfter, err)
	}
	newDescription := "Ask recruiter about duties and 32 hours"
	updated, patchChange, err := s.PatchAction(ctx, ownerActor(), created.ID, ActionPatch{
		ExpectedRevision: 1, Description: &newDescription,
	})
	if err != nil || updated.Revision != 2 || updated.Description != newDescription || patchChange == createChange {
		t.Fatalf("patch: %+v err=%v", updated, err)
	}
	rescheduled, rescheduleChange, err := s.RescheduleAction(ctx, ownerActor(), created.ID, 2,
		ActionDue{At: "2026-10-25T02:30:00+01:00", Timezone: "Europe/Amsterdam"})
	if err != nil || rescheduled.Revision != 3 || rescheduled.DueDate != "" ||
		rescheduled.DueAt != "2026-10-25T01:30:00.000000000Z" ||
		rescheduled.DueTimezone != "Europe/Amsterdam" || rescheduleChange == patchChange {
		t.Fatalf("reschedule: %+v err=%v", rescheduled, err)
	}
	if _, _, err := s.RescheduleAction(ctx, ownerActor(), created.ID, 2, ActionDue{Date: "2026-10-26"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale reschedule: %v", err)
	}
	completed, completeChange, err := s.CompleteAction(ctx, ownerActor(), created.ID, 3)
	if err != nil || completed.Status != "completed" || completed.CompletedAt == "" ||
		completed.Revision != 4 || completeChange == rescheduleChange {
		t.Fatalf("complete: %+v err=%v", completed, err)
	}
	if _, _, err := s.CompleteAction(ctx, ownerActor(), created.ID, 3); !errors.Is(err, ErrConflict) {
		t.Fatalf("completion retry silently rewrote action: %v", err)
	}
	if _, _, err := s.RescheduleAction(ctx, ownerActor(), created.ID, 4, ActionDue{Date: "2026-10-27"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed action rescheduled: %v", err)
	}
	other := createActionFixture(t, s, "Independent follow-up", ActionDue{Date: "2026-10-24"})
	cancelled, cancelChange, err := s.CancelAction(ctx, ownerActor(), other.ID, 1)
	if err != nil || cancelled.Status != "cancelled" || cancelled.CompletedAt != "" || cancelChange == "" {
		t.Fatalf("cancel: %+v err=%v", cancelled, err)
	}
	if _, _, err := s.ArchiveOpportunity(ctx, ownerActor(), opportunity.ID, opportunity.Revision); err != nil {
		t.Fatal(err)
	}
	read, err = s.Action(ctx, created.ID)
	if err != nil || read.OpportunityID != opportunity.ID || read.Status != "completed" {
		t.Fatalf("archive lost action reference: %+v err=%v", read, err)
	}
	var auditBefore int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAction(ctx, ownerActor(), ActionInput{OpportunityID: opportunity.ID,
		Description: "New link to archived opening", Due: ActionDue{Date: "2026-10-25"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new archived link accepted: %v", err)
	}
	var auditAfter int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatalf("failed link left audit row: %d -> %d err=%v", auditBefore, auditAfter, err)
	}
	for i := 0; i < 3; i++ {
		createActionFixture(t, s, fmt.Sprintf("Tied action %d", i), ActionDue{Date: "2026-10-25"})
	}
	// Synthetic timestamp tie exercises the ID tie breaker.
	if _, err := s.db.ExecContext(ctx, `UPDATE actions SET created_at='2026-09-23T00:00:00.000000000Z'`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.ListActions(ctx, ActionListOptions{Cursor: cursor, Limit: 1})
		if err != nil || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("tied action page: %+v err=%v", page, err)
		}
		seen[page.Items[0].ID] = true
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("action pagination skipped records: %+v", seen)
	}
	completedPage, err := s.ListActions(ctx, ActionListOptions{Status: "completed"})
	if err != nil || len(completedPage.Items) != 1 || completedPage.Items[0].ID != created.ID {
		t.Fatalf("completed filter: %+v err=%v", completedPage, err)
	}
}

func TestActionValidationAndNoPartialPatch(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	invalid := []ActionDue{
		{},
		{Date: "2026-02-30"},
		{Date: "2026-10-25", At: "2026-10-25T02:30:00+01:00", Timezone: "Europe/Amsterdam"},
		{At: "2026-10-25T02:30:00", Timezone: "Europe/Amsterdam"},
		{At: "2026-10-25T02:30:00+01:00"},
		{At: "2026-10-25T02:30:00+01:00", Timezone: "Local"},
		{At: "2026-10-25T02:30:00+01:00", Timezone: "No/Such_Zone"},
		{At: "2026-03-29T02:30:00+01:00", Timezone: "Europe/Amsterdam"},
		{At: "2026-10-25T02:30:00+00:00", Timezone: "Europe/Amsterdam"},
	}
	for i, due := range invalid {
		if _, _, err := s.CreateAction(ctx, ownerActor(), ActionInput{Description: "Follow up", Due: due}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid due %d accepted: %v", i, err)
		}
	}
	if _, _, err := s.CreateAction(ctx, ownerActor(), ActionInput{Description: "   ", Due: ActionDue{Date: "2026-10-25"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank description accepted: %v", err)
	}
	created := createActionFixture(t, s, "Valid action", ActionDue{Date: "2026-10-25"})
	var auditBefore int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	badDescription := " "
	if _, _, err := s.PatchAction(ctx, ownerActor(), created.ID, ActionPatch{
		ExpectedRevision: 1, Description: &badDescription,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank patch accepted: %v", err)
	}
	if _, _, err := s.RescheduleAction(ctx, ownerActor(), created.ID, 1,
		ActionDue{At: "2026-03-29T02:30:00", Timezone: "Europe/Amsterdam"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ambiguous/nonexistent local wall time accepted: %v", err)
	}
	current, err := s.Action(ctx, created.ID)
	if err != nil || current.Revision != 1 || current.DueDate != "2026-10-25" {
		t.Fatalf("failed patch partially wrote action: %+v err=%v", current, err)
	}
	var auditAfter int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatalf("failed patch left audit: %d -> %d err=%v", auditBefore, auditAfter, err)
	}
}

func TestActionDueAndOverdueAcrossAmsterdamDST(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	date28 := createActionFixture(t, s, "March 28 calendar", ActionDue{Date: "2026-03-28"})
	date29 := createActionFixture(t, s, "March 29 calendar", ActionDue{Date: "2026-03-29"})
	springTimed := createActionFixture(t, s, "Spring timed", ActionDue{
		At: "2026-03-29T03:30:00+02:00", Timezone: "Europe/Amsterdam"})
	options := ActionDueListOptions{At: mustClock(t, "2026-03-28T22:30:00Z"), CalendarTimezone: "Europe/Amsterdam"}
	before, err := s.ListDueActions(ctx, options)
	if err != nil || !actionIDs(before)[date28.ID] || actionIDs(before)[date29.ID] || actionIDs(before)[springTimed.ID] {
		t.Fatalf("before spring local midnight: %+v err=%v", before, err)
	}
	options.At = mustClock(t, "2026-03-28T23:30:00Z") // Amsterdam March 29, 00:30
	afterMidnight, err := s.ListDueActions(ctx, options)
	if err != nil || !actionIDs(afterMidnight)[date29.ID] || actionIDs(afterMidnight)[springTimed.ID] {
		t.Fatalf("after spring local midnight: %+v err=%v", afterMidnight, err)
	}
	overdue, err := s.ListOverdueActions(ctx, options)
	if err != nil || !actionIDs(overdue)[date28.ID] || actionIDs(overdue)[date29.ID] {
		t.Fatalf("spring calendar overdue: %+v err=%v", overdue, err)
	}
	options.At = mustClock(t, "2026-03-29T01:30:00Z")
	atInstant, err := s.ListDueActions(ctx, options)
	if err != nil || !actionIDs(atInstant)[springTimed.ID] {
		t.Fatalf("spring timed exact instant: %+v err=%v", atInstant, err)
	}
	overdue, err = s.ListOverdueActions(ctx, options)
	if err != nil || actionIDs(overdue)[springTimed.ID] {
		t.Fatalf("exact timed deadline called overdue: %+v err=%v", overdue, err)
	}
	firstFall := createActionFixture(t, s, "First fall 02:30", ActionDue{
		At: "2026-10-25T02:30:00+02:00", Timezone: "Europe/Amsterdam"})
	secondFall := createActionFixture(t, s, "Second fall 02:30", ActionDue{
		At: "2026-10-25T02:30:00+01:00", Timezone: "Europe/Amsterdam"})
	fallDate := createActionFixture(t, s, "October 25 calendar", ActionDue{Date: "2026-10-25"})
	if firstFall.DueAt == secondFall.DueAt {
		t.Fatalf("repeated local hour collapsed: %q", firstFall.DueAt)
	}
	options.At = mustClock(t, "2026-10-24T21:30:00Z") // Amsterdam Oct 24, 23:30
	beforeFallDay, err := s.ListDueActions(ctx, options)
	if err != nil || actionIDs(beforeFallDay)[fallDate.ID] {
		t.Fatalf("fall date shifted early: %+v err=%v", beforeFallDay, err)
	}
	options.At = mustClock(t, "2026-10-25T00:45:00Z") // first 02:45 local
	firstHour, err := s.ListDueActions(ctx, options)
	if err != nil || !actionIDs(firstHour)[fallDate.ID] || !actionIDs(firstHour)[firstFall.ID] || actionIDs(firstHour)[secondFall.ID] {
		t.Fatalf("fall repeated hour collapsed: %+v err=%v", firstHour, err)
	}
	options.At = mustClock(t, "2026-10-25T01:30:00Z") // second 02:30 local
	secondHour, err := s.ListDueActions(ctx, options)
	if err != nil || !actionIDs(secondHour)[firstFall.ID] || !actionIDs(secondHour)[secondFall.ID] {
		t.Fatalf("second fall instant missing: %+v err=%v", secondHour, err)
	}
	overdue, err = s.ListOverdueActions(ctx, options)
	if err != nil || actionIDs(overdue)[secondFall.ID] {
		t.Fatalf("second fall exact instant overdue: %+v err=%v", overdue, err)
	}
}

func TestActionConcurrentStaleWritersAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	action := createActionFixture(t, first, "Concurrent action", ActionDue{Date: "2026-10-25"})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			_, _, err := store.RescheduleAction(ctx, ownerActor(), action.ID, 1, ActionDue{Date: "2026-10-26"})
			results <- err
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	var auditCount int
	if err := first.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes WHERE entity_kind='action' AND entity_id=?`, action.ID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("stale writer left audit: count=%d err=%v", auditCount, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	read, err := reopened.Action(ctx, action.ID)
	if err != nil || read.Revision != 2 || read.DueDate != "2026-10-26" {
		t.Fatalf("restart lost reschedule: %+v err=%v", read, err)
	}
}
