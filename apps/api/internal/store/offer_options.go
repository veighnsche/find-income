package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type OfferOption struct {
	ID    string
	Label string
}

type OfferOptionSet struct {
	ID             string
	OpportunityID  string
	SourceID       string
	ContextVersion int64
	SpanStart      int
	SpanEnd        int
	SourceExcerpt  string
	ExcerptSHA256  string
	SupersedesID   string
	CreatedAt      string
	Actor          Actor
	Options        []OfferOption
}

type OfferOptionSetInput struct {
	OpportunityID           string
	SourceID                string
	ExpectedContextVersion  int64
	ExpectedEvidenceVersion int64
	SpanStart               int
	SpanEnd                 int
	Labels                  []string // two to eight explicitly offered alternatives
	SupersedesID            string
}

func (s *Store) CreateOfferOptionSet(ctx context.Context, actor Actor, input OfferOptionSetInput) (OfferOptionSet, string, error) {
	if input.OpportunityID == "" || input.SourceID == "" || input.ExpectedContextVersion < 1 ||
		input.ExpectedEvidenceVersion < 0 || len(input.Labels) < 2 || len(input.Labels) > 8 ||
		input.SpanStart < 0 || input.SpanEnd <= input.SpanStart || input.SpanEnd-input.SpanStart > 2000 {
		return OfferOptionSet{}, "", fmt.Errorf("%w: bounded sourced alternatives required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, label := range input.Labels {
		folded := strings.ToLower(label)
		if !boundedNonempty(label, 100) || strings.TrimSpace(label) != label || seen[folded] {
			return OfferOptionSet{}, "", fmt.Errorf("%w: distinct option labels required", ErrInvalid)
		}
		seen[folded] = true
	}
	id, err := randomID()
	if err != nil {
		return OfferOptionSet{}, "", err
	}
	set := OfferOptionSet{ID: id, OpportunityID: input.OpportunityID, SourceID: input.SourceID,
		ContextVersion: input.ExpectedContextVersion, SpanStart: input.SpanStart, SpanEnd: input.SpanEnd,
		SupersedesID: input.SupersedesID, CreatedAt: utcNow(), Actor: actor}
	for _, label := range input.Labels {
		optionID, err := randomID()
		if err != nil {
			return OfferOptionSet{}, "", err
		}
		set.Options = append(set.Options, OfferOption{ID: optionID, Label: label})
	}
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		if err := lockQualificationInput(ctx, tx, input.OpportunityID); err != nil {
			return Change{}, err
		}
		var evidenceVersion, contextVersion int64
		var companyID, kind, currentURL, currentText string
		err := tx.QueryRowContext(ctx, `SELECT v.evidence_version,v.context_version,o.company_id,o.kind,
  COALESCE(o.source_url,''),o.original_text FROM qualification_input_versions v
  JOIN opportunities o ON o.id=v.opportunity_id WHERE o.id=? AND o.archived_at IS NULL`,
			input.OpportunityID).Scan(&evidenceVersion, &contextVersion, &companyID, &kind, &currentURL, &currentText)
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, err
		}
		if evidenceVersion != input.ExpectedEvidenceVersion || contextVersion != input.ExpectedContextVersion {
			return Change{}, ErrConflict
		}
		source, err := scanEvidenceSource(tx.QueryRowContext(ctx,
			`SELECT `+evidenceSourceColumns+` FROM evidence_sources WHERE id=?`, input.SourceID))
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrInvalid
		}
		if err != nil {
			return Change{}, err
		}
		if source.OpportunityID != input.OpportunityID || source.ContextVersion != contextVersion ||
			source.CompanyID != companyID || source.OpportunityKind != kind ||
			source.SourceKind == OwnerObservation {
			return Change{}, ErrConflict
		}
		if source.SourceKind == VacancySnapshot &&
			(source.SourceURL != currentURL || source.ContentSHA256 != sourceDigest(currentText)) {
			return Change{}, ErrConflict
		}
		set.SourceExcerpt, err = exactExcerpt(source.OriginalText, input.SpanStart, input.SpanEnd)
		if err != nil {
			return Change{}, err
		}
		for _, label := range input.Labels {
			if !strings.Contains(strings.ToLower(set.SourceExcerpt), strings.ToLower(label)) {
				return Change{}, fmt.Errorf("%w: each option label must appear in the declaration", ErrInvalid)
			}
		}
		set.ExcerptSHA256 = sourceDigest(set.SourceExcerpt)
		if input.SupersedesID != "" {
			var oldOpportunity string
			var oldContext int64
			err := tx.QueryRowContext(ctx, `SELECT opportunity_id,context_version FROM offer_option_sets
  WHERE id=? AND NOT EXISTS(SELECT 1 FROM offer_option_sets child WHERE child.supersedes_id=offer_option_sets.id)`,
				input.SupersedesID).Scan(&oldOpportunity, &oldContext)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrConflict
			}
			if err != nil {
				return Change{}, err
			}
			if oldOpportunity != input.OpportunityID || oldContext != contextVersion {
				return Change{}, ErrConflict
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO offer_option_sets
  (id,opportunity_id,source_id,context_version,span_start,span_end,source_excerpt,
   excerpt_sha256,supersedes_id,created_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, set.ID, set.OpportunityID, set.SourceID, set.ContextVersion,
			set.SpanStart, set.SpanEnd, set.SourceExcerpt, set.ExcerptSHA256, optionalText(set.SupersedesID),
			set.CreatedAt, actor.Kind, actor.ID)
		if err != nil {
			return Change{}, err
		}
		for _, option := range set.Options {
			if _, err := tx.ExecContext(ctx, `INSERT INTO offer_options(id,set_id,label) VALUES (?,?,?)`,
				option.ID, set.ID, option.Label); err != nil {
				return Change{}, err
			}
		}
		return Change{Operation: "offer_options.create", EntityKind: "offer_option_set", EntityID: set.ID}, nil
	})
	if err != nil {
		return OfferOptionSet{}, "", err
	}
	return set, changeID, nil
}

// CurrentOfferOptionSets returns the active source-backed alternatives for the
// current company/kind context. More than one result is intentionally exposed
// so qualification can mark competing declarations unresolved.
func (s *Store) CurrentOfferOptionSets(ctx context.Context, opportunityID string) ([]OfferOptionSet, error) {
	return currentOfferOptionSets(ctx, s.db, opportunityID)
}

type optionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func currentOfferOptionSets(ctx context.Context, db optionQueryer, opportunityID string) ([]OfferOptionSet, error) {
	if opportunityID == "" {
		return nil, ErrInvalid
	}
	rows, err := db.QueryContext(ctx, `SELECT sets.id,sets.opportunity_id,sets.source_id,sets.context_version,
  sets.span_start,sets.span_end,sets.source_excerpt,sets.excerpt_sha256,sets.supersedes_id,
  sets.created_at,sets.actor_kind,sets.actor_id,
  src.source_kind,COALESCE(src.source_url,''),src.content_sha256,
  COALESCE(o.source_url,''),o.original_text
  FROM offer_option_sets sets JOIN evidence_sources src ON src.id=sets.source_id
  JOIN opportunities o ON o.id=sets.opportunity_id
  JOIN qualification_input_versions v ON v.opportunity_id=o.id
  WHERE o.id=? AND sets.context_version=v.context_version AND src.company_id=o.company_id
    AND src.opportunity_kind=o.kind
    AND NOT EXISTS(SELECT 1 FROM offer_option_sets child WHERE child.supersedes_id=sets.id)
  ORDER BY sets.created_at,sets.id`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sets []OfferOptionSet
	for rows.Next() {
		var set OfferOptionSet
		var old sql.NullString
		var kind EvidenceSourceKind
		var sourceURL, sourceHash, currentURL, currentText string
		if err := rows.Scan(&set.ID, &set.OpportunityID, &set.SourceID, &set.ContextVersion,
			&set.SpanStart, &set.SpanEnd, &set.SourceExcerpt, &set.ExcerptSHA256, &old,
			&set.CreatedAt, &set.Actor.Kind, &set.Actor.ID, &kind, &sourceURL, &sourceHash,
			&currentURL, &currentText); err != nil {
			return nil, err
		}
		if kind == VacancySnapshot && (sourceURL != currentURL || sourceHash != sourceDigest(currentText)) {
			continue
		}
		set.SupersedesID = old.String
		sets = append(sets, set)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range sets {
		options, err := db.QueryContext(ctx, `SELECT id,label FROM offer_options WHERE set_id=? ORDER BY rowid`, sets[i].ID)
		if err != nil {
			return nil, err
		}
		for options.Next() {
			var option OfferOption
			if err := options.Scan(&option.ID, &option.Label); err != nil {
				options.Close()
				return nil, err
			}
			sets[i].Options = append(sets[i].Options, option)
		}
		err = options.Err()
		options.Close()
		if err != nil {
			return nil, err
		}
	}
	return sets, nil
}
