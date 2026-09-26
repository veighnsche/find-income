package store

import (
	"context"
	"errors"
	"testing"
)

func walkToPrepared(t *testing.T, s *Store, opportunityID string) RoleWorkflow {
	t.Helper()
	var workflow RoleWorkflow
	var err error
	for _, stage := range []string{RoleStageChecking, RoleStageChecked, RoleStageAnswering,
		RoleStageAnswered, RoleStagePreparing, RoleStagePrepared} {
		workflow, err = s.AdvanceRoleWorkflow(ctxOf(t), opportunityID, workflow.Revision, stage, "")
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
	}
	return workflow
}

func ctxOf(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestSaveRoleHandoff(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-handoff")
	prepared := walkToPrepared(t, s, opportunity.ID)

	if _, _, err := s.SaveRoleHandoff(ctx, opportunity.ID, prepared.Revision+99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v, want conflict", err)
	}
	saved, created, err := s.SaveRoleHandoff(ctx, opportunity.ID, prepared.Revision)
	if err != nil || !created || saved.Stage != RoleStageHandoffSaved || saved.Revision != prepared.Revision+1 {
		t.Fatalf("saved=%+v created=%v err=%v, want terminal handoff_saved", saved, created, err)
	}
	replayed, created, err := s.SaveRoleHandoff(ctx, opportunity.ID, 0)
	if err != nil || created || replayed.OpportunityID != saved.OpportunityID ||
		replayed.Stage != RoleStageHandoffSaved || replayed.Revision != saved.Revision {
		t.Fatalf("replayed=%+v created=%v err=%v, want idempotent resolve", replayed, created, err)
	}
	early := selectFixtureOpportunity(t, s, company.ID, "select-handoff-early")
	if _, _, err := s.SaveRoleHandoff(ctx, early.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unprepared save: %v, want invalid", err)
	}
}
