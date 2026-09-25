-- Persisted Jev group classifications with selected personalized reasons
-- (lane C, C3). One immutable row per Jev assessment of collected vacancy
-- evidence: the group, up to three catalog reason selections with raw Jev
-- support, optional conflict/missing singletons, evidence provenance and the
-- pinned brief/rubric/catalog versions. Rows never update: a re-classification
-- (new revision, brief, or evidence) inserts a new row, and reads flag
-- version drift as stale instead of silently reusing mismatched versions.
-- Unknown is reserved for unusable evidence: it carries an unknown_basis and
-- no reason selections. jevSupport travels as the raw recorded signal; it is
-- never a verified-correctness claim.
CREATE TABLE findings (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  run_id TEXT NOT NULL REFERENCES rounds(id),
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision > 0),
  assessment_id TEXT NOT NULL REFERENCES jev_assessments_dynamic(id),
  profile_version INTEGER NOT NULL CHECK (profile_version > 0),
  rubric_version TEXT NOT NULL CHECK (length(trim(rubric_version)) > 0),
  catalog_version TEXT NOT NULL CHECK (catalog_version LIKE 'catalog-v%'),
  candidate_set_hash TEXT NOT NULL CHECK (length(candidate_set_hash) = 64),
  reuse_key TEXT NOT NULL CHECK (length(reuse_key) = 64),
  group_name TEXT NOT NULL CHECK (group_name IN
    ('recommended','could_be_recommended','probably_not_recommended','not_recommended','unknown')),
  unknown_basis TEXT NOT NULL DEFAULT '',
  reasons_json TEXT NOT NULL CHECK (json_valid(reasons_json)),
  conflict_json TEXT CHECK (conflict_json IS NULL OR json_valid(conflict_json)),
  missing_fact_json TEXT CHECK (missing_fact_json IS NULL OR json_valid(missing_fact_json)),
  evidence_links_json TEXT NOT NULL CHECK (json_valid(evidence_links_json)),
  source_id TEXT NOT NULL DEFAULT '',
  source_revision TEXT NOT NULL DEFAULT '',
  observed_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(actor_kind, actor_id, reuse_key),
  CHECK ((group_name = 'unknown') = (length(trim(unknown_basis)) > 0)),
  CHECK ((group_name != 'unknown') OR
    (reasons_json = '[]' AND conflict_json IS NULL AND missing_fact_json IS NULL))
);
CREATE INDEX findings_run_opportunity_idx ON findings(run_id, opportunity_id, created_at, id);
CREATE INDEX findings_opportunity_idx ON findings(opportunity_id, created_at, id);
CREATE INDEX findings_assessment_idx ON findings(assessment_id);
CREATE TRIGGER findings_no_update BEFORE UPDATE ON findings
BEGIN SELECT RAISE(ABORT,'findings are immutable'); END;
CREATE TRIGGER findings_no_delete BEFORE DELETE ON findings
BEGIN SELECT RAISE(ABORT,'findings are immutable'); END;
