package researchmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestResearchMemoryHandlerOps(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	h := NewResearchMemoryHandler(env.mem)
	desc := testDescriptor()

	lookup, err := h.Handle(ctx, ResearchMemoryArgs{Op: "lookup", RunID: "round-1", Request: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if lookup["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("lookup: %+v", lookup)
	}

	claim, err := h.Handle(ctx, ResearchMemoryArgs{Op: "claim", RunID: "round-1",
		Owner: "attempt-1", Generation: 1, IdempotencyKey: "k1", Request: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if claim["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("claim: %+v", claim)
	}
	lease, ok := claim["lease"].(map[string]any)
	if !ok || lease["leaseId"] == "" || lease["expiresAt"] == "" {
		t.Fatalf("bad lease map: %+v", claim)
	}

	// A rival claim is fenced in-band (nil error: the bridge swallows errors).
	rival, err := h.Handle(ctx, ResearchMemoryArgs{Op: "claim", RunID: "round-1",
		Owner: "worker-b", Generation: 1, Request: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if rival["outcome"] != string(researchcontract.OutcomeClaimedElsewhere) || rival["nextSteps"] == nil {
		t.Fatalf("rival: %+v", rival)
	}

	renewed, err := h.Handle(ctx, ResearchMemoryArgs{Op: "renew",
		LeaseID: lease["leaseId"].(string), Owner: "attempt-1", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if renewed["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("renew: %+v", renewed)
	}

	released, err := h.Handle(ctx, ResearchMemoryArgs{Op: "release",
		LeaseID: lease["leaseId"].(string), Owner: "attempt-1", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if released["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("release: %+v", released)
	}

	concluded, err := h.Handle(ctx, ResearchMemoryArgs{Op: "conclude", RunID: "round-1",
		Conclusion: &ConclusionArgs{BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent: "map night-shift boards", Conclusion: "two boards worth a revisit"}})
	if err != nil {
		t.Fatal(err)
	}
	if concluded["outcome"] != string(researchcontract.OutcomeOK) || concluded["noteId"] == "" {
		t.Fatalf("conclude: %+v", concluded)
	}

	// Intent search surfaces overlapping investigations.
	withIntent, err := h.Handle(ctx, ResearchMemoryArgs{Op: "lookup", RunID: "round-1",
		Request: &desc, Intent: "night-shift boards"})
	if err != nil {
		t.Fatal(err)
	}
	overlaps, ok := withIntent["overlappingInvestigations"].([]map[string]any)
	if !ok || len(overlaps) != 1 {
		t.Fatalf("overlaps: %+v", withIntent)
	}

	if _, err := h.Handle(ctx, ResearchMemoryArgs{Op: "levitate"}); err != nil {
		t.Fatal(err)
	} else {
		out, _ := h.Handle(ctx, ResearchMemoryArgs{Op: "levitate"})
		if out["outcome"] != string(researchcontract.OutcomeInvalid) {
			t.Fatalf("bad op: %+v", out)
		}
	}
}

func TestResearchMemoryHandlerCheckpoint(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	h := NewResearchMemoryHandler(env.mem)

	missing, err := h.Handle(ctx, ResearchMemoryArgs{Op: "checkpoint", RunID: "round-1"})
	if err != nil {
		t.Fatal(err)
	}
	if missing["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("checkpoint without row: %+v", missing)
	}
	if err := env.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, "round-1", researchcontract.Checkpoint{
			ProfileVersion: 3, RubricVersion: "rubric-v2",
			EvidenceIDs: []string{"ev-1"}, NextWork: []string{"revisit board B"},
			Generation: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	got, err := h.Handle(ctx, ResearchMemoryArgs{Op: "checkpoint", RunID: "round-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("checkpoint: %+v", got)
	}
	bv, ok := got["briefVersion"].(map[string]any)
	if !ok || bv["profileVersion"] != int64(3) {
		t.Fatalf("briefVersion: %+v", got)
	}
}

func TestEvidenceCaptureHandler(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	h := NewEvidenceCaptureHandler(env.caps)
	body := "<html>full vacancy description</html>"
	_, captureID := observeFull(t, env, "rcpt-ev", body)

	got, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-ev", AssertComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if got["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("capture: %+v", got)
	}
	if got["captureId"] == "" || got["contentRef"] == "" || got["isSnippet"] != false {
		t.Fatalf("capture fields: %+v", got)
	}
	extent, ok := got["extent"].(map[string]any)
	if !ok || extent["complete"] != true || extent["bytes"] != int64(len(body)) {
		t.Fatalf("extent: %+v", got)
	}
	if ref := got["contentRef"].(string); strings.Contains(ref, "/") || !strings.HasPrefix(ref, "capture:") {
		t.Fatalf("contentRef leaks a path: %q", ref)
	}

	// Quoted spans verify against the immutable bytes.
	start := strings.Index(body, "vacancy")
	sum := sha256.Sum256([]byte("vacancy"))
	spanOK, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-ev",
		Excerpt: &ExcerptArgs{SpanStart: int64(start), SpanEnd: int64(start + 7),
			ExcerptSHA256: hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if spanOK["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("excerpt: %+v", spanOK)
	}
	badSum := sha256.Sum256([]byte("vacanc!"))
	spanBad, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-ev",
		Excerpt: &ExcerptArgs{SpanStart: int64(start), SpanEnd: int64(start + 7),
			ExcerptSHA256: hex.EncodeToString(badSum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if spanBad["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("bad excerpt: %+v", spanBad)
	}

	// Forged receipts fail in-band.
	forged, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-forged"})
	if err != nil {
		t.Fatal(err)
	}
	if forged["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("forged: %+v", forged)
	}

	// Snippets block complete claims through the handler too.
	desc := testDescriptor()
	desc.Operation = researchcontract.OperationSearch
	desc.Backend = "fixture-search"
	claim, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, desc, "key-snip")
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := desc.Fingerprint()
	rec := testReceipt("rcpt-snip-h", fp)
	rec.Operation = researchcontract.OperationSearch
	snipCap := testCapture(`[{"title":"Nurse"}]`)
	snipCap.Provenance = researchcontract.ProvenanceSearchResult
	if _, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome: store.ObservationSuccess, Provenance: researchcontract.ProvenanceSearchResult,
		Receipt: rec, Capture: snipCap,
	}); err != nil {
		t.Fatal(err)
	}
	snip, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-snip-h", AssertComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if snip["outcome"] != string(researchcontract.OutcomeCaptureIncomplete) {
		t.Fatalf("snippet complete-claim: %+v", snip)
	}
	_ = captureID
}

func TestEvidenceCaptureHandlerFailedReceipt(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	h := NewEvidenceCaptureHandler(env.caps)
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	rec := testReceipt("rcpt-fail", fp)
	rec.Status = researchcontract.ReceiptFailed
	rec.ErrorCode = "http_500"
	if _, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome: store.ObservationFailed, Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt: rec, ErrorCode: "http_500",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := h.Handle(ctx, EvidenceCaptureArgs{Receipt: "rcpt-fail"})
	if err != nil {
		t.Fatal(err)
	}
	if got["outcome"] != string(researchcontract.OutcomeInvalid) || got["errorCode"] != "http_500" {
		t.Fatalf("failed receipt: %+v", got)
	}
}

func TestContextReadHandler(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	env.auth.ledger = researchcontract.UsageLedger{
		Enforced: researchcontract.Allowance{Requests: 60},
		Reserved: researchcontract.Allowance{Requests: 2},
		Observed: researchcontract.Allowance{Requests: 5},
		Unknown:  true,
	}
	h := NewContextReadHandler(env.db, env.mem, env.auth)

	if err := env.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, "round-1", researchcontract.Checkpoint{
			ProfileVersion: 2, RubricVersion: "rubric-v1",
			EvidenceIDs: []string{"ev-1"},
			UnresolvedAttempts: []researchcontract.UnresolvedAttempt{
				{AttemptID: "attempt-9", Reason: "disconnect during fetch"}},
			NextWork: []string{"retry board B"}, Generation: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")

	got, err := h.Handle(ctx, ContextReadArgs{Checkpoint: "round-1", Brief: true})
	if err != nil {
		t.Fatal(err)
	}
	if got["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("context: %+v", got)
	}
	bv := got["briefVersion"].(map[string]any)
	if bv["profileVersion"] != int64(2) || bv["rubricVersion"] != "rubric-v1" {
		t.Fatalf("briefVersion: %+v", got)
	}
	active := got["activeClaims"].([]map[string]any)
	if len(active) != 1 || active[0]["fingerprint"] == "" {
		t.Fatalf("activeClaims: %+v", got)
	}
	history := got["history"].([]map[string]any)
	if len(history) == 0 || history[0]["eventId"] == "" {
		t.Fatalf("history: %+v", got)
	}
	limits := got["runLimits"].(map[string]any)
	if limits["unknown"] != true {
		t.Fatalf("runLimits: %+v", got)
	}
	if got["deferred"] == nil {
		t.Fatal("brief facts must be explicitly deferred, not invented")
	}

	// Paging cursors round-trip; bad cursors fail in-band.
	paged, err := h.Handle(ctx, ContextReadArgs{Checkpoint: "round-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := paged["nextCursor"].(string)
	if cursor == "" {
		t.Fatalf("paging: %+v", paged)
	}
	badCursor, err := h.Handle(ctx, ContextReadArgs{Checkpoint: "round-1", Cursor: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if badCursor["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("bad cursor: %+v", badCursor)
	}

	// Record subjects belong to the D-owned reader, not this handler.
	rec, err := h.Handle(ctx, ContextReadArgs{Subject: &ContextSubject{Kind: "opportunity", ID: "opp-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if rec["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("record subject: %+v", rec)
	}
	// Investigation subjects add notes.
	if _, err := env.mem.AddNote(ctx, NoteInput{RoundID: "round-1",
		BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
		Intent: "context probe", Conclusion: "visible"}); err != nil {
		t.Fatal(err)
	}
	inv, err := h.Handle(ctx, ContextReadArgs{Subject: &ContextSubject{Kind: "investigation", ID: "round-1"}})
	if err != nil {
		t.Fatal(err)
	}
	notes, ok := inv["notes"].([]map[string]any)
	if !ok || len(notes) != 1 {
		t.Fatalf("investigation notes: %+v", inv)
	}
}
