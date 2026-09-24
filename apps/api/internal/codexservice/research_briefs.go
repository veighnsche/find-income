// Research brief readers: the current owner brief (commission-time rubric
// resolution, T13 gap) and run-scoped brief versions (assessment binding).
//
// Owner: lane B (runtime), T17. No central rubric table exists and the
// schema is frozen, so the current brief store is the versioned owner
// profile itself: the rubric version identifies exactly which owner brief
// a run was commissioned against, and any criteria edit yields a new
// rubric version (v1 assessments never rebound to v2, T04 B01).
package codexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// BriefFact is one owner brief fact or preference entry.
type BriefFact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OwnerBrief is the current owner brief: profile version, derived rubric
// version, the source the rubric resolved from, and servable facts.
type OwnerBrief struct {
	ProfileVersion int64       `json:"profileVersion"`
	RubricVersion  string      `json:"rubricVersion"`
	Source         string      `json:"source"`
	Facts          []BriefFact `json:"facts"`
	Preferences    []BriefFact `json:"preferences"`
}

// OwnerBriefReader serves the current owner brief. The production reader
// is CurrentOwnerBrief; protocol tests use doubles.
type OwnerBriefReader interface {
	OwnerBrief(ctx context.Context) (OwnerBrief, error)
}

// OwnerBriefFunc adapts a function to OwnerBriefReader.
type OwnerBriefFunc func(ctx context.Context) (OwnerBrief, error)

// OwnerBrief implements OwnerBriefReader.
func (f OwnerBriefFunc) OwnerBrief(ctx context.Context) (OwnerBrief, error) { return f(ctx) }

// CurrentOwnerBrief resolves the current owner brief from the brief store
// (the versioned owner profile). The rubric version is
// "criteria-v<profile>-<12hex criteria digest>": stable for an unchanged
// brief, distinct after any criteria edit. Source records the exact
// profile version; commissioning journals it on the commission record.
func CurrentOwnerBrief(ctx context.Context, db *store.Store) (OwnerBrief, error) {
	if db == nil {
		return OwnerBrief{}, errors.New("codexservice: brief store required")
	}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OwnerBrief{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"brief", "no current owner brief; save owner preferences before commissioning")
		}
		return OwnerBrief{}, err
	}
	if prefs.Version < 1 {
		return OwnerBrief{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"brief", "current owner brief has no usable version")
	}
	raw, err := json.Marshal(prefs.RoleCriteria)
	if err != nil {
		return OwnerBrief{}, err
	}
	sum := sha256.Sum256(raw)
	out := OwnerBrief{
		ProfileVersion: prefs.Version,
		RubricVersion:  fmt.Sprintf("criteria-v%d-%s", prefs.Version, hex.EncodeToString(sum[:])[:12]),
		Source:         fmt.Sprintf("preferences_versions:current:v%d", prefs.Version),
		Facts:          briefFacts(prefs),
		Preferences:    briefPreferences(prefs),
	}
	return out, nil
}

func briefFacts(prefs store.Preferences) []BriefFact {
	facts := []BriefFact{}
	if strings.TrimSpace(prefs.PreferredLocation) != "" {
		facts = append(facts, BriefFact{Key: "preferredLocation", Value: prefs.PreferredLocation})
	}
	facts = append(facts,
		BriefFact{Key: "allowRemote", Value: fmt.Sprintf("%v", prefs.AllowRemote)},
		BriefFact{Key: "allowHybrid", Value: fmt.Sprintf("%v", prefs.AllowHybrid)})
	if prefs.TargetHoursHundredths > 0 {
		facts = append(facts, BriefFact{Key: "targetHours",
			Value: fmt.Sprintf("%.2f", float64(prefs.TargetHoursHundredths)/100)})
	}
	if prefs.MinMonthlyBaseCents > 0 {
		facts = append(facts, BriefFact{Key: "minMonthlyBase",
			Value: fmt.Sprintf("%d %s", prefs.MinMonthlyBaseCents, prefs.SalaryCurrency)})
	}
	if strings.TrimSpace(prefs.Timezone) != "" {
		facts = append(facts, BriefFact{Key: "timezone", Value: prefs.Timezone})
	}
	return facts
}

func briefPreferences(prefs store.Preferences) []BriefFact {
	out := []BriefFact{}
	for _, c := range prefs.RoleCriteria {
		value := c.Mode + ": " + c.Label
		if strings.TrimSpace(c.Description) != "" {
			value += " — " + c.Description
		}
		out = append(out, BriefFact{Key: c.ID, Value: value})
	}
	return out
}

// RunBriefs serves run-scoped brief versions from the supervisor
// checkpoint: the binding pair the run was commissioned against. It
// satisfies the jevassess, identity and recordsave Briefs interfaces, so
// T23 wires the one implementation behind all three assessment/save paths.
type RunBriefs struct {
	DB *store.Store
}

// CurrentBrief implements Briefs.
func (b RunBriefs) CurrentBrief(ctx context.Context, runID string) (int64, string, error) {
	if b.DB == nil || strings.TrimSpace(runID) == "" {
		return 0, "", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"runId", "run brief requires a store and run id")
	}
	var cp researchcontract.Checkpoint
	if err := b.DB.Read(ctx, func(r store.Reader) error {
		var err error
		cp, err = store.GetRunCheckpoint(ctx, r, runID)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, "", researchcontract.NewError(researchcontract.OutcomeNotFound,
				"brief", "no checkpoint stored for run "+runID)
		}
		return 0, "", err
	}
	return cp.ProfileVersion, cp.RubricVersion, nil
}

// RecordReader serves company/opportunity subjects for context_read
// composition. Summaries are redacted to identifying fields plus revision.
type RecordReader interface {
	CompanySummary(ctx context.Context, id string) (map[string]any, error)
	OpportunitySummary(ctx context.Context, id string) (map[string]any, error)
}

// StoreRecordReader is the production RecordReader over the store.
type StoreRecordReader struct {
	DB *store.Store
}

// CompanySummary implements RecordReader.
func (r StoreRecordReader) CompanySummary(ctx context.Context, id string) (map[string]any, error) {
	if r.DB == nil || strings.TrimSpace(id) == "" {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"subject", "company subject requires a store and id")
	}
	company, err := r.DB.Company(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"subject", "unknown company "+id)
		}
		return nil, err
	}
	out := map[string]any{"kind": "company", "id": company.ID,
		"name": company.Name, "revision": company.Revision}
	if company.Website != "" {
		out["website"] = company.Website
	}
	if company.ArchivedAt != "" {
		out["archivedAt"] = company.ArchivedAt
	}
	return out, nil
}

// OpportunitySummary implements RecordReader.
func (r StoreRecordReader) OpportunitySummary(ctx context.Context, id string) (map[string]any, error) {
	if r.DB == nil || strings.TrimSpace(id) == "" {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"subject", "opportunity subject requires a store and id")
	}
	opp, err := r.DB.Opportunity(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"subject", "unknown opportunity "+id)
		}
		return nil, err
	}
	out := map[string]any{"kind": "opportunity", "id": opp.ID,
		"companyId": opp.CompanyID, "title": opp.Title, "stage": opp.Stage,
		"revision": opp.Revision}
	if opp.WorkPattern != "" {
		out["workPattern"] = opp.WorkPattern
	}
	if opp.LocationText != "" {
		out["locationText"] = opp.LocationText
	}
	if opp.SourceURL != "" {
		out["sourceUrl"] = opp.SourceURL
	}
	if opp.ArchivedAt != "" {
		out["archivedAt"] = opp.ArchivedAt
	}
	return out, nil
}
