package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ApplicationPackMutationInput is built by the pack service, then supplied
// only through ApplyRoundMutation. No direct/public write method is exposed.
type ApplicationPackMutationInput struct {
	OpportunityID               string `json:"opportunityId"`
	ExpectedOpportunityRevision int64  `json:"expectedOpportunityRevision"`
	ExpectedProfileRevision     int64  `json:"expectedProfileRevision"`
	ContentSHA256               string `json:"contentSha256"`
	ManifestJSON                []byte `json:"manifestJson"`
	TypstSource                 []byte `json:"typstSource"`
	PDF                         []byte `json:"pdf"`
}

type ApplicationPack struct {
	ID                  string
	OpportunityID       string
	OpportunityRevision int64
	ProfileRevision     int64
	Version             int64
	ContentSHA256       string
	ManifestJSON        []byte
	TypstSource         []byte
	PDF                 []byte
	CreatedAt           string
}

func applicationPackContentHash(manifest, source, pdf []byte) string {
	encoded, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, source, pdf})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// createApplicationPackTx checks the current revisions and inserts one new
// immutable version. The caller must own round fencing, charge, audit and link.
func createApplicationPackTx(ctx context.Context, tx *sql.Tx, input ApplicationPackMutationInput) (string, int64, error) {
	if input.OpportunityID == "" || input.ExpectedOpportunityRevision < 1 || input.ExpectedProfileRevision < 1 ||
		len(input.ManifestJSON) == 0 || len(input.ManifestJSON) > 800000 || len(input.TypstSource) == 0 || len(input.TypstSource) > 100000 ||
		len(input.PDF) < 100 || len(input.PDF) > 3<<20 || !bytes.HasPrefix(input.PDF, []byte("%PDF-")) ||
		applicationPackContentHash(input.ManifestJSON, input.TypstSource, input.PDF) != input.ContentSHA256 || !json.Valid(input.ManifestJSON) {
		return "", 0, ErrInvalid
	}
	var manifest struct {
		Role struct {
			OpportunityID       string `json:"opportunityId"`
			OpportunityRevision int64  `json:"opportunityRevision"`
			ProfileRevision     int64  `json:"profileRevision"`
		} `json:"role"`
	}
	if err := json.Unmarshal(input.ManifestJSON, &manifest); err != nil ||
		manifest.Role.OpportunityID != input.OpportunityID || manifest.Role.OpportunityRevision != input.ExpectedOpportunityRevision ||
		manifest.Role.ProfileRevision != input.ExpectedProfileRevision {
		return "", 0, ErrInvalid
	}
	var opportunityRevision, profileRevision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, input.OpportunityID).Scan(&opportunityRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profileRevision); err != nil {
		return "", 0, err
	}
	if opportunityRevision != input.ExpectedOpportunityRevision || profileRevision != input.ExpectedProfileRevision {
		return "", 0, ErrConflict
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM application_packs WHERE opportunity_id=?`, input.OpportunityID).Scan(&version); err != nil {
		return "", 0, err
	}
	id, err := randomID()
	if err != nil {
		return "", 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_packs
	  (id,opportunity_id,opportunity_revision,profile_revision,version,content_sha256,manifest_json,typst_source,pdf,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`, id, input.OpportunityID, opportunityRevision, profileRevision, version,
		input.ContentSHA256, string(input.ManifestJSON), input.TypstSource, input.PDF, utcNow())
	if err != nil {
		return "", 0, fmt.Errorf("insert application pack: %w", err)
	}
	return id, version, nil
}

func (s *Store) ApplicationPack(ctx context.Context, id string) (ApplicationPack, error) {
	var pack ApplicationPack
	err := s.db.QueryRowContext(ctx, `SELECT id,opportunity_id,opportunity_revision,profile_revision,version,
	  content_sha256,manifest_json,typst_source,pdf,created_at FROM application_packs WHERE id=?`, id).Scan(
		&pack.ID, &pack.OpportunityID, &pack.OpportunityRevision, &pack.ProfileRevision, &pack.Version,
		&pack.ContentSHA256, &pack.ManifestJSON, &pack.TypstSource, &pack.PDF, &pack.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplicationPack{}, ErrNotFound
	}
	if err != nil {
		return ApplicationPack{}, err
	}
	if applicationPackContentHash(pack.ManifestJSON, pack.TypstSource, pack.PDF) != pack.ContentSHA256 {
		return ApplicationPack{}, fmt.Errorf("%w: application pack digest mismatch", ErrInvalid)
	}
	return pack, nil
}
