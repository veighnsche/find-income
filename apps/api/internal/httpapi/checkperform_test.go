package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type stubCheckPerformer struct {
	db         *store.Store
	authorized bool
	calls      int
	err        error
}

func (s *stubCheckPerformer) Authorized() bool { return s.authorized }

func (s *stubCheckPerformer) PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
	s.calls++
	if s.err != nil {
		return store.CheckView{}, s.err
	}
	return s.db.SaveJobCheckBody(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.CheckSaveInput{
		OpportunityID: opportunityID, CheckID: checkID,
		Blocked: &store.CheckBlockedInput{Code: store.CheckBlockedSourceUnavailable, Detail: "stub has no evidence"},
	})
}

func checkWiredHarness(t *testing.T, performer musewire.CheckPerformer) *harness {
	t.Helper()
	h := newHarness(t)
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: performer})
	return h
}

func TestCheckPostPerformsWhenAuthorized(t *testing.T) {
	newOpportunity := func(t *testing.T, h *harness, key string) (string, int64) {
		t.Helper()
		opportunity := createCheckedOpportunity(t, h, key)
		return opportunity.ID, opportunity.Revision
	}
	start := func(id string, revision int64) string {
		return fmt.Sprintf(`{"requestKey":"check-perform-1","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, revision)
	}

	t.Run("unauthorized leaves pending", func(t *testing.T) {
		performer := &stubCheckPerformer{authorized: false}
		h := checkWiredHarness(t, performer)
		cookie, csrf := h.login()
		id, revision := newOpportunity(t, h, "check-unauth")
		performer.db = h.db
		response := h.request("POST", "/api/v1/opportunities/"+id+"/checks", start(id, revision), cookie, "", csrf, origin)
		if response.Code != 201 {
			t.Fatalf("start: %d %s", response.Code, response.Body.String())
		}
		var view struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Status != "checking" || performer.calls != 0 {
			t.Fatalf("view=%+v calls=%d, want pending with no perform", view, performer.calls)
		}
	})

	t.Run("non-muse role leaves pending", func(t *testing.T) {
		performer := &stubCheckPerformer{authorized: true, err: musewire.ErrCheckNotMuse}
		h := checkWiredHarness(t, performer)
		cookie, csrf := h.login()
		id, revision := newOpportunity(t, h, "check-notmuse")
		performer.db = h.db
		response := h.request("POST", "/api/v1/opportunities/"+id+"/checks", start(id, revision), cookie, "", csrf, origin)
		var view struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if response.Code != 201 || view.Status != "checking" || performer.calls != 1 {
			t.Fatalf("code=%d view=%+v calls=%d, want pending passthrough", response.Code, view, performer.calls)
		}
	})

	t.Run("authorized perform returns completed view", func(t *testing.T) {
		performer := &stubCheckPerformer{authorized: true}
		h := checkWiredHarness(t, performer)
		cookie, csrf := h.login()
		id, revision := newOpportunity(t, h, "check-performed")
		performer.db = h.db
		response := h.request("POST", "/api/v1/opportunities/"+id+"/checks", start(id, revision), cookie, "", csrf, origin)
		var view struct {
			Status string `json:"status"`
			Check  struct {
				Status        string `json:"status"`
				BlockedReason *struct {
					Code string `json:"code"`
				} `json:"blockedReason"`
			} `json:"check"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if response.Code != 201 || view.Check.Status != "blocked" || view.Check.BlockedReason.Code != "source_unavailable" {
			t.Fatalf("code=%d view=%+v calls=%d, want completed blocked view", response.Code, view, performer.calls)
		}
	})

	t.Run("perform failure surfaces", func(t *testing.T) {
		performer := &stubCheckPerformer{authorized: true, err: store.ErrInvalid}
		h := checkWiredHarness(t, performer)
		cookie, csrf := h.login()
		id, revision := newOpportunity(t, h, "check-failed")
		performer.db = h.db
		response := h.request("POST", "/api/v1/opportunities/"+id+"/checks", start(id, revision), cookie, "", csrf, origin)
		if response.Code != 400 {
			t.Fatalf("code=%d, want 400", response.Code)
		}
	})
}
