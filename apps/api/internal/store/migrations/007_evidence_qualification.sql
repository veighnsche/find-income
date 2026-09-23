-- T10: preserve existing evidence/evaluations; legacy rows without the new
-- provenance tuple remain historical and cannot qualify a current record.
CREATE TABLE qualification_input_versions (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  material_version INTEGER NOT NULL CHECK (material_version > 0),
  evidence_version INTEGER NOT NULL CHECK (evidence_version >= 0),
  context_version INTEGER NOT NULL CHECK (context_version > 0)
);
INSERT INTO qualification_input_versions(opportunity_id,material_version,evidence_version,context_version)
SELECT id,1,0,1 FROM opportunities;

CREATE TABLE qualification_refresh_queue (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  requested_at TEXT NOT NULL,
  reason TEXT NOT NULL
);
INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
SELECT id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'migration' FROM opportunities;

CREATE TABLE evidence_sources (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  company_id TEXT NOT NULL REFERENCES companies(id),
  opportunity_kind TEXT NOT NULL CHECK (opportunity_kind IN ('employment','project')),
  context_version INTEGER NOT NULL CHECK (context_version > 0),
  source_kind TEXT NOT NULL CHECK (source_kind IN
    ('vacancy_snapshot','employer_statement','recruiter_statement','owner_observation')),
  record_change_audit_id TEXT REFERENCES record_changes(audit_id),
  source_url TEXT,
  original_text TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  speaker_name TEXT,
  speaker_role TEXT,
  speaker_organisation TEXT,
  channel TEXT,
  occurred_at TEXT,
  owner_preferences_version INTEGER REFERENCES preferences_versions(version),
  recorded_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  CHECK ((source_kind='vacancy_snapshot' AND record_change_audit_id IS NOT NULL)
    OR (source_kind<>'vacancy_snapshot' AND record_change_audit_id IS NULL)),
  CHECK (source_kind NOT IN ('employer_statement','recruiter_statement') OR
    (length(trim(COALESCE(speaker_name,'')))>0 AND
     length(trim(COALESCE(speaker_role,'')))>0 AND
     length(trim(COALESCE(speaker_organisation,'')))>0 AND
     length(trim(COALESCE(channel,'')))>0 AND
     length(trim(original_text))>0 AND occurred_at IS NOT NULL)),
  CHECK (source_kind<>'owner_observation' OR actor_kind='administrator')
);
CREATE INDEX evidence_sources_opportunity_idx ON evidence_sources(opportunity_id,recorded_at,id);
CREATE TRIGGER evidence_sources_no_update BEFORE UPDATE ON evidence_sources
BEGIN SELECT RAISE(ABORT,'evidence sources are immutable'); END;
CREATE TRIGGER evidence_sources_no_delete BEFORE DELETE ON evidence_sources
BEGIN SELECT RAISE(ABORT,'evidence sources are immutable'); END;

ALTER TABLE evidence ADD COLUMN source_id TEXT REFERENCES evidence_sources(id);
ALTER TABLE evidence ADD COLUMN finding TEXT;
ALTER TABLE evidence ADD COLUMN span_start INTEGER;
ALTER TABLE evidence ADD COLUMN span_end INTEGER;
ALTER TABLE evidence ADD COLUMN excerpt_sha256 TEXT;
ALTER TABLE evidence ADD COLUMN hours_min INTEGER;
ALTER TABLE evidence ADD COLUMN hours_max INTEGER;
ALTER TABLE evidence ADD COLUMN hours_hard INTEGER;
ALTER TABLE evidence ADD COLUMN arrangement_pattern TEXT;
ALTER TABLE evidence ADD COLUMN arrangement_location TEXT;
ALTER TABLE evidence ADD COLUMN arrangement_remote_geography TEXT;
ALTER TABLE evidence ADD COLUMN arrangement_onsite_days INTEGER;
ALTER TABLE evidence ADD COLUMN owner_arrangement_evidence_id TEXT REFERENCES evidence(id);
ALTER TABLE evidence ADD COLUMN owner_preferences_version INTEGER REFERENCES preferences_versions(version);
ALTER TABLE evidence ADD COLUMN salary_currency TEXT;
ALTER TABLE evidence ADD COLUMN salary_period TEXT;
ALTER TABLE evidence ADD COLUMN salary_basis TEXT;
ALTER TABLE evidence ADD COLUMN salary_amount_cents INTEGER;
ALTER TABLE evidence ADD COLUMN salary_weekly_hours INTEGER;
-- Old rows can already have several successors. Constrain only the new,
-- source-backed branch; migration must never fail or rewrite old history.
CREATE UNIQUE INDEX evidence_new_successor_unique ON evidence(supersedes_id)
WHERE supersedes_id IS NOT NULL AND source_id IS NOT NULL;
CREATE INDEX evidence_source_idx ON evidence(source_id);
CREATE TRIGGER evidence_no_update BEFORE UPDATE ON evidence
BEGIN SELECT RAISE(ABORT,'evidence is immutable'); END;
CREATE TRIGGER evidence_no_delete BEFORE DELETE ON evidence
BEGIN SELECT RAISE(ABORT,'evidence is immutable'); END;
CREATE TRIGGER evidence_source_guard BEFORE INSERT ON evidence
WHEN NEW.source_id IS NOT NULL
BEGIN
  SELECT CASE WHEN NEW.finding NOT IN
    ('explicit_match','explicit_mismatch','mention_only','ambiguous') OR
    NEW.span_start IS NULL OR NEW.span_end IS NULL OR
    NEW.span_start<0 OR NEW.span_end<=NEW.span_start OR
    NEW.span_end-NEW.span_start>2000 OR
    length(COALESCE(NEW.source_excerpt,''))=0 OR NEW.excerpt_sha256 IS NULL
    THEN RAISE(ABORT,'source-backed evidence requires exact excerpt') END;
  SELECT CASE WHEN NOT EXISTS (
    SELECT 1 FROM evidence_sources s
    JOIN qualification_input_versions v ON v.opportunity_id=s.opportunity_id
    WHERE s.id=NEW.source_id AND s.opportunity_id=NEW.opportunity_id
      AND s.context_version=v.context_version
  ) THEN RAISE(ABORT,'source context mismatch') END;
  SELECT CASE WHEN NEW.supersedes_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM evidence e WHERE e.id=NEW.supersedes_id
      AND e.opportunity_id=NEW.opportunity_id AND e.criterion=NEW.criterion
  ) THEN RAISE(ABORT,'supersession mismatch') END;
END;

ALTER TABLE qualification_evaluations ADD COLUMN material_version INTEGER;
ALTER TABLE qualification_evaluations ADD COLUMN evidence_version INTEGER;
ALTER TABLE qualification_evaluations ADD COLUMN context_version INTEGER;
ALTER TABLE qualification_evaluations ADD COLUMN rules_version TEXT;
ALTER TABLE qualification_evaluations ADD COLUMN source_claims_json TEXT;
ALTER TABLE qualification_evaluations ADD COLUMN salary_json TEXT;
ALTER TABLE qualification_evaluations ADD COLUMN actor_kind TEXT;
ALTER TABLE qualification_evaluations ADD COLUMN actor_id TEXT;
ALTER TABLE qualification_evaluations ADD COLUMN cause_evidence_id TEXT REFERENCES evidence(id);
CREATE INDEX qualification_tuple_idx ON qualification_evaluations
  (opportunity_id,material_version,evidence_version,preferences_version,created_at);
CREATE TRIGGER qualification_evaluations_no_update BEFORE UPDATE ON qualification_evaluations
BEGIN SELECT RAISE(ABORT,'qualification evaluations are immutable'); END;
CREATE TRIGGER qualification_evaluations_no_delete BEFORE DELETE ON qualification_evaluations
BEGIN SELECT RAISE(ABORT,'qualification evaluations are immutable'); END;

CREATE TABLE qualification_current (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  evaluation_id TEXT NOT NULL REFERENCES qualification_evaluations(id)
);
CREATE TRIGGER qualification_current_match_insert BEFORE INSERT ON qualification_current
WHEN NOT EXISTS (SELECT 1 FROM qualification_evaluations e
  WHERE e.id=NEW.evaluation_id AND e.opportunity_id=NEW.opportunity_id)
BEGIN SELECT RAISE(ABORT,'qualification pointer opportunity mismatch'); END;
CREATE TRIGGER qualification_current_match_update BEFORE UPDATE ON qualification_current
WHEN NOT EXISTS (SELECT 1 FROM qualification_evaluations e
  WHERE e.id=NEW.evaluation_id AND e.opportunity_id=NEW.opportunity_id)
BEGIN SELECT RAISE(ABORT,'qualification pointer opportunity mismatch'); END;

CREATE TRIGGER qualification_opportunity_insert AFTER INSERT ON opportunities
BEGIN
  INSERT INTO qualification_input_versions(opportunity_id,material_version,evidence_version,context_version)
  VALUES (NEW.id,1,0,1);
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'opportunity.create');
END;
CREATE TRIGGER qualification_opportunity_material AFTER UPDATE ON opportunities
WHEN OLD.company_id IS NOT NEW.company_id OR OLD.kind IS NOT NEW.kind OR
  OLD.source_url IS NOT NEW.source_url OR OLD.original_text IS NOT NEW.original_text OR
  OLD.work_pattern IS NOT NEW.work_pattern OR OLD.location_text IS NOT NEW.location_text
BEGIN
  UPDATE qualification_input_versions SET material_version=material_version+1,
    context_version=context_version+CASE WHEN OLD.company_id IS NOT NEW.company_id OR
      OLD.kind IS NOT NEW.kind THEN 1 ELSE 0 END WHERE opportunity_id=NEW.id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'opportunity.material')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
CREATE TRIGGER qualification_compensation_insert AFTER INSERT ON compensation
BEGIN
  UPDATE qualification_input_versions SET material_version=material_version+1
    WHERE opportunity_id=NEW.opportunity_id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.opportunity_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'compensation.insert')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
CREATE TRIGGER qualification_compensation_update AFTER UPDATE ON compensation
WHEN OLD.currency IS NOT NEW.currency OR OLD.min_amount_cents IS NOT NEW.min_amount_cents OR
  OLD.max_amount_cents IS NOT NEW.max_amount_cents OR OLD.period IS NOT NEW.period OR
  OLD.reference_hours IS NOT NEW.reference_hours OR OLD.basis IS NOT NEW.basis
BEGIN
  UPDATE qualification_input_versions SET material_version=material_version+1
    WHERE opportunity_id=NEW.opportunity_id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.opportunity_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'compensation.update')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
CREATE TRIGGER qualification_source_insert AFTER INSERT ON evidence_sources
BEGIN
  UPDATE qualification_input_versions SET evidence_version=evidence_version+1
    WHERE opportunity_id=NEW.opportunity_id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.opportunity_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'evidence.source')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
CREATE TRIGGER qualification_evidence_insert AFTER INSERT ON evidence
WHEN NEW.source_id IS NOT NULL
BEGIN
  UPDATE qualification_input_versions SET evidence_version=evidence_version+1
    WHERE opportunity_id=NEW.opportunity_id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.opportunity_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'evidence.claim')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
CREATE TRIGGER qualification_preferences_update AFTER UPDATE OF version ON preferences_current
WHEN OLD.version IS NOT NEW.version
BEGIN
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  SELECT id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'preferences.update' FROM opportunities
  WHERE archived_at IS NULL
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;
