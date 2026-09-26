package researchmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// observeFull records a fresh success and returns its observation/capture ids.
func observeFull(t *testing.T, env *testEnv, receiptID, body string) (obsID, captureID string) {
	t.Helper()
	ctx := context.Background()
	desc := testDescriptor()
	fp, err := desc.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-"+receiptID)
	out, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt(receiptID, fp), Capture: testCapture(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.ObservationID, out.CaptureID
}

func TestResolveReceiptAndForgedRejection(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	obsID, _ := observeFull(t, env, "rcpt-ok", "<html>authentic</html>")
	rec, err := env.caps.ResolveReceipt(ctx, "rcpt-ok")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fingerprint != fp || rec.Status != researchcontract.ReceiptOK {
		t.Fatalf("bad resolved receipt: %+v", rec)
	}
	obs, err := env.caps.ObservationForReceipt(ctx, "rcpt-ok")
	if err != nil {
		t.Fatal(err)
	}
	if obs.ID != obsID {
		t.Fatal("receipt does not resolve to its observation")
	}

	// Forged and unknown receipts fail server-side.
	for _, forged := range []string{"rcpt-forged", "../../etc/passwd", "", "note:1"} {
		if _, err := env.caps.ResolveReceipt(ctx, forged); err == nil {
			t.Fatalf("forged receipt %q resolved", forged)
		} else {
			mustContractCode(t, err, researchcontract.OutcomeNotFound)
		}
	}

	// A receipt file without a recorded observation is not trusted.
	orphan := testReceipt("rcpt-orphan", fp)
	if err := env.arts.PutReceipt(orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := env.caps.ResolveReceipt(ctx, "rcpt-orphan"); err == nil {
		t.Fatal("unrecorded receipt resolved")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeNotFound)
	}
}

func TestAlteredArtifactRejected(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	_, captureID := observeFull(t, env, "rcpt-a", "<html>pristine</html>")

	var capt store.SourceCapture
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		capt, err = store.GetSourceCapture(ctx, r, captureID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.caps.OpenCapture(ctx, captureID); err != nil {
		t.Fatal(err)
	}

	// Tamper with the stored bytes out of band.
	if err := os.WriteFile(filepath.Join(env.artsDir, capt.ArtifactRef), []byte("<html>TAMPERED</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.caps.OpenCapture(ctx, captureID); err == nil {
		t.Fatal("altered artifact opened")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeInvalid)
	}
}

func TestOpenCaptureByContentSHA(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	body := "<html>content-addressed</html>"
	_, captureID := observeFull(t, env, "rcpt-c", body)

	sum := sha256.Sum256([]byte(body))
	contentSHA := hex.EncodeToString(sum[:])
	desc, rc, err := env.caps.OpenCapture(ctx, contentSHA)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if desc.ID != contentSHA || desc.SHA256 != contentSHA {
		t.Fatalf("content view not content-addressed: %+v", desc)
	}
	if desc.Operation != researchcontract.OperationFetch || desc.Fingerprint == "" {
		t.Fatalf("retrieval binding missing: %+v", desc)
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Fatal("capture bytes mismatch")
	}
	var capt store.SourceCapture
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		capt, err = store.GetSourceCapture(ctx, r, captureID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if capt.ContentSHA256 != contentSHA || !desc.Complete || desc.IsSnippet {
		t.Fatalf("bad capture row/view: %+v %+v", capt, desc)
	}
}

func TestVerifyExcerpt(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	body := "<html>night-shift nursing, Ghent</html>"
	_, captureID := observeFull(t, env, "rcpt-e", body)

	span := []byte("nursing")
	start := strings.Index(body, "nursing")
	end := start + len(span)
	if start < 0 || body[start:end] != "nursing" {
		t.Fatalf("test span misaligned: %q", body[start:end])
	}
	sum := sha256.Sum256(span)
	good := hex.EncodeToString(sum[:])
	if err := env.caps.VerifyExcerpt(ctx, captureID, int64(start), int64(end), good); err != nil {
		t.Fatal(err)
	}
	// Wrong hash, bad spans and note ids all fail.
	badSum := sha256.Sum256([]byte("nurse!!"))
	if err := env.caps.VerifyExcerpt(ctx, captureID, int64(start), int64(end), hex.EncodeToString(badSum[:])); err == nil {
		t.Fatal("unsupported quote verified")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeInvalid)
	}
	if err := env.caps.VerifyExcerpt(ctx, captureID, -1, 5, good); err == nil {
		t.Fatal("negative span verified")
	}
	if err := env.caps.VerifyExcerpt(ctx, captureID, 0, int64(len(body)+1), good); err == nil {
		t.Fatal("overlong span verified")
	}
	note, err := env.mem.AddNote(ctx, NoteInput{RoundID: "round-1",
		BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
		Intent: "excerpt firewall probe", Conclusion: "notes are not evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.caps.VerifyExcerpt(ctx, note.ID, 0, 5, good); err == nil {
		t.Fatal("note id verified as capture")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeNotFound)
	}
}

func TestRequireFullCapture(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	_, fullID := observeFull(t, env, "rcpt-full", "<html>full vacancy</html>")
	if err := env.caps.RequireFullCapture(ctx, fullID); err != nil {
		t.Fatal(err)
	}

	// Search snippets default to is_snippet and block complete claims.
	searchDesc := testDescriptor()
	searchDesc.Operation = researchcontract.OperationSearch
	searchDesc.Backend = "fixture-search"
	searchFP, _ := searchDesc.Fingerprint()
	claim, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, searchDesc, "key-search")
	if err != nil {
		t.Fatal(err)
	}
	snipCap := testCapture(`[{"title":"Nurse","snippet":"..."}]`)
	snipCap.Provenance = researchcontract.ProvenanceSearchResult
	rec := testReceipt("rcpt-snip", searchFP)
	rec.Operation = researchcontract.OperationSearch
	snipOut, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: searchDesc.URLOrQuery,
		Outcome: store.ObservationSuccess, Provenance: researchcontract.ProvenanceSearchResult,
		Receipt: rec, Capture: snipCap,
	})
	if err != nil {
		t.Fatal(err)
	}
	var snip store.SourceCapture
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		snip, err = store.GetSourceCapture(ctx, r, snipOut.CaptureID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !snip.IsSnippet {
		t.Fatal("search capture did not default to is_snippet")
	}
	if err := env.caps.RequireFullCapture(ctx, snipOut.CaptureID); err == nil {
		t.Fatal("snippet satisfied a complete claim")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeCaptureIncomplete)
	}

	// Explicitly truncated captures also block complete claims.
	_ = fp
	truncDesc := testDescriptor()
	truncDesc.URLOrQuery = "https://example.com/jobs/long"
	claim2, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, truncDesc, "key-trunc")
	if err != nil {
		t.Fatal(err)
	}
	truncFP, _ := truncDesc.Fingerprint()
	truncCap := testCapture("<html>partial…</html>")
	truncCap.Completeness = store.CaptureTruncated
	rec2 := testReceipt("rcpt-trunc", truncFP)
	rec2.Truncated = true
	truncOut, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim2.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: truncDesc.URLOrQuery,
		Outcome: store.ObservationSuccess, Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt: rec2, Capture: truncCap, TruncationNote: "cut after 20 bytes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.caps.RequireFullCapture(ctx, truncOut.CaptureID); err == nil {
		t.Fatal("truncated capture satisfied a complete claim")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeCaptureIncomplete)
	}
}

func TestArtifactStoreDiscipline(t *testing.T) {
	arts, err := OpenArtifactStore(filepath.Join(t.TempDir(), "arts"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenArtifactStore("relative/path"); err == nil {
		t.Fatal("relative artifact root accepted")
	}
	data := []byte("bytes")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	ref, err := arts.PutBlob(good, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := arts.PutBlob(good, data); err != nil {
		t.Fatal("identical re-put must be idempotent")
	}
	if _, err := arts.PutBlob(good, []byte("other.")); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, err := arts.OpenBlob("nope"); err == nil {
		t.Fatal("bad ref opened")
	}
	if _, err := arts.OpenBlob(ref); err != nil {
		t.Fatal(err)
	}
	rec := testReceipt("rcpt-x", store.FixtureSHA256("x"))
	if err := arts.PutReceipt(rec); err != nil {
		t.Fatal(err)
	}
	if err := arts.PutReceipt(rec); err != nil {
		t.Fatal("identical receipt re-put must be idempotent")
	}
	rec.Truncated = true
	if err := arts.PutReceipt(rec); err == nil {
		t.Fatal("receipt overwrite accepted")
	}
}

func TestResolveReceiptByCapture(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	_, captureID := observeFull(t, env, "rcpt-cap", "<html>by capture</html>")
	rec, err := env.caps.ResolveReceiptByCapture(ctx, captureID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "rcpt-cap" {
		t.Fatalf("resolved receipt = %q, want rcpt-cap", rec.ID)
	}
	if _, err := env.caps.ResolveReceiptByCapture(ctx, "cap-unknown"); err == nil {
		t.Fatal("unknown capture resolved")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeNotFound)
	}
	if _, err := env.caps.ResolveReceiptByCapture(ctx, ""); err == nil {
		t.Fatal("empty capture resolved")
	}
}
