package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/delivery"
	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type contractSender struct{ calls int }

func (s *contractSender) Send(context.Context, delivery.Material, string) delivery.Outcome {
	s.calls++
	return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
}

func setupBoundSendFixture(t *testing.T) (*harness, *contractSender, store.Opportunity) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := h.db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Harbour"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID,
		Title: "Backend Engineer", Kind: "employment", SourceURL: "https://harbour.example/job",
		OriginalText: "Send your application to jobs@harbour.example.", Stage: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: "http-select", ExpectedOpportunityRevision: opportunity.Revision,
		ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	preferences, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setSHA := sha256.Sum256([]byte("http-question-set"))
	setHex := hex.EncodeToString(setSHA[:])
	manifest, _ := json.Marshal(map[string]any{
		"role": map[string]any{"opportunityId": opportunity.ID,
			"opportunityRevision": opportunity.Revision, "profileRevision": preferences.Version, "title": opportunity.Title,
			"company": company.Name, "sourceUrl": opportunity.SourceURL, "description": opportunity.OriginalText},
		"draft": map[string]any{"materialUnknowns": []string{}},
	})
	source, pdf := []byte("= CV"), append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	packed, _ := json.Marshal(struct{ Manifest, Source, PDF []byte }{manifest, source, pdf})
	packHash := sha256.Sum256(packed)
	pdfHash := sha256.Sum256(pdf)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = h.db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		// F2: bind the pack to a prepared question-less material version so
		// the send pre-check verifies instead of rejecting the fixture.
		_, err := tx.ExecContext(ctx, `INSERT INTO job_checks(id,opportunity_id,request_key,request_sha256,opportunity_revision,workflow_revision,status,question_set_sha256,actor_kind,actor_id,created_at,completed_at)
		 VALUES('http-check',?,?,?,?,1,'checked',?,'administrator','owner',?,?)`, opportunity.ID,
			"http-check-key", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			opportunity.Revision, setHex, now, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO application_packs(id,opportunity_id,opportunity_revision,profile_revision,version,content_sha256,manifest_json,typst_source,pdf,created_at)
		 VALUES('http-pack',?,?,1,1,?,?,?,?,?)`, opportunity.ID, opportunity.Revision, hex.EncodeToString(packHash[:]), string(manifest), source, pdf, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_material_versions(opportunity_id,version,pack_id,check_id,question_set_sha256,opportunity_revision,profile_revision,workflow_revision,origin,answers_json,readiness_json,source_shas_json,request_key,request_sha256,actor_kind,actor_id,created_at)
		 VALUES(?,1,'http-pack','http-check',?,?,?,?, 'prepared','[]','{"ready":true,"missingRequired":[],"held":[]}','[]','http-material','dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd','administrator','owner',?)`,
			opportunity.ID, setHex, opportunity.Revision, preferences.Version, 1, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_routes(id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		 VALUES('http-route',?,'direct','jobs@harbour.example','posting',?, ?,1,?,?)`, opportunity.ID,
			"Send your application to jobs@harbour.example.", now, now, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO delivery_reviews(id,owner_id,request_key,pack_ids_json,material_sha256,approved_sha256,approved_at,created_at)
		 VALUES('http-review','owner','http-review-key','["http-pack"]',?,?,?,?)`,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO delivery_items(id,review_id,pack_id,opportunity_id,opportunity_revision,source_sha256,profile_revision,
		 pack_content_sha256,route_id,route_revision,route_sha256,title,company_name,route_excerpt,recipient,sender,subject,body,attachment_sha256,
		 mime_sha256,mime_bytes,message_id,state,created_at,updated_at)
		 VALUES('http-item','http-review','http-pack',?,?,?,?,?,'http-route',1,?,?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?)`,
			opportunity.ID, opportunity.Revision, store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText),
			1, hex.EncodeToString(packHash[:]), store.DeliveryRouteHash(store.OpportunityRoute{OpportunityRouteInput: store.OpportunityRouteInput{
				Kind: "direct", DestinationText: "jobs@harbour.example", SourceKind: "posting",
				SourceExcerpt: "Send your application to jobs@harbour.example."}}), opportunity.Title, company.Name,
			"Send your application to jobs@harbour.example.", "jobs@harbour.example", "owner@example.org", "Application: Backend Engineer",
			"I would like to apply.", hex.EncodeToString(pdfHash[:]), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			[]byte("synthetic MIME"), "<http-review@find-income.local>", now, now)
		return store.Change{Operation: "fixture.delivery_http", EntityKind: "delivery_review", EntityID: "http-review"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &contractSender{}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Delivery: &deliveryservice.Service{
		Store: h.db, Sender: sender, From: "owner@example.org"}})
	return h, sender, opportunity
}

func TestDeliverySendHTTPRoundContractOnFirstCallAndReplay(t *testing.T) {
	h, sender, _ := setupBoundSendFixture(t)
	cookie, csrf := h.login()
	var firstID string
	for call := 0; call < 2; call++ {
		response := h.request(http.MethodPost, "/api/v1/delivery/reviews/http-review/send", "", cookie, "", csrf, origin)
		if response.Code != http.StatusOK {
			t.Fatalf("send call %d: %d %s", call, response.Code, response.Body.String())
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var round map[string]json.RawMessage
		if err := json.Unmarshal(envelope["round"], &round); err != nil {
			t.Fatal(err)
		}
		if len(round["id"]) == 0 || len(round["state"]) == 0 || len(round["originalProfileVersion"]) == 0 ||
			len(round["effectiveProfileVersion"]) == 0 || len(round["ID"]) != 0 || len(round["State"]) != 0 {
			t.Fatalf("round response violates contract on call %d: %s", call, envelope["round"])
		}
		var id string
		if err := json.Unmarshal(round["id"], &id); err != nil || id == "" {
			t.Fatalf("round identity: %q %v", round["id"], err)
		}
		if call == 0 {
			firstID = id
		} else if id != firstID {
			t.Fatalf("replay returned another round: %s / %s", firstID, id)
		}
	}
	if sender.calls != 0 {
		t.Fatalf("fixture's unsupported route reached sender: %d calls", sender.calls)
	}
}

func TestDeliverySendRejectsSupersededPack(t *testing.T) {
	h, sender, opportunity := setupBoundSendFixture(t)
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	preferences, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setSHA := sha256.Sum256([]byte("http-question-set"))
	manifest, _ := json.Marshal(map[string]any{
		"draft": map[string]any{"materialUnknowns": []string{}},
	})
	pdf := append([]byte("%PDF-1.7 v2\n"), make([]byte, 120)...)
	packed, _ := json.Marshal(struct{ Manifest, Source, PDF []byte }{manifest, []byte("= CV v2"), pdf})
	packHash := sha256.Sum256(packed)
	_, err = h.db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO application_packs(id,opportunity_id,opportunity_revision,profile_revision,version,content_sha256,manifest_json,typst_source,pdf,created_at)
		 VALUES('http-pack-2',?,?,1,2,?,?,?,?,?)`, opportunity.ID, opportunity.Revision,
			hex.EncodeToString(packHash[:]), string(manifest), []byte("= CV v2"), pdf, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_material_versions(opportunity_id,version,pack_id,check_id,question_set_sha256,opportunity_revision,profile_revision,workflow_revision,origin,answers_json,readiness_json,source_shas_json,request_key,request_sha256,actor_kind,actor_id,created_at)
		 VALUES(? ,2,'http-pack-2','http-check',?,?,?,1,'direct_edit','[]','{"ready":true,"missingRequired":[],"held":[]}','[]','http-material-2','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','administrator','owner',?)`,
			opportunity.ID, hex.EncodeToString(setSHA[:]), opportunity.Revision, preferences.Version, now)
		return store.Change{Operation: "fixture.delivery_supersede", EntityKind: "opportunity_material_versions", EntityID: "http-pack-2"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := h.login()
	response := h.request(http.MethodPost, "/api/v1/delivery/reviews/http-review/send", "", cookie, "", csrf, origin)
	if response.Code != http.StatusConflict {
		t.Fatalf("superseded send: got %d, want 409", response.Code)
	}
	if sender.calls != 0 {
		t.Fatalf("superseded review reached sender: %d calls", sender.calls)
	}
}
