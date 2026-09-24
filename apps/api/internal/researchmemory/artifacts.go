package researchmemory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

var (
	blobRefShape   = regexp.MustCompile(`\Ablobs/[0-9a-f]{2}/[0-9a-f]{64}\z`)
	receiptIDShape = regexp.MustCompile(`\A[A-Za-z0-9_:.~-]{1,128}\z`)
	hexSHA256Shape = regexp.MustCompile(`\A[0-9a-f]{64}\z`)
)

// ArtifactStore is the private content-addressed blob + receipt file store.
// The root must be an absolute private directory OUTSIDE the model-writable
// workspace and the web root (a T23 wiring property; tests use t.TempDir).
// Blob refs are server-generated ("blobs/<sha-prefix>/<sha>") and never
// caller-supplied; refs, never filesystem paths, cross the tool boundary.
type ArtifactStore struct{ root string }

// OpenArtifactStore creates (0700) and opens a private artifact root.
func OpenArtifactStore(root string) (*ArtifactStore, error) {
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"artifactRoot", "artifact root must be an absolute non-root path")
	}
	dir := filepath.Clean(root)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("protect artifact root: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"artifactRoot", "artifact root must be a directory with mode 0700")
	}
	return &ArtifactStore{root: dir}, nil
}

// PutBlob persists bytes under their content sha (hex). It verifies the
// digest, refuses to overwrite differing bytes, and returns the server-side
// ref. Identical re-put is idempotent (blob-layer dedup, T06 §3).
func (a *ArtifactStore) PutBlob(contentSHA256 string, data []byte) (string, error) {
	if !hexSHA256Shape.MatchString(contentSHA256) {
		return "", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"contentSHA256", "content sha256 must be 64 lowercase hex chars")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != contentSHA256 {
		return "", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"bytes", "blob bytes do not match the claimed content sha256")
	}
	ref := "blobs/" + contentSHA256[:2] + "/" + contentSHA256
	path := filepath.Join(a.root, ref)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create blob dir: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) {
			return "", researchcontract.NewError(researchcontract.OutcomeConflict,
				"artifactRef", "immutable blob already stored with different bytes")
		}
		return ref, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read existing blob: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}
	return ref, nil
}

// OpenBlob reads a blob and verifies its bytes against the content sha
// encoded in the ref. Tampered bytes fail closed with outcome invalid.
func (a *ArtifactStore) OpenBlob(ref string) ([]byte, error) {
	if !blobRefShape.MatchString(ref) {
		return nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"artifactRef", "unknown artifact ref")
	}
	data, err := os.ReadFile(filepath.Join(a.root, ref))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"artifactRef", "artifact bytes are missing")
		}
		return nil, fmt.Errorf("read blob: %w", err)
	}
	want := ref[len("blobs/xx/"):]
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"artifactRef", "artifact bytes fail integrity verification (altered artifact)")
	}
	return data, nil
}

// PutReceipt persists an executor-issued receipt pre-commit (like artifact
// bytes, T03 §4 T-observe). Receipt files are write-once: re-put of a
// different receipt under the same id is rejected.
func (a *ArtifactStore) PutReceipt(rec researchcontract.ExecutionReceipt) error {
	if !receiptIDShape.MatchString(rec.ID) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "receipt id has an unsupported shape")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := filepath.Join(a.root, "receipts", rec.ID+".json")
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, raw) {
			return researchcontract.NewError(researchcontract.OutcomeConflict,
				"receipt", "receipt id already issued with a different body")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing receipt: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(a.root, "receipts"), 0700); err != nil {
		return fmt.Errorf("create receipts dir: %w", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("write receipt: %w", err)
	}
	return nil
}

// OpenReceipt reads an executor-issued receipt file. Unknown ids fail with
// outcome not_found (forged-receipt rejection happens in ResolveReceipt,
// which additionally requires a referencing observation row).
func (a *ArtifactStore) OpenReceipt(id string) (researchcontract.ExecutionReceipt, error) {
	var rec researchcontract.ExecutionReceipt
	if !receiptIDShape.MatchString(id) {
		return rec, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"receipt", "unknown receipt id")
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "receipts", id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return rec, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"receipt", "unknown receipt id")
		}
		return rec, fmt.Errorf("read receipt: %w", err)
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "stored receipt body is corrupt")
	}
	return rec, nil
}
