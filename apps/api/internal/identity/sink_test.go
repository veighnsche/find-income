package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func seedSinkRound(t *testing.T, s *store.Store, roundID string, attempts ...string) {
	t.Helper()
	ctx := context.Background()
	if err := s.ResearchWrite(ctx, func(db store.ResearchDB) error {
		if err := store.SeedResearchRound(ctx, db, roundID, attempts[0]); err != nil {
			return err
		}
		for _, attempt := range attempts[1:] {
			if _, err := db.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,?,'research',1,'reserved',0,0,0,0,?,?)`,
				attempt, roundID, "attempt-"+attempt,
				store.FixtureSHA256("attempt-"+attempt), "research.fetch",
				store.FixtureTime, store.FixtureTime); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedJevAttempt(t *testing.T, s *store.Store, jevID, roundID, attemptID string) {
	t.Helper()
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		return store.SeedResearchJevAttempt(context.Background(), db, jevID, roundID, attemptID, 0)
	}); err != nil {
		t.Fatal(err)
	}
}

// sinkRecord mimics the Jev handler's output encoding: snake_case evidence
// refs and candidate sets, camelCase questions/answers.
func sinkRecord(id, runID, jevID string, profile int64, rubric, reuseSeed string) jevassess.DynamicAssessmentRecord {
	return jevassess.DynamicAssessmentRecord{
		ID:            id,
		RunID:         runID,
		JevAttemptID:  jevID,
		Purpose:       "role_fit",
		QuestionsJSON: []byte(`[{"id":"q1","text":"Fit?","alternatives":[{"id":"yes","label":"Yes"}],"abstainAllowed":true}]`),
		EvidenceRefsJSON: []byte(`[{"capture_id":"cap-crisp","span_start":0,"span_end":64},` +
			`{"capture_id":"cap-crisp","span_start":64,"span_end":128}]`),
		ProfileVersion:   profile,
		RubricVersion:    rubric,
		CandidatesJSON:   []byte(`[{"candidate_id":"opp-1","kind":"opportunity","revision":2}]`),
		CandidateSetHash: store.FixtureSHA256("candidates-" + id),
		RequestedModel:   "jev-1.13.0",
		ReuseKey:         store.FixtureSHA256("reuse-" + reuseSeed),
		Status:           store.DynamicAssessmentSucceeded,
		AnswersJSON:      []byte(`[{"questionId":"q1","answerId":"yes"}]`),
	}
}

func TestSinkPersistsAssessmentWithLinks(t *testing.T) {
	s := openMatchStore(t)
	seedSinkRound(t, s, "round-1", "attempt-1")
	seedJevAttempt(t, s, "jev-1", "round-1", "attempt-1")
	seedCapture(t, s, "cap-crisp", "https://example.test/crisp", "crisp posting body fixture text for link tests............")

	sink := &StoreSink{Store: s}
	rec := sinkRecord("jda_v1", "round-1", "jev-1", 1, "rubric-v1", "v1")
	if err := sink.SaveDynamicAssessment(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(context.Background(), func(r store.Reader) error {
		row, err := store.GetDynamicAssessment(context.Background(), r, "jda_v1")
		if err != nil {
			return err
		}
		if row.ActorKind != store.FixtureActorKind || row.ActorID != store.FixtureActorID {
			t.Fatalf("actor must resolve from the run: %+v", row)
		}
		if row.RoundID != "round-1" || row.JevAttemptID != "jev-1" || row.Purpose != "role_fit" ||
			row.ProfileVersion != 1 || row.RubricVersion != "rubric-v1" ||
			row.CandidateSetHash != rec.CandidateSetHash || row.ReuseKey != rec.ReuseKey ||
			row.RequestedModel != "jev-1.13.0" || row.Status != "succeeded" || row.SupersedesID != "" {
			t.Fatalf("row columns: %+v", row)
		}
		if row.QuestionsJSON != string(rec.QuestionsJSON) || row.EvidenceRefsJSON != string(rec.EvidenceRefsJSON) ||
			row.CandidatesJSON != string(rec.CandidatesJSON) || row.AnswersJSON != string(rec.AnswersJSON) {
			t.Fatalf("row JSON columns mangled: %+v", row)
		}
		links, err := store.ListAssessmentCaptures(context.Background(), r, "jda_v1")
		if err != nil || len(links) != 2 {
			t.Fatalf("links: %+v %v", links, err)
		}
		back, err := store.ListAssessmentsByCapture(context.Background(), r, "cap-crisp")
		if err != nil || len(back) != 2 || back[0].AssessmentID != "jda_v1" {
			t.Fatalf("capture walk: %+v %v", back, err)
		}
		reused, err := store.GetDynamicAssessmentByReuseKey(context.Background(), r,
			store.FixtureActorKind, store.FixtureActorID, rec.ReuseKey)
		if err != nil || reused.ID != "jda_v1" {
			t.Fatalf("reuse lookup: %+v %v", reused, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSinkReassessmentChainsWithoutRefetch(t *testing.T) {
	s := openMatchStore(t)
	seedSinkRound(t, s, "round-1", "attempt-1", "attempt-2")
	seedJevAttempt(t, s, "jev-1", "round-1", "attempt-1")
	seedJevAttempt(t, s, "jev-2", "round-1", "attempt-2")
	seedCapture(t, s, "cap-crisp", "https://example.test/crisp", "crisp posting body fixture text for link tests............")

	sink := &StoreSink{Store: s}
	ctx := context.Background()
	v1 := sinkRecord("jda_v1", "round-1", "jev-1", 1, "rubric-v1", "v1")
	if err := sink.SaveDynamicAssessment(ctx, v1); err != nil {
		t.Fatal(err)
	}
	// Brief change: new attempt, new reuse key, same evidence. No capture
	// reader exists in this test: reassessment binds stored refs only.
	v2 := sinkRecord("jda_v2", "round-1", "jev-2", 2, "rubric-v2", "v2")
	v2.SupersedesID = "jda_v1"
	v2.AnswersJSON = []byte(`[{"questionId":"q1","answerId":"no"}]`)
	if err := sink.SaveDynamicAssessment(ctx, v2); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(r store.Reader) error {
		gotV2, err := store.GetDynamicAssessment(ctx, r, "jda_v2")
		if err != nil || gotV2.SupersedesID != "jda_v1" {
			t.Fatalf("chain link: %+v %v", gotV2, err)
		}
		if gotV2.ProfileVersion != 2 || gotV2.RubricVersion != "rubric-v2" || gotV2.ReuseKey == v1.ReuseKey {
			t.Fatalf("v2 must bind the new brief with a new reuse key: %+v", gotV2)
		}
		gotV1, err := store.GetDynamicAssessment(ctx, r, "jda_v1")
		if err != nil || gotV1.AnswersJSON != string(v1.AnswersJSON) || gotV1.SupersedesID != "" {
			t.Fatalf("v1 must stay immutable: %+v %v", gotV1, err)
		}
		links1, err := store.ListAssessmentCaptures(ctx, r, "jda_v1")
		if err != nil {
			return err
		}
		links2, err := store.ListAssessmentCaptures(ctx, r, "jda_v2")
		if err != nil {
			return err
		}
		if len(links1) != 2 || len(links2) != 2 {
			t.Fatalf("both assessments cite the same capture: %+v %+v", links1, links2)
		}
		for i := range links1 {
			if links1[i].CaptureID != links2[i].CaptureID || links1[i].SpanStart != links2[i].SpanStart {
				t.Fatalf("reassessment must reuse the stored spans: %+v %+v", links1, links2)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSinkRejectsBadRecords(t *testing.T) {
	s := openMatchStore(t)
	seedSinkRound(t, s, "round-1", "attempt-1")
	seedJevAttempt(t, s, "jev-1", "round-1", "attempt-1")
	seedCapture(t, s, "cap-crisp", "https://example.test/crisp", "crisp posting body fixture text for link tests............")
	sink := &StoreSink{Store: s}
	ctx := context.Background()

	if err := (&StoreSink{}).SaveDynamicAssessment(ctx, sinkRecord("jda_x", "round-1", "jev-1", 1, "rubric-v1", "x")); err == nil {
		t.Fatal("nil store must fail")
	}
	rec := sinkRecord("jda_norun", "missing-round", "jev-1", 1, "rubric-v1", "norun")
	if err := sink.SaveDynamicAssessment(ctx, rec); err == nil {
		t.Fatal("unknown run must fail")
	}
	rec = sinkRecord("jda_nopred", "round-1", "jev-1", 1, "rubric-v1", "nopred")
	rec.SupersedesID = "jda_missing"
	if err := sink.SaveDynamicAssessment(ctx, rec); err == nil {
		t.Fatal("unknown predecessor must fail")
	}
	rec = sinkRecord("jda_noev", "round-1", "jev-1", 1, "rubric-v1", "noev")
	rec.EvidenceRefsJSON = []byte(`[]`)
	if err := sink.SaveDynamicAssessment(ctx, rec); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("evidence-less assessment: %v", err)
	}
	rec = sinkRecord("jda_badspan", "round-1", "jev-1", 1, "rubric-v1", "badspan")
	rec.EvidenceRefsJSON = []byte(`[{"capture_id":"cap-crisp","span_start":9,"span_end":9}]`)
	if err := sink.SaveDynamicAssessment(ctx, rec); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty span: %v", err)
	}
	rec = sinkRecord("jda_nojev", "round-1", "missing-attempt", 1, "rubric-v1", "nojev")
	if err := sink.SaveDynamicAssessment(ctx, rec); err == nil {
		t.Fatal("unknown jev attempt must fail the FK")
	}
	first := sinkRecord("jda_dup1", "round-1", "jev-1", 1, "rubric-v1", "dup")
	if err := sink.SaveDynamicAssessment(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := sinkRecord("jda_dup2", "round-1", "jev-1", 1, "rubric-v1", "dup")
	if err := sink.SaveDynamicAssessment(ctx, second); err == nil {
		t.Fatal("duplicate reuse key must fail loudly, never double-persist")
	}
	if err := s.Read(ctx, func(r store.Reader) error {
		var n int
		if err := r.QueryRowContext(ctx, `SELECT count(*) FROM jev_assessments_dynamic`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("failed saves must leave no partial rows: %d", n)
		}
		return r.QueryRowContext(ctx, `SELECT count(*) FROM jev_assessment_captures`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}
