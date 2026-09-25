package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// RW-A4 verification: preparation holds/readiness, edit/rewrite immutability,
// review digest binding, stale-approval invalidation, one-shot send guards,
// uncertain-send protection, read-only reconcile, and frozen attempt
// snapshots. No network, no sender, no paid calls: the deliver round is
// driven through the same store calls the delivery service uses, stopping
// before SMTP.

type prepsendFixture struct {
	db          *Store
	mat         materialFixture
	owner       Actor
	opportunity Opportunity
	company     Company
	check       CheckView
	pack        ApplicationPack
	version     MaterialVersionView
	routeID     string
	destination string
	review      DeliveryReview
}

func prepsendRoleManifest(t *testing.T, f prepsendFixture, manifest map[string]any) {
	t.Helper()
	role, ok := manifest["role"].(map[string]any)
	if !ok {
		t.Fatal("material manifest lacks role section")
	}
	role["title"] = f.opportunity.Title
	role["company"] = f.company.Name
	role["sourceUrl"] = f.opportunity.SourceURL
	role["description"] = f.opportunity.OriginalText
}

// setupPrepsendReady builds one selected role with a checked check, saved
// required answers, a committed ready v1, one assessed email route, and one
// prepared (unapproved) delivery review for the v1 pack.
func setupPrepsendReady(t *testing.T, key string) prepsendFixture {
	t.Helper()
	ctx := context.Background()
	owner := ownerActor()
	mat := setupMaterialAnswered(t, key)
	db := mat.store
	company, err := db.Company(ctx, mat.opportunity.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	f := prepsendFixture{db: db, mat: mat, owner: owner,
		opportunity: mat.opportunity, company: company, check: mat.check,
		routeID: "route-" + key, destination: "applications@example.invalid"}
	prepsendFinishActiveRounds(t, db, owner)
	saveFixtureAnswer(t, mat, 0, "I want this role for its Go platform work.")
	saveFixtureAnswer(t, mat, 1, "I shipped a billing service in Go.")
	input := materialPrepareInput(t, mat, key+"-prep")
	input.Pack = materialPackFor(t, mat, func(manifest map[string]any) {
		prepsendRoleManifest(t, f, manifest)
	})
	view, created, err := db.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID, input)
	if err != nil || !created || view.Status != MaterialStatusPrepared || !view.Current.Readiness.Ready {
		t.Fatalf("fixture prepare: %+v %v", view, err)
	}
	f.version = *view.Current
	pack, err := db.ApplicationPack(ctx, f.version.PackID)
	if err != nil {
		t.Fatal(err)
	}
	f.pack = pack
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.WriteAudited(ctx, owner, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO opportunity_routes
		  (id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		  VALUES(? ,?,'direct',?,'application_instruction',?,?,1,?,?)`,
			f.routeID, f.opportunity.ID, f.destination, "Build Go services.", now, now, now)
		return Change{Operation: "fixture.prepsend_route", EntityKind: "opportunity_route", EntityID: f.routeID}, err
	}); err != nil {
		t.Fatal(err)
	}
	prepsendRecordRouteJudgment(t, f, key)
	review, err := db.CreateDeliveryReview(ctx, owner, key+"-review", prepsendDrafts(t, f, pack, key))
	if err != nil {
		t.Fatal(err)
	}
	f.review = review
	return f
}

// prepsendFinishActiveRounds completes leftover running rounds (the check
// round from the material fixture) so later rounds can start. Only one
// round is active at a time. Dispatched codex turns get a recorded
// completed observation first, mirroring a finished local turn.
func prepsendFinishActiveRounds(t *testing.T, db *Store, owner Actor) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.db.QueryContext(ctx, `SELECT id FROM rounds WHERE state IN ('running','stopping')`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		attempts, err := db.db.QueryContext(ctx, `SELECT id,operation FROM round_attempts WHERE round_id=? AND state='dispatched'`, id)
		if err != nil {
			t.Fatal(err)
		}
		var finishes [][2]string
		for attempts.Next() {
			var attemptID, operation string
			if err := attempts.Scan(&attemptID, &operation); err != nil {
				attempts.Close()
				t.Fatal(err)
			}
			finishes = append(finishes, [2]string{attemptID, operation})
		}
		attempts.Close()
		if err := attempts.Err(); err != nil {
			t.Fatal(err)
		}
		for _, finish := range finishes {
			if finish[1] == RoundCodexTurn {
				if _, err := db.db.ExecContext(ctx, `INSERT OR IGNORE INTO round_remote_dispatches
				  (attempt_id,round_id,generation,observed_status,updated_at) VALUES (?,?,1,'completed',?)`,
					finish[0], id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.FinishRoundAttempt(ctx, owner, id, finish[0], true, json.RawMessage(`{}`), ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.FinishRound(ctx, owner, id, RoundCompleted, "fixture", "check_saved", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
}

// prepsendRecordRouteJudgment records a completed route-assessment round,
// its succeeded Jev exchange, and the mailbox judgment row the send-time
// currentness check requires. Judgment validation itself is covered by
// existing assessment tests; this fixture targets downstream guards.
func prepsendRecordRouteJudgment(t *testing.T, f prepsendFixture, key string) {
	t.Helper()
	ctx := context.Background()
	preferences, err := f.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	route := prepsendRoute(t, f)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	roundKey := "route-assess-" + key
	roundSum := sha256.Sum256([]byte(roundKey))
	inputSum := sha256.Sum256([]byte("input-" + key))
	logical := []byte(`{"state":{"route":"` + f.routeID + `"}}`)
	logicalSum := sha256.Sum256(logical)
	if _, err := f.db.WriteAudited(ctx, f.owner, func(tx *sql.Tx) (Change, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rounds
		  (id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,
		   initial_profile_version,profile_version,scope_json,state,revision,generation,
		   deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at,completed_at)
		  VALUES(?,?,?,?,?,?,?,?,?,?,'completed',1,1,?,1,1,2,0,?,?,?)`,
			"round-"+key, f.owner.Kind, f.owner.ID, roundKey, hex.EncodeToString(roundSum[:]),
			"Assess the delivery route", "prepare", preferences.Version, preferences.Version, `{}`,
			time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), now, now, now); err != nil {
			return Change{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO round_attempts
		  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
		   requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,
		   created_at,updated_at,dispatched_at,finished_at)
		  VALUES(?,?,?,?,?,?,1,'succeeded',1,0,1,0,'{}',?,?,?,?)`,
			"attempt-"+key, "round-"+key, "route-jev-"+key,
			hex.EncodeToString(roundSum[:]), RoundJevRequest, "opportunity:"+f.opportunity.ID,
			now, now, now, now); err != nil {
			return Change{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jev_attempts
		  (id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,
		   candidate_set_json,profile_version,rubric_version,requested_model,returned_model,
		   logical_request_json,transport_request_bytes,raw_response_bytes,status,
		   input_tokens,output_tokens,created_at,finished_at)
		  VALUES(?,?,?,0,'delivery_route',?,?,?,?,'delivery-route-v1','synthetic-verify','synthetic-verify',
		   ?,?,?,'succeeded',12,4,?,?)`,
			"jev-"+key, "round-"+key, "attempt-"+key, hex.EncodeToString(logicalSum[:]),
			`[]`, `[]`, preferences.Version, logical, []byte(`{}`),
			[]byte(`{"choice":"application_mailbox"}`), now, now); err != nil {
			return Change{}, err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO delivery_route_assessments
		  (id,opportunity_id,route_id,source_sha256,route_sha256,input_sha256,choice,round_id,jev_attempt_id,result_json,created_at)
		  VALUES(?,?,?,?,?,?,'application_mailbox',?,?,?,?)`, "assess-"+key, f.opportunity.ID, f.routeID,
			DeliverySourceHash(f.opportunity.Title, f.opportunity.SourceURL, f.opportunity.OriginalText),
			DeliveryRouteHash(route), hex.EncodeToString(inputSum[:]),
			"round-"+key, "jev-"+key, `{"choice":"application_mailbox"}`, now)
		return Change{Operation: "fixture.prepsend_judgment", EntityKind: "delivery_route_assessment", EntityID: "assess-" + key}, err
	}); err != nil {
		t.Fatal(err)
	}
}

func storeAllowance(requests, items, tools int64) RoundAllowance {
	return RoundAllowance{Requests: requests, Items: items, Tools: tools}
}

func prepsendRoute(t *testing.T, f prepsendFixture) OpportunityRoute {
	t.Helper()
	routes, err := f.db.ListOpportunityRoutes(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.ID == f.routeID {
			return route
		}
	}
	t.Fatal("fixture route missing")
	return OpportunityRoute{}
}

func prepsendDrafts(t *testing.T, f prepsendFixture, pack ApplicationPack, key string) []DeliveryDraft {
	t.Helper()
	route := prepsendRoute(t, f)
	pdfSum := sha256.Sum256(pack.PDF)
	mime := []byte("From: owner@example.org\r\nTo: " + f.destination + "\r\n" +
		"Subject: Application: " + f.opportunity.Title + "\r\n\r\nI would like to apply.\r\n")
	mimeSum := sha256.Sum256(mime)
	return []DeliveryDraft{{
		PackID: pack.ID, OpportunityID: f.opportunity.ID,
		OpportunityRevision: pack.OpportunityRevision,
		SourceSHA256:        DeliverySourceHash(f.opportunity.Title, f.opportunity.SourceURL, f.opportunity.OriginalText),
		ProfileRevision:     pack.ProfileRevision, PackContentSHA256: pack.ContentSHA256,
		RouteID: route.ID, RouteRevision: route.Revision, RouteSHA256: DeliveryRouteHash(route),
		Title: f.opportunity.Title, CompanyName: f.company.Name, RouteExcerpt: route.SourceExcerpt,
		Recipient: f.destination, Sender: "owner@example.org",
		Subject: "Application: " + f.opportunity.Title, Body: "I would like to apply.",
		AttachmentSHA256: hex.EncodeToString(pdfSum[:]),
		MIMESHA256:       hex.EncodeToString(mimeSum[:]), MIMEBytes: mime,
		MessageID: "<" + key + "@find-income.local>"}}
}

func prepsendStartDeliverRound(t *testing.T, f prepsendFixture, requestKey string) Round {
	t.Helper()
	return prepsendStartDeliverRoundLimits(t, f, requestKey,
		storeAllowance(int64(len(f.review.Items))+1, int64(len(f.review.Items)), int64(len(f.review.Items))))
}

func prepsendStartDeliverRoundLimits(t *testing.T, f prepsendFixture, requestKey string, limits RoundAllowance) Round {
	t.Helper()
	ctx := context.Background()
	resources := make([]string, 0, len(f.review.Items)+1)
	for _, item := range f.review.Items {
		resources = append(resources, "delivery:"+item.ID)
	}
	resources = append(resources, "campaign:active")
	round, created, err := f.db.StartRound(ctx, f.owner, StartRoundInput{
		RequestKey: requestKey, Intent: "Deliver only the exact approved application review " + f.review.ID,
		Outcome: "deliver", ProfileVersion: f.review.Items[0].ProfileRevision,
		Scope: RoundScope{InputRefs: []string{"delivery_review:" + f.review.ID},
			Resources: resources, Operations: []string{RoundDeliverApplication, RoundJevRequest}},
		Limits:   limits,
		Deadline: time.Now().Add(10 * time.Minute)})
	if err != nil || !created {
		t.Fatalf("deliver round: %+v %v", round, err)
	}
	round, err = f.db.ActivateRound(ctx, f.owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	return round
}

func prepsendApprove(t *testing.T, f prepsendFixture) DeliveryReview {
	t.Helper()
	review, err := f.db.ApproveDeliveryReview(context.Background(), f.owner, f.review.ID, f.review.MaterialSHA256)
	if err != nil || review.ApprovedSHA256 != review.MaterialSHA256 {
		t.Fatalf("approve: %+v %v", review, err)
	}
	f.review = review
	return review
}

func prepsendManifestBytes(t *testing.T, db *Store, packID string) []byte {
	t.Helper()
	var raw []byte
	if err := db.db.QueryRow(`SELECT manifest_json FROM application_packs WHERE id=?`, packID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), raw...)
}

func TestPrepsendHeldRequiredBlocksReadiness(t *testing.T) {
	ctx := context.Background()
	mat := setupMaterialAnswered(t, "ps-held")
	saveFixtureAnswer(t, mat, 0, "I want this role for its Go platform work.")
	heldID := mat.check.Questions[1].ID
	view, created, err := mat.store.PrepareOpportunityMaterials(ctx, ownerActor(), mat.opportunity.ID,
		materialPrepareInput(t, mat, "ps-held-key"))
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if view.Status != MaterialStatusHeld || view.Current.Readiness.Ready {
		t.Fatalf("unset required must hold: %+v", view)
	}
	readiness := view.Current.Readiness
	if len(readiness.MissingRequired) != 1 || readiness.MissingRequired[0] != heldID ||
		len(readiness.Held) != 1 || readiness.Held[0] != heldID {
		t.Fatalf("held readiness: %+v", readiness)
	}
	if ref := view.Current.Answers[1]; ref.AnswerVersion != 0 || ref.TextSHA256 != materialEmptyTextSHA {
		t.Fatalf("held ref pins empty text: %+v", ref)
	}
	// Optional and required-unknown unset questions never hold the version.
	for _, ordinal := range []int{2, 3} {
		for _, id := range readiness.MissingRequired {
			if id == mat.check.Questions[ordinal].ID {
				t.Fatalf("unset optional/unknown holds: %+v", readiness)
			}
		}
	}
}

func TestPrepsendBlankRequiredBlocksWithoutHold(t *testing.T) {
	ctx := context.Background()
	mat := setupMaterialAnswered(t, "ps-blank")
	saveFixtureAnswer(t, mat, 0, "I want this role for its Go platform work.")
	if _, err := mat.store.SaveAnswerValue(ctx, ownerActor(), mat.opportunity.ID,
		mat.check.Questions[1].ID, AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: ""}); err != nil {
		t.Fatal(err)
	}
	view, _, err := mat.store.PrepareOpportunityMaterials(ctx, ownerActor(), mat.opportunity.ID,
		materialPrepareInput(t, mat, "ps-blank-key"))
	if err != nil {
		t.Fatal(err)
	}
	if view.Current.Readiness.Ready || len(view.Current.Readiness.Held) != 0 {
		t.Fatalf("blank required is missing, not held: %+v", view.Current.Readiness)
	}
	if len(view.Current.Readiness.MissingRequired) != 1 {
		t.Fatalf("blank required missing: %+v", view.Current.Readiness)
	}
	// Optional blanks keep a version ready.
	mat2 := setupMaterialAnswered(t, "ps-optblank")
	saveFixtureAnswer(t, mat2, 0, "I want this role for its Go platform work.")
	saveFixtureAnswer(t, mat2, 1, "I shipped a billing service in Go.")
	if _, err := mat2.store.SaveAnswerValue(ctx, ownerActor(), mat2.opportunity.ID,
		mat2.check.Questions[2].ID, AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: ""}); err != nil {
		t.Fatal(err)
	}
	view2, _, err := mat2.store.PrepareOpportunityMaterials(ctx, ownerActor(), mat2.opportunity.ID,
		materialPrepareInput(t, mat2, "ps-optblank-key"))
	if err != nil || !view2.Current.Readiness.Ready || view2.Status != MaterialStatusPrepared {
		t.Fatalf("optional blank must stay ready: %+v %v", view2, err)
	}
}

func TestPrepsendReprepareMintsNewVersion(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	mat := setupMaterialAnswered(t, "ps-reprepare")
	saveFixtureAnswer(t, mat, 0, "I want this role for its Go platform work.")
	saveFixtureAnswer(t, mat, 1, "I shipped a billing service in Go.")
	first, _, err := mat.store.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID,
		materialPrepareInput(t, mat, "ps-reprepare-1"))
	if err != nil || first.Current.Version != 1 {
		t.Fatalf("v1: %+v %v", first, err)
	}
	optional := saveFixtureAnswer(t, mat, 2, "Happy to share references.")
	second, created, err := mat.store.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID,
		materialPrepareInput(t, mat, "ps-reprepare-2"))
	if err != nil || !created {
		t.Fatalf("re-prepare: %+v %v", second, err)
	}
	if second.Current.Version != 2 || second.Current.PackID == first.Current.PackID {
		t.Fatalf("re-prepare must mint a new pack: %+v", second.Current)
	}
	if ref := second.Current.Answers[2]; ref.AnswerVersion != optional.Version || ref.TextSHA256 != optional.TextSHA256 {
		t.Fatalf("re-prepare carries the new answer: %+v", ref)
	}
	one, err := mat.store.OpportunityMaterialVersion(ctx, mat.opportunity.ID, 1)
	if err != nil || one.PackID != first.Current.PackID || one.Answers[2].AnswerVersion != 0 {
		t.Fatalf("v1 mutated by re-prepare: %+v %v", one, err)
	}
}

func TestPrepsendEditRewriteImmutability(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	f := setupPrepsendReady(t, "ps-immutable")
	v1Bytes := prepsendManifestBytes(t, f.db, f.version.PackID)
	text := "Dear hiring team,\n\n\tI want this rôle — “Go platform work”.  \nSnowman: ☃\n"
	edited, created, err := f.db.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, MaterialEditInput{
		RequestKey: "ps-immutable-edit", ExpectedVersion: 1, Text: text,
		Pack: materialRevisionPackFor(t, f.mat, MaterialOriginDirectEdit, text, nil)})
	if err != nil || !created {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	if edited.Version != 2 || edited.PackID == f.version.PackID ||
		edited.Provenance.Origin != MaterialOriginDirectEdit || edited.Provenance.RewriteOf != nil {
		t.Fatalf("edit version: %+v", edited)
	}
	for i, ref := range edited.Answers {
		if ref != f.version.Answers[i] {
			t.Fatalf("edit changed ref %d: %+v vs %+v", i, ref, f.version.Answers[i])
		}
	}
	rewrite := materialRewriteTexts(f.mat, map[int]string{
		0: "Rewritten motivation.", 1: "Rewritten Go history.",
		2: "Rewritten extra.", 3: "Rewritten availability."})
	rewritten, created, err := f.db.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, MaterialRewriteInput{
		RequestKey: "ps-immutable-rewrite", ExpectedVersion: 2, Texts: rewrite,
		Pack: materialRevisionPackFor(t, f.mat, MaterialOriginRewrite, "",
			map[string]string{
				f.check.Questions[0].ID: "Rewritten motivation.",
				f.check.Questions[1].ID: "Rewritten Go history.",
				f.check.Questions[2].ID: "Rewritten extra.",
				f.check.Questions[3].ID: "Rewritten availability.",
			})})
	if err != nil || !created {
		t.Fatalf("rewrite: %+v %v", rewritten, err)
	}
	if rewritten.Version != 3 || rewritten.PackID == edited.PackID || rewritten.PackID == f.version.PackID ||
		rewritten.Provenance.Origin != MaterialOriginRewrite ||
		rewritten.Provenance.RewriteOf == nil || *rewritten.Provenance.RewriteOf != 2 {
		t.Fatalf("rewrite version: %+v", rewritten)
	}
	if !rewritten.Readiness.Ready {
		t.Fatalf("answered rewrite must be ready: %+v", rewritten.Readiness)
	}
	// Every earlier pack and version row is byte-identical after later commits.
	if again := prepsendManifestBytes(t, f.db, f.version.PackID); string(again) != string(v1Bytes) {
		t.Fatal("v1 pack manifest mutated by later revisions")
	}
	one, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != f.version.PackID || len(one.Answers) != len(f.version.Answers) {
		t.Fatalf("v1 row mutated: %+v %v", one, err)
	}
	for i, ref := range one.Answers {
		if ref != f.version.Answers[i] {
			t.Fatalf("v1 ref %d mutated: %+v", i, ref)
		}
	}
	two, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 2)
	if err != nil || two.PackID != edited.PackID {
		t.Fatalf("v2 row mutated by rewrite: %+v %v", two, err)
	}
	current, err := f.db.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || current.Status != MaterialStatusPrepared || current.Current.Version != 3 {
		t.Fatalf("current follows newest: %+v %v", current, err)
	}
}

func TestPrepsendPrepareEditIdempotency(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	mat := setupMaterialAnswered(t, "ps-idem")
	saveFixtureAnswer(t, mat, 0, "I want this role for its Go platform work.")
	draftText := "I shipped a billing service in Go last year."
	input := materialPrepareInput(t, mat, "ps-idem-prep")
	input.Drafts = []MaterialDraft{{QuestionID: mat.check.Questions[1].ID,
		Text: draftText, TextSHA256: materialTextSHA(draftText)}}
	first, created, err := mat.store.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID, input)
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", first, err)
	}
	replay, created, err := mat.store.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID, input)
	if err != nil || created || replay.Current.Version != 1 || replay.Current.PackID != first.Current.PackID {
		t.Fatalf("identical key must replay: %+v %v", replay, err)
	}
	changed := input
	changed.Drafts = []MaterialDraft{{QuestionID: mat.check.Questions[1].ID,
		Text: "Different draft text.", TextSHA256: materialTextSHA("Different draft text.")}}
	if _, _, err := mat.store.PrepareOpportunityMaterials(ctx, owner, mat.opportunity.ID, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed input under reused key: %v", err)
	}
	text := "Exact edited text."
	edit := MaterialEditInput{RequestKey: "ps-idem-edit", ExpectedVersion: 1, Text: text,
		Pack: materialRevisionPackFor(t, mat, MaterialOriginDirectEdit, text, nil)}
	edited, created, err := mat.store.EditOpportunityMaterials(ctx, owner, mat.opportunity.ID, edit)
	if err != nil || !created || edited.Version != 2 {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	editReplay, created, err := mat.store.EditOpportunityMaterials(ctx, owner, mat.opportunity.ID, edit)
	if err != nil || created || editReplay.Version != 2 || editReplay.PackID != edited.PackID {
		t.Fatalf("identical edit key must replay: %+v %v", editReplay, err)
	}
	changedEdit := edit
	changedEdit.Text = "Other text."
	if _, _, err := mat.store.EditOpportunityMaterials(ctx, owner, mat.opportunity.ID, changedEdit); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed edit under reused key: %v", err)
	}
}

func TestPrepsendReviewDigestBinding(t *testing.T) {
	ctx := context.Background()
	f := setupPrepsendReady(t, "ps-digest")
	if _, err := f.db.ApproveDeliveryReview(ctx, f.owner, f.review.ID, "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed digest: %v", err)
	}
	wrong := strings.Repeat("0", 64)
	if wrong == f.review.MaterialSHA256 {
		wrong = strings.Repeat("1", 64)
	}
	if _, err := f.db.ApproveDeliveryReview(ctx, f.owner, f.review.ID, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong digest approved: %v", err)
	}
	approved := prepsendApprove(t, f)
	again, err := f.db.ApproveDeliveryReview(ctx, f.owner, f.review.ID, f.review.MaterialSHA256)
	if err != nil || again.ApprovedSHA256 != approved.ApprovedSHA256 {
		t.Fatalf("same-digest approve must replay: %+v %v", again, err)
	}
	// Lost-response recovery: the same request key returns the identical
	// review; a reused key for different packs conflicts.
	replay, err := f.db.DeliveryReviewByRequest(ctx, f.owner, "ps-digest-review", []string{f.pack.ID})
	if err != nil || replay.ID != f.review.ID || replay.MaterialSHA256 != f.review.MaterialSHA256 ||
		replay.Items[0].MessageID != f.review.Items[0].MessageID {
		t.Fatalf("review replay diverged: %+v %v", replay, err)
	}
	if _, err := f.db.DeliveryReviewByRequest(ctx, f.owner, "ps-digest-review", []string{"other-pack"}); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("reused key for different packs: %v", err)
	}
	if _, err := f.db.CreateDeliveryReview(ctx, f.owner, "ps-digest-review", prepsendDrafts(t, f, f.pack, "ps-digest-other")); err != nil {
		t.Fatalf("create replay: %v", err)
	}
}

func TestPrepsendUnapprovedCannotReserve(t *testing.T) {
	ctx := context.Background()
	f := setupPrepsendReady(t, "ps-noapproval")
	round := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID)
	if _, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, f.review.Items[0].ID, round.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("unapproved reserve: %v", err)
	}
}

func TestPrepsendChangedMaterialInvalidatesApproval(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	f := setupPrepsendReady(t, "ps-stale")
	prepsendApprove(t, f)
	text := "Edited after approval."
	if _, _, err := f.db.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, MaterialEditInput{
		RequestKey: "ps-stale-edit", ExpectedVersion: 1, Text: text,
		Pack: materialRevisionPackFor(t, f.mat, MaterialOriginDirectEdit, text, nil)}); err != nil {
		t.Fatal(err)
	}
	reread, err := f.db.DeliveryReview(ctx, f.review.ID)
	if err != nil || reread.Items[0].Current || reread.Items[0].BlockingReason == "" {
		t.Fatalf("stale item still current: %+v %v", reread.Items[0], err)
	}
	round := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID)
	if _, err := f.db.ReserveDeliveryAttempt(ctx, owner, f.review.ID, f.review.Items[0].ID, round.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval reserved: %v", err)
	}
	// The same staleness blocks a not-yet-approved review at approve time.
	g := setupPrepsendReady(t, "ps-stale2")
	other := "Edited before approval."
	if _, _, err := g.db.EditOpportunityMaterials(ctx, owner, g.opportunity.ID, MaterialEditInput{
		RequestKey: "ps-stale2-edit", ExpectedVersion: 1, Text: other,
		Pack: materialRevisionPackFor(t, g.mat, MaterialOriginDirectEdit, other, nil)}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.db.ApproveDeliveryReview(ctx, owner, g.review.ID, g.review.MaterialSHA256); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale review approved: %v", err)
	}
}

func TestPrepsendRecipientChangeInvalidatesApproval(t *testing.T) {
	ctx := context.Background()
	f := setupPrepsendReady(t, "ps-recipient")
	prepsendApprove(t, f)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.WriteAudited(ctx, f.owner, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `UPDATE opportunity_routes SET destination_text=?,updated_at=? WHERE id=?`,
			"elsewhere@example.invalid", now, f.routeID)
		return Change{Operation: "fixture.prepsend_routemove", EntityKind: "opportunity_route", EntityID: f.routeID}, err
	}); err != nil {
		t.Fatal(err)
	}
	reread, err := f.db.DeliveryReview(ctx, f.review.ID)
	if err != nil || reread.Items[0].Current {
		t.Fatalf("moved recipient still current: %+v %v", reread.Items[0], err)
	}
	round := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID)
	if _, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, f.review.Items[0].ID, round.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("moved recipient reserved: %v", err)
	}
}

func TestPrepsendOneShotSendNoAutoRetry(t *testing.T) {
	ctx := context.Background()
	f := setupPrepsendReady(t, "ps-oneshot")
	prepsendApprove(t, f)
	round := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID)
	item := f.review.Items[0]
	attempt, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, item.ID, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.db.ClaimDeliveryItem(ctx, f.owner, f.review.ID, item.ID, round.ID, attempt.ID)
	if err != nil || claimed.State != "sending" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if _, err := f.db.ClaimDeliveryItem(ctx, f.owner, f.review.ID, item.ID, round.ID, attempt.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("double claim: %v", err)
	}
	if err := f.db.SaveDeliveryOutcome(ctx, item.ID, attempt.ID, "accepted_by_smtp", "data_reply", 250, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveDeliveryOutcome(ctx, item.ID, attempt.ID, "failed", "data_reply", 250, "late"); !errors.Is(err, ErrFenced) {
		t.Fatalf("second outcome overwrote: %v", err)
	}
	// A retry round for the same review cannot reserve the consumed item,
	// and the original round key replays the same round instead of forking.
	if _, err := f.db.FinishRound(ctx, f.owner, round.ID, RoundCompleted, "fixture", "send_recorded", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	retry := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID+":retry")
	if _, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, item.ID, retry.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("consumed item reserved again: %v", err)
	}
	again, err := f.db.RoundByRequest(ctx, f.owner, "delivery:"+f.review.ID)
	if err != nil || again.ID != round.ID {
		t.Fatalf("round key forked: %+v %v", again, err)
	}
	var attempts int
	if err := f.db.db.QueryRow(`SELECT COUNT(*) FROM round_attempts WHERE round_id=?`, round.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("duplicate attempts: %d %v", attempts, err)
	}
}

func TestPrepsendUncertainProtectedAndReconcileReadOnly(t *testing.T) {
	ctx := context.Background()
	f := setupPrepsendReady(t, "ps-uncertain")
	prepsendApprove(t, f)
	// One reconcile check charges a request plus a tool, so this round
	// carries headroom beyond the service's send-sized allowance.
	round := prepsendStartDeliverRoundLimits(t, f, "delivery:"+f.review.ID, storeAllowance(3, 1, 2))
	item := f.review.Items[0]
	attempt, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, item.ID, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimDeliveryItem(ctx, f.owner, f.review.ID, item.ID, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveDeliveryOutcome(ctx, item.ID, attempt.ID, "uncertain", "data_reply", 0, "response lost"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PauseUncertainDelivery(ctx, round.ID, attempt.ID, round.Generation); err != nil {
		t.Fatal(err)
	}
	paused, err := f.db.Round(ctx, round.ID)
	if err != nil || paused.State != RoundPaused {
		t.Fatalf("uncertain did not pause: %+v %v", paused, err)
	}
	// No retry commission can start while the paused round is active.
	if _, _, err := f.db.StartRound(ctx, f.owner, StartRoundInput{RequestKey: "delivery:" + f.review.ID + ":paused",
		Intent: "Deliver only the exact approved application review " + f.review.ID, Outcome: "deliver",
		ProfileVersion: f.review.Items[0].ProfileRevision,
		Scope: RoundScope{InputRefs: []string{"delivery_review:" + f.review.ID},
			Resources:  []string{"delivery:" + item.ID, "campaign:active"},
			Operations: []string{RoundDeliverApplication, RoundJevRequest}},
		Limits: storeAllowance(2, 1, 1), Deadline: time.Now().Add(10 * time.Minute)}); !errors.Is(err, ErrActiveRound) {
		t.Fatalf("retry started while paused round active: %v", err)
	}
	check, err := f.db.BeginRoundReconciliation(ctx, round.ID, attempt.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.FinishRoundReconciliation(ctx, check, AttemptObservedFailure, json.RawMessage(`{"observed":"remote_rejected"}`)); err != nil {
		t.Fatal(err)
	}
	// Reconciliation records the remote observation; the delivery item keeps
	// its uncertain snapshot and gains no second attempt.
	reread, err := f.db.DeliveryReview(ctx, f.review.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := reread.Items[0]
	if got.State != "uncertain" || got.AttemptID != attempt.ID || got.RoundID != round.ID {
		t.Fatalf("reconcile rewrote the item: %+v", got)
	}
	if string(got.MIMEBytes) != string(item.MIMEBytes) || got.MIMESHA256 != item.MIMESHA256 ||
		got.Recipient != item.Recipient || got.Subject != item.Subject || got.Body != item.Body {
		t.Fatal("reconcile mutated the attempt snapshot")
	}
	var attempts int
	if err := f.db.db.QueryRow(`SELECT COUNT(*) FROM round_attempts WHERE round_id=?`, round.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("reconcile duplicated attempts: %d %v", attempts, err)
	}
	// Even after the paused round is closed, the uncertain item cannot be
	// reserved from a fresh round: uncertainty never auto-retries.
	if _, err := f.db.FinishRound(ctx, f.owner, round.ID, RoundCompleted, "fixture_closed", "submission_unverified", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	retry := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID+":retry")
	if _, err := f.db.ReserveDeliveryAttempt(ctx, f.owner, f.review.ID, item.ID, retry.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("uncertain item reserved again: %v", err)
	}
}

func TestPrepsendAttemptSnapshotFrozenAcrossVersions(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	f := setupPrepsendReady(t, "ps-frozen")
	prepsendApprove(t, f)
	round := prepsendStartDeliverRound(t, f, "delivery:"+f.review.ID)
	item := f.review.Items[0]
	attempt, err := f.db.ReserveDeliveryAttempt(ctx, owner, f.review.ID, item.ID, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimDeliveryItem(ctx, owner, f.review.ID, item.ID, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveDeliveryOutcome(ctx, item.ID, attempt.ID, "accepted_by_smtp", "data_reply", 250, "accepted"); err != nil {
		t.Fatal(err)
	}
	text := "Edited after the attempt."
	edited, _, err := f.db.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, MaterialEditInput{
		RequestKey: "ps-frozen-edit", ExpectedVersion: 1, Text: text,
		Pack: materialRevisionPackFor(t, f.mat, MaterialOriginDirectEdit, text, nil)})
	if err != nil || edited.Version != 2 {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	rewrite := materialRewriteTexts(f.mat, map[int]string{
		0: "New motivation.", 1: "New Go history.", 2: "New extra.", 3: "New availability."})
	if _, _, err := f.db.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, MaterialRewriteInput{
		RequestKey: "ps-frozen-rewrite", ExpectedVersion: 2, Texts: rewrite,
		Pack: materialRevisionPackFor(t, f.mat, MaterialOriginRewrite, "",
			map[string]string{
				f.check.Questions[0].ID: "New motivation.",
				f.check.Questions[1].ID: "New Go history.",
				f.check.Questions[2].ID: "New extra.",
				f.check.Questions[3].ID: "New availability.",
			})}); err != nil {
		t.Fatal(err)
	}
	reread, err := f.db.DeliveryReview(ctx, f.review.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := reread.Items[0]
	if got.State != "accepted_by_smtp" || got.AttemptID != attempt.ID || got.RoundID != round.ID ||
		got.PackID != item.PackID || got.PackContentSHA256 != item.PackContentSHA256 {
		t.Fatalf("attempt identity drifted: %+v", got)
	}
	if string(got.MIMEBytes) != string(item.MIMEBytes) || got.MIMESHA256 != item.MIMESHA256 ||
		got.Recipient != item.Recipient || got.Sender != item.Sender || got.Subject != item.Subject ||
		got.Body != item.Body || got.MessageID != item.MessageID || got.AttachmentSHA256 != item.AttachmentSHA256 {
		t.Fatal("attempt snapshot changed under newer versions")
	}
	one, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != f.version.PackID {
		t.Fatalf("attempted v1 mutated: %+v %v", one, err)
	}
}
