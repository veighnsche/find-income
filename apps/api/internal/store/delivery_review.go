package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type DeliveryDraft struct {
	PackID              string
	OpportunityID       string
	OpportunityRevision int64
	SourceSHA256        string
	ProfileRevision     int64
	PackContentSHA256   string
	RouteID             string
	RouteRevision       int64
	RouteSHA256         string
	Title               string
	CompanyName         string
	RouteExcerpt        string
	Recipient           string
	Sender              string
	Subject             string
	Body                string
	AttachmentSHA256    string
	MIMESHA256          string
	MIMEBytes           []byte
	MessageID           string
}

type DeliveryItem struct {
	ID                  string `json:"id"`
	ReviewID            string `json:"reviewId"`
	PackID              string `json:"packId"`
	OpportunityID       string `json:"opportunityId"`
	OpportunityRevision int64  `json:"opportunityRevision"`
	SourceSHA256        string `json:"sourceSha256"`
	ProfileRevision     int64  `json:"profileRevision"`
	PackContentSHA256   string `json:"packContentSha256"`
	RouteID             string `json:"routeId"`
	RouteRevision       int64  `json:"routeRevision"`
	RouteSHA256         string `json:"routeSha256"`
	Title               string `json:"title"`
	CompanyName         string `json:"companyName"`
	RouteExcerpt        string `json:"routeExcerpt"`
	Recipient           string `json:"recipient"`
	Sender              string `json:"sender"`
	Subject             string `json:"subject"`
	Body                string `json:"body"`
	AttachmentSHA256    string `json:"attachmentSha256"`
	MIMESHA256          string `json:"mimeSha256"`
	MIMEBytes           []byte `json:"-"`
	MessageID           string `json:"messageId"`
	State               string `json:"state"`
	RoundID             string `json:"roundId,omitempty"`
	AttemptID           string `json:"attemptId,omitempty"`
	SMTPStage           string `json:"smtpStage,omitempty"`
	SMTPCode            int    `json:"smtpCode,omitempty"`
	OutcomeDetail       string `json:"outcomeDetail,omitempty"`
	Current             bool   `json:"current"`
	BlockingReason      string `json:"blockingReason,omitempty"`
}

type DeliveryReview struct {
	ID             string         `json:"id"`
	MaterialSHA256 string         `json:"materialSha256"`
	ApprovedSHA256 string         `json:"approvedSha256,omitempty"`
	ApprovedAt     string         `json:"approvedAt,omitempty"`
	Items          []DeliveryItem `json:"items"`
}

func deliveryDigest(drafts []DeliveryDraft) string {
	type component struct {
		PackID, OpportunityID, SourceSHA256, PackContentSHA256, RouteID, RouteSHA256, Recipient, Sender, MIMESHA256 string
	}
	parts := make([]component, len(drafts))
	for i, d := range drafts {
		parts[i] = component{d.PackID, d.OpportunityID, d.SourceSHA256, d.PackContentSHA256, d.RouteID, d.RouteSHA256, d.Recipient, d.Sender, d.MIMESHA256}
	}
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Store) DeliveryReviewByRequest(ctx context.Context, actor Actor, requestKey string, packIDs []string) (DeliveryReview, error) {
	if !ownerRoundActor(actor) || requestKey == "" || len(requestKey) > 180 || requestKey != strings.TrimSpace(requestKey) || len(packIDs) < 1 || len(packIDs) > 3 {
		return DeliveryReview{}, ErrInvalid
	}
	var id, saved string
	err := s.db.QueryRowContext(ctx, `SELECT id,pack_ids_json FROM delivery_reviews WHERE owner_id=? AND request_key=?`, actor.ID, requestKey).Scan(&id, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryReview{}, ErrNotFound
	}
	if err != nil {
		return DeliveryReview{}, err
	}
	want, _ := json.Marshal(packIDs)
	if saved != string(want) {
		return DeliveryReview{}, ErrRoundIdempotencyConflict
	}
	return s.DeliveryReview(ctx, id)
}

func (s *Store) CreateDeliveryReview(ctx context.Context, actor Actor, requestKey string, drafts []DeliveryDraft) (DeliveryReview, error) {
	if !ownerRoundActor(actor) || requestKey == "" || len(requestKey) > 180 || requestKey != strings.TrimSpace(requestKey) || len(drafts) < 1 || len(drafts) > 3 {
		return DeliveryReview{}, ErrInvalid
	}
	packIDs := make([]string, len(drafts))
	for i, d := range drafts {
		packIDs[i] = d.PackID
	}
	packIDsJSON, _ := json.Marshal(packIDs)
	id, err := randomID()
	if err != nil {
		return DeliveryReview{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryReview{}, err
	}
	defer tx.Rollback()
	// Obtain a writer before checking mutable currentness.
	if _, err := tx.ExecContext(ctx, `UPDATE preferences_current SET version=version WHERE singleton=1`); err != nil {
		return DeliveryReview{}, err
	}
	var existingID, existingPackIDs string
	err = tx.QueryRowContext(ctx, `SELECT id,pack_ids_json FROM delivery_reviews WHERE owner_id=? AND request_key=?`, actor.ID, requestKey).Scan(&existingID, &existingPackIDs)
	if err == nil {
		_ = tx.Rollback()
		if existingPackIDs != string(packIDsJSON) {
			return DeliveryReview{}, ErrRoundIdempotencyConflict
		}
		return s.DeliveryReview(ctx, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeliveryReview{}, err
	}
	seen := map[string]bool{}
	for _, d := range drafts {
		if d.PackID == "" || seen[d.PackID] || d.RouteID == "" || d.MIMESHA256 == "" || len(d.MIMEBytes) == 0 || len(d.MIMEBytes) > 16<<20 ||
			d.OpportunityID == "" || d.Title == "" || d.CompanyName == "" || d.RouteExcerpt == "" || d.Recipient == "" || d.Sender == "" || d.Subject == "" || d.Body == "" || d.MessageID == "" ||
			d.OpportunityRevision < 1 || d.ProfileRevision < 1 || d.RouteRevision < 1 {
			return DeliveryReview{}, ErrInvalid
		}
		seen[d.PackID] = true
		h := sha256.Sum256(d.MIMEBytes)
		if hex.EncodeToString(h[:]) != d.MIMESHA256 {
			return DeliveryReview{}, ErrInvalid
		}
		if err := checkDeliveryCurrentTx(ctx, tx, d.PackID, d.OpportunityID, d.OpportunityRevision, d.ProfileRevision,
			d.SourceSHA256, d.PackContentSHA256, d.AttachmentSHA256, d.RouteID, d.RouteSHA256, d.Recipient); err != nil {
			return DeliveryReview{}, err
		}
		var title, company, excerpt string
		if err := tx.QueryRowContext(ctx, `SELECT o.title,c.name,r.source_excerpt FROM opportunities o
		 JOIN companies c ON c.id=o.company_id JOIN opportunity_routes r ON r.opportunity_id=o.id
		 WHERE o.id=? AND r.id=?`, d.OpportunityID, d.RouteID).Scan(&title, &company, &excerpt); err != nil {
			return DeliveryReview{}, err
		}
		if d.Title != title || d.CompanyName != company || d.RouteExcerpt != excerpt {
			return DeliveryReview{}, ErrInvalid
		}
	}
	digest := deliveryDigest(drafts)
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_reviews(id,owner_id,request_key,pack_ids_json,material_sha256,created_at) VALUES(?,?,?,?,?,?)`, id, actor.ID, requestKey, string(packIDsJSON), digest, utcNow()); err != nil {
		return DeliveryReview{}, err
	}
	for _, d := range drafts {
		itemID, err := randomID()
		if err != nil {
			return DeliveryReview{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO delivery_items(id,review_id,pack_id,opportunity_id,opportunity_revision,source_sha256,profile_revision,
		  pack_content_sha256,route_id,route_revision,route_sha256,title,company_name,route_excerpt,recipient,sender,subject,body,attachment_sha256,
		  mime_sha256,mime_bytes,message_id,state,created_at,updated_at)
		  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?)`, itemID, id, d.PackID, d.OpportunityID,
			d.OpportunityRevision, d.SourceSHA256, d.ProfileRevision, d.PackContentSHA256, d.RouteID, d.RouteRevision, d.RouteSHA256,
			d.Title, d.CompanyName, d.RouteExcerpt, d.Recipient, d.Sender, d.Subject, d.Body, d.AttachmentSHA256, d.MIMESHA256, d.MIMEBytes, d.MessageID, utcNow(), utcNow())
		if err != nil {
			return DeliveryReview{}, err
		}
	}
	if err := writeRoundAudit(ctx, tx, actor, "delivery.review.prepare", id); err != nil {
		return DeliveryReview{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReview{}, err
	}
	return s.DeliveryReview(ctx, id)
}

func scanDeliveryItem(row rowScanner) (DeliveryItem, error) {
	var d DeliveryItem
	var roundID, attemptID sql.NullString
	err := row.Scan(&d.ID, &d.ReviewID, &d.PackID, &d.OpportunityID, &d.OpportunityRevision, &d.SourceSHA256, &d.ProfileRevision,
		&d.PackContentSHA256, &d.RouteID, &d.RouteRevision, &d.RouteSHA256, &d.Title, &d.CompanyName, &d.RouteExcerpt, &d.Recipient, &d.Sender, &d.Subject,
		&d.Body, &d.AttachmentSHA256, &d.MIMESHA256, &d.MIMEBytes, &d.MessageID, &d.State, &roundID, &attemptID,
		&d.SMTPStage, &d.SMTPCode, &d.OutcomeDetail)
	d.RoundID, d.AttemptID = roundID.String, attemptID.String
	return d, err
}

const deliveryItemColumns = `id,review_id,pack_id,opportunity_id,opportunity_revision,source_sha256,profile_revision,pack_content_sha256,
 route_id,route_revision,route_sha256,title,company_name,route_excerpt,recipient,sender,subject,body,attachment_sha256,mime_sha256,mime_bytes,
 message_id,state,round_id,attempt_id,smtp_stage,smtp_code,outcome_detail`

func (s *Store) DeliveryReview(ctx context.Context, id string) (DeliveryReview, error) {
	var v DeliveryReview
	err := s.db.QueryRowContext(ctx, `SELECT id,material_sha256,COALESCE(approved_sha256,''),COALESCE(approved_at,'') FROM delivery_reviews WHERE id=?`, id).
		Scan(&v.ID, &v.MaterialSHA256, &v.ApprovedSHA256, &v.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryReview{}, ErrNotFound
	}
	if err != nil {
		return DeliveryReview{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryItemColumns+` FROM delivery_items WHERE review_id=? ORDER BY id`, id)
	if err != nil {
		return DeliveryReview{}, err
	}
	defer rows.Close()
	v.Items = []DeliveryItem{}
	for rows.Next() {
		d, err := scanDeliveryItem(rows)
		if err != nil {
			return DeliveryReview{}, err
		}
		v.Items = append(v.Items, d)
	}
	if err := rows.Err(); err != nil {
		return DeliveryReview{}, err
	}
	if err := rows.Close(); err != nil {
		return DeliveryReview{}, err
	}
	// This read is advisory to the owner. ClaimDeliveryItem repeats the same
	// check under a writer transaction immediately before dispatch.
	for i := range v.Items {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return DeliveryReview{}, err
		}
		item := &v.Items[i]
		checkErr := checkDeliveryCurrentTx(ctx, tx, item.PackID, item.OpportunityID, item.OpportunityRevision, item.ProfileRevision,
			item.SourceSHA256, item.PackContentSHA256, item.AttachmentSHA256, item.RouteID, item.RouteSHA256, item.Recipient)
		_ = tx.Rollback()
		item.Current = checkErr == nil
		if checkErr != nil {
			item.BlockingReason = "saved_pack_source_profile_or_route_changed"
		}
	}
	return v, nil
}

func (s *Store) ApproveDeliveryReview(ctx context.Context, actor Actor, id, exactDigest string) (DeliveryReview, error) {
	if !ownerRoundActor(actor) || len(exactDigest) != 64 {
		return DeliveryReview{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryReview{}, err
	}
	defer tx.Rollback()
	var current, approved, owner string
	err = tx.QueryRowContext(ctx, `SELECT material_sha256,COALESCE(approved_sha256,''),owner_id FROM delivery_reviews WHERE id=?`, id).Scan(&current, &approved, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryReview{}, ErrNotFound
	}
	if err != nil {
		return DeliveryReview{}, err
	}
	if owner != actor.ID || exactDigest != current || approved != "" && approved != exactDigest {
		return DeliveryReview{}, ErrConflict
	}
	items, err := deliveryItemsTx(ctx, tx, id)
	if err != nil {
		return DeliveryReview{}, err
	}
	if err := checkDeliveryItemsCurrentTx(ctx, tx, items); err != nil {
		return DeliveryReview{}, err
	}
	if approved == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_reviews SET approved_sha256=?,approved_at=? WHERE id=? AND approved_sha256 IS NULL`, exactDigest, utcNow(), id); err != nil {
			return DeliveryReview{}, err
		}
		if err := writeRoundAudit(ctx, tx, actor, "delivery.review.approve", id); err != nil {
			return DeliveryReview{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReview{}, err
	}
	return s.DeliveryReview(ctx, id)
}

func deliveryItemsTx(ctx context.Context, tx *sql.Tx, reviewID string) ([]DeliveryItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+deliveryItemColumns+` FROM delivery_items WHERE review_id=? ORDER BY id`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []DeliveryItem
	for rows.Next() {
		item, err := scanDeliveryItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func checkDeliveryItemsCurrentTx(ctx context.Context, tx *sql.Tx, items []DeliveryItem) error {
	if len(items) < 1 || len(items) > 3 {
		return ErrInvalid
	}
	for _, item := range items {
		if item.State != "prepared" {
			return ErrFenced
		}
		h := sha256.Sum256(item.MIMEBytes)
		if hex.EncodeToString(h[:]) != item.MIMESHA256 {
			return ErrInvalid
		}
		if err := checkDeliveryCurrentTx(ctx, tx, item.PackID, item.OpportunityID, item.OpportunityRevision, item.ProfileRevision,
			item.SourceSHA256, item.PackContentSHA256, item.AttachmentSHA256, item.RouteID, item.RouteSHA256, item.Recipient); err != nil {
			return err
		}
	}
	return nil
}

func deliveryRouteHash(kind, destination, sourceKind, sourceRef, sourceExcerpt string) string {
	b, _ := json.Marshal([]string{kind, destination, sourceKind, sourceRef, sourceExcerpt})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func DeliveryRouteHash(route OpportunityRoute) string {
	return deliveryRouteHash(route.Kind, route.DestinationText, route.SourceKind, route.SourceRef, route.SourceExcerpt)
}

func deliveryPackRoleMatches(manifest []byte, opportunityID string, opportunityRevision, profileRevision int64,
	title, companyName, sourceURL, originalText string) bool {
	var snapshot struct {
		Role struct {
			OpportunityID       string `json:"opportunityId"`
			OpportunityRevision int64  `json:"opportunityRevision"`
			ProfileRevision     int64  `json:"profileRevision"`
			Title               string `json:"title"`
			Company             string `json:"company"`
			SourceURL           string `json:"sourceUrl"`
			Description         string `json:"description"`
		} `json:"role"`
	}
	return json.Unmarshal(manifest, &snapshot) == nil && snapshot.Role.OpportunityID == opportunityID &&
		snapshot.Role.OpportunityRevision == opportunityRevision && snapshot.Role.ProfileRevision == profileRevision &&
		snapshot.Role.Title == title && snapshot.Role.Company == companyName && snapshot.Role.SourceURL == sourceURL &&
		snapshot.Role.Description == originalText
}

// CurrentDeliveryPackRole permits cosmetic opportunity revisions while
// rejecting a pack prepared for different role or employer material.
func (s *Store) CurrentDeliveryPackRole(ctx context.Context, packID string) error {
	pack, err := s.ApplicationPack(ctx, packID)
	if err != nil {
		return err
	}
	var title, companyName, sourceURL, originalText, archived string
	err = s.db.QueryRowContext(ctx, `SELECT o.title,c.name,o.source_url,o.original_text,COALESCE(o.archived_at,'')
	 FROM opportunities o JOIN companies c ON c.id=o.company_id WHERE o.id=?`, pack.OpportunityID).
		Scan(&title, &companyName, &sourceURL, &originalText, &archived)
	if err != nil {
		return err
	}
	if archived != "" || !deliveryPackRoleMatches(pack.ManifestJSON, pack.OpportunityID, pack.OpportunityRevision,
		pack.ProfileRevision, title, companyName, sourceURL, originalText) {
		return ErrConflict
	}
	return nil
}

func checkDeliveryCurrentTx(ctx context.Context, tx *sql.Tx, packID, opportunityID string, opportunityRevision, profileRevision int64,
	sourceHash, packHash, attachmentHash, routeID, routeHash, recipient string) error {
	var packOpportunityRevision, currentOpportunity, currentProfile, packVersion, latestVersion, currentRoute int64
	var packProfileRevision int64
	var currentPackHash, currentRouteHash, destination, kind, sourceKind, sourceRef, excerpt, originalText, title, sourceURL, companyName, archived string
	var manifest, source, pdf []byte
	err := tx.QueryRowContext(ctx, `SELECT p.opportunity_revision,p.profile_revision,p.content_sha256,p.version,
	 (SELECT max(version) FROM application_packs WHERE opportunity_id=p.opportunity_id),o.revision,o.original_text,o.title,o.source_url,c.name,COALESCE(o.archived_at,''),p.manifest_json,p.typst_source,p.pdf
	 FROM application_packs p JOIN opportunities o ON o.id=p.opportunity_id JOIN companies c ON c.id=o.company_id
	 WHERE p.id=? AND p.opportunity_id=?`, packID, opportunityID).
		Scan(&packOpportunityRevision, &packProfileRevision, &currentPackHash, &packVersion, &latestVersion, &currentOpportunity, &originalText, &title, &sourceURL, &companyName, &archived, &manifest, &source, &pdf)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return err
	}
	if !deliveryPackRoleMatches(manifest, opportunityID, packOpportunityRevision, packProfileRevision,
		title, companyName, sourceURL, originalText) {
		return ErrConflict
	}
	_ = currentOpportunity // A cosmetic notes/audit revision does not change approval.
	if archived != "" || packOpportunityRevision != opportunityRevision || DeliverySourceHash(title, sourceURL, originalText) != sourceHash ||
		currentProfile != profileRevision || packProfileRevision != profileRevision || currentPackHash != packHash ||
		applicationPackContentHash(manifest, source, pdf) != packHash || packVersion != latestVersion {
		return ErrConflict
	}
	pdfDigest := sha256.Sum256(pdf)
	if hex.EncodeToString(pdfDigest[:]) != attachmentHash {
		return ErrConflict
	}
	err = tx.QueryRowContext(ctx, `SELECT revision,kind,destination_text,source_kind,COALESCE(source_ref,''),source_excerpt FROM opportunity_routes WHERE id=? AND opportunity_id=?`, routeID, opportunityID).
		Scan(&currentRoute, &kind, &destination, &sourceKind, &sourceRef, &excerpt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	currentRouteHash = deliveryRouteHash(kind, destination, sourceKind, sourceRef, excerpt)
	_ = currentRoute // Route timestamps and revision alone do not change approval.
	if currentRouteHash != routeHash || destination != recipient ||
		!DeliveryRouteCandidate(OpportunityRoute{OpportunityRouteInput: OpportunityRouteInput{
			Kind: kind, DestinationText: destination, SourceExcerpt: excerpt}}, originalText) {
		return ErrConflict
	}
	return currentApplicationJudgmentTx(ctx, tx, routeID, opportunityID, sourceHash, routeHash)
}

func DeliverySourceHash(title, sourceURL, originalText string) string {
	b, _ := json.Marshal([]string{title, sourceURL, originalText})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
