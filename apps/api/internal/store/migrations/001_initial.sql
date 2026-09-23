-- Initial schema for the single-user qualification prototype.
-- Fresh databases are seeded with editable preferences by the Go bootstrap.

CREATE TABLE preferences_versions (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  preferred_location TEXT NOT NULL,
  allow_remote INTEGER NOT NULL CHECK (allow_remote IN (0, 1)),
  allow_hybrid INTEGER NOT NULL CHECK (allow_hybrid IN (0, 1)),
  target_hours_hundredths INTEGER NOT NULL CHECK (target_hours_hundredths BETWEEN 100 AND 16800),
  min_monthly_base_cents INTEGER NOT NULL CHECK (min_monthly_base_cents >= 0),
  salary_currency TEXT NOT NULL,
  role_criteria_json TEXT NOT NULL CHECK (json_valid(role_criteria_json)),
  timezone TEXT NOT NULL,
  created_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL
);

CREATE TABLE preferences_current (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  version INTEGER NOT NULL REFERENCES preferences_versions(version)
);

CREATE TABLE companies (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  website TEXT,
  notes TEXT NOT NULL DEFAULT '',
  archived_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE opportunities (
  id TEXT PRIMARY KEY,
  company_id TEXT NOT NULL REFERENCES companies(id),
  title TEXT NOT NULL CHECK (length(trim(title)) > 0),
  kind TEXT NOT NULL CHECK (kind IN ('employment', 'project')),
  source_url TEXT,
  original_text TEXT NOT NULL DEFAULT '',
  stage TEXT NOT NULL,
  work_pattern TEXT NOT NULL DEFAULT 'unknown',
  location_text TEXT NOT NULL DEFAULT '',
  posted_on TEXT,
  deadline_on TEXT,
  archived_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
, notes TEXT NOT NULL DEFAULT '');

CREATE TABLE compensation (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  currency TEXT NOT NULL,
  min_amount_cents INTEGER CHECK (min_amount_cents >= 0),
  max_amount_cents INTEGER CHECK (max_amount_cents >= min_amount_cents),
  period TEXT NOT NULL CHECK (period IN ('month', 'year', 'hour', 'project', 'unknown')),
  reference_hours_hundredths INTEGER CHECK (reference_hours_hundredths BETWEEN 100 AND 16800),
  annual_conversion TEXT CHECK (annual_conversion IN ('twelve_equal_monthly_base_payments')),
  annual_conversion_span_start INTEGER,
  annual_conversion_span_end INTEGER,
  annual_conversion_excerpt TEXT,
  annual_conversion_sha256 TEXT,
  basis TEXT NOT NULL CHECK (basis IN ('base', 'inclusive', 'unknown')),
  benefits_text TEXT NOT NULL DEFAULT '',
  CHECK (max_amount_cents IS NULL OR min_amount_cents IS NOT NULL),
  CHECK (annual_conversion IS NULL OR (period='year' AND basis='base')),
  CHECK ((annual_conversion IS NULL AND annual_conversion_span_start IS NULL AND annual_conversion_span_end IS NULL
    AND annual_conversion_excerpt IS NULL AND annual_conversion_sha256 IS NULL) OR
    (annual_conversion IS NOT NULL AND annual_conversion_span_start>=0 AND
    annual_conversion_span_end>annual_conversion_span_start AND
    length(annual_conversion_excerpt)>0 AND length(annual_conversion_sha256)=64))
);

CREATE TABLE evidence (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  criterion TEXT NOT NULL,
  observed_value TEXT NOT NULL,
  confirmation_state TEXT NOT NULL CHECK (confirmation_state IN ('confirmed', 'unknown', 'conflicting')),
  source_kind TEXT NOT NULL,
  source_url TEXT,
  source_excerpt TEXT NOT NULL,
  source_contact_text TEXT,
  observed_at TEXT NOT NULL,
  supersedes_id TEXT REFERENCES evidence(id),
  created_at TEXT NOT NULL,
  source_id TEXT NOT NULL REFERENCES evidence_sources(id),
  finding TEXT NOT NULL CHECK (finding IN ('explicit_match','explicit_mismatch','mention_only','ambiguous')),
  span_start INTEGER NOT NULL CHECK (span_start >= 0),
  span_end INTEGER NOT NULL CHECK (span_end > span_start),
  excerpt_sha256 TEXT NOT NULL,
  hours_min_hundredths INTEGER CHECK (hours_min_hundredths BETWEEN 100 AND 16800),
  hours_max_hundredths INTEGER CHECK (hours_max_hundredths BETWEEN 100 AND 16800),
  hours_hard INTEGER CHECK (hours_hard IN (0,1)),
  arrangement_pattern TEXT,
  arrangement_location TEXT,
  arrangement_remote_geography TEXT,
  arrangement_onsite_days_hundredths INTEGER CHECK (arrangement_onsite_days_hundredths BETWEEN 0 AND 700),
  owner_arrangement_evidence_id TEXT REFERENCES evidence(id),
  owner_preferences_version INTEGER REFERENCES preferences_versions(version),
  owner_location_fingerprint TEXT,
  salary_currency TEXT,
  salary_period TEXT,
  salary_basis TEXT,
  salary_amount_cents INTEGER,
  salary_weekly_hours_hundredths INTEGER CHECK (salary_weekly_hours_hundredths BETWEEN 100 AND 16800),
  salary_annual_conversion TEXT CHECK (salary_annual_conversion IN ('twelve_equal_monthly_base_payments')),
  role_criterion_id TEXT,
  role_definition_hash TEXT,
  role_definition_json TEXT CHECK (role_definition_json IS NULL OR json_valid(role_definition_json)),
  role_preferences_version INTEGER REFERENCES preferences_versions(version),
  role_presence TEXT CHECK (role_presence IN ('explicit_presence','explicit_absence','mention_only','ambiguous')),
  offer_option_id TEXT REFERENCES offer_options(id),
  CHECK (span_end-span_start <= 2000),
  CHECK (role_criterion_id IS NULL OR (role_definition_hash IS NOT NULL AND role_definition_json IS NOT NULL
    AND role_preferences_version IS NOT NULL AND role_presence IS NOT NULL))
);

CREATE TABLE qualification_evaluations (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision > 0),
  preferences_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  overall_state TEXT NOT NULL CHECK (overall_state IN ('qualified', 'unsuitable', 'unresolved', 'needs_requalification')),
  criterion_results_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  material_version INTEGER NOT NULL CHECK (material_version > 0),
  evidence_version INTEGER NOT NULL CHECK (evidence_version >= 0),
  context_version INTEGER NOT NULL CHECK (context_version > 0),
  rules_version TEXT NOT NULL,
  source_claims_json TEXT NOT NULL CHECK (json_valid(source_claims_json)),
  salary_json TEXT NOT NULL CHECK (json_valid(salary_json)),
  option_results_json TEXT NOT NULL CHECK (json_valid(option_results_json)),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  cause_evidence_id TEXT REFERENCES evidence(id)
);

CREATE TABLE actions (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT REFERENCES opportunities(id),
  description TEXT NOT NULL CHECK (length(trim(description)) > 0),
  due_date TEXT,
  due_at TEXT,
  due_timezone TEXT,
  status TEXT NOT NULL CHECK (status IN ('open', 'completed', 'cancelled')),
  completed_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((due_date IS NOT NULL AND due_at IS NULL AND due_timezone IS NULL)
      OR (due_date IS NULL AND due_at IS NOT NULL AND due_timezone IS NOT NULL))
);

CREATE TABLE audit_changes (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  entity_kind TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  revision_before INTEGER,
  revision_after INTEGER,
  occurred_at TEXT NOT NULL
);

CREATE TABLE administrator (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  password_hash TEXT NOT NULL,
  credential_version INTEGER NOT NULL CHECK (credential_version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,
  administrator_id INTEGER NOT NULL DEFAULT 1 REFERENCES administrator(singleton),
  csrf_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_seen_at TEXT
);

CREATE TABLE agent_credentials (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
  token_hash TEXT NOT NULL UNIQUE,
  scopes_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_used_at TEXT,
  created_by_admin INTEGER NOT NULL DEFAULT 1 REFERENCES administrator(singleton)
);

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (length(trim(kind)) BETWEEN 1 AND 80),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_sha256 TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  request_sha256 TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
  available_at TEXT NOT NULL,
  lease_token TEXT,
  lease_owner TEXT,
  lease_until TEXT,
  result_ref TEXT,
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  last_error_code TEXT,
  last_error_message TEXT,
  cancelled_by_kind TEXT,
  cancelled_by_id TEXT,
  cancelled_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (actor_kind, actor_id, idempotency_key),
  CHECK ((state = 'running' AND lease_token IS NOT NULL AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
      OR (state <> 'running' AND lease_token IS NULL AND lease_owner IS NULL AND lease_until IS NULL)),
  CHECK ((state = 'succeeded' AND completed_at IS NOT NULL)
      OR (state <> 'succeeded' AND result_ref IS NULL AND result_json IS NULL)),
  CHECK ((state = 'cancelled' AND cancelled_at IS NOT NULL AND cancelled_by_kind IS NOT NULL AND cancelled_by_id IS NOT NULL)
      OR (state <> 'cancelled' AND cancelled_at IS NULL AND cancelled_by_kind IS NULL AND cancelled_by_id IS NULL))
);

CREATE TABLE job_attempts (
  job_id TEXT NOT NULL REFERENCES jobs(id),
  attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
  lease_token TEXT NOT NULL UNIQUE,
  worker_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  lease_until TEXT NOT NULL,
  finished_at TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('running', 'succeeded', 'retry', 'failed', 'expired', 'cancelled')),
  error_code TEXT,
  error_message TEXT,
  retry_not_before TEXT,
  result_ref TEXT,
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  PRIMARY KEY (job_id, attempt_no),
  CHECK ((outcome = 'running' AND finished_at IS NULL)
      OR (outcome <> 'running' AND finished_at IS NOT NULL))
);

-- Both scheduled discoveries and owner submissions enter this same durable
-- intake. Jobs provide leases/attempt history; this row preserves source and
-- the exact verified record mapping across retries and process restarts.
CREATE TABLE ingestion_requests (
  id TEXT PRIMARY KEY,
  origin TEXT NOT NULL CHECK (origin IN ('collector','owner','agent')),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  submission_sha256 TEXT NOT NULL,
  source_url TEXT,
  original_text TEXT NOT NULL DEFAULT '',
  connector_id TEXT,
  external_id TEXT,
  discovered_at TEXT,
  status TEXT NOT NULL CHECK (status IN ('pending','processing','completed','needs_text','failed')),
  job_id TEXT NOT NULL REFERENCES jobs(id),
  attempts_started INTEGER NOT NULL DEFAULT 1 CHECK (attempts_started > 0),
  dispatch_started INTEGER NOT NULL DEFAULT 0 CHECK (dispatch_started IN (0,1)),
  codex_thread_id TEXT,
  codex_turn_id TEXT,
  opportunity_id TEXT REFERENCES opportunities(id),
  record_change_id TEXT REFERENCES record_changes(audit_id),
  source_id TEXT REFERENCES evidence_sources(id),
  organisation_job_id TEXT REFERENCES jobs(id),
  safe_error_code TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (actor_kind,actor_id,idempotency_key),
  CHECK (length(original_text) <= 200000),
  CHECK (source_url IS NOT NULL OR length(trim(original_text)) > 0),
  CHECK (status <> 'completed' OR (opportunity_id IS NOT NULL AND record_change_id IS NOT NULL)),
  CHECK (status <> 'needs_text' OR source_url IS NOT NULL)
);

CREATE INDEX ingestion_requests_recent_idx ON ingestion_requests(created_at,id);
CREATE INDEX ingestion_requests_job_idx ON ingestion_requests(job_id);
CREATE INDEX ingestion_requests_opportunity_idx ON ingestion_requests(opportunity_id);

-- Configured public ATS boards are scanned in bounded pages. A cursor and
-- lease make one scan resumable across service restarts and overlapping workers.
CREATE TABLE collector_boards (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  site TEXT NOT NULL,
  region TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  interval_minutes INTEGER NOT NULL CHECK (interval_minutes BETWEEN 15 AND 10080),
  next_scan_at TEXT NOT NULL,
  next_offset INTEGER NOT NULL DEFAULT 0 CHECK (next_offset >= 0),
  lease_token TEXT,
  lease_until TEXT,
  last_run_at TEXT,
  last_success_at TEXT,
  last_error_code TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(provider,site,region),
  CHECK ((lease_token IS NULL AND lease_until IS NULL) OR
    (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX collector_boards_due_idx ON collector_boards(enabled,next_scan_at,lease_until);

CREATE TABLE organisation_category_versions (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  categories_json TEXT NOT NULL CHECK (json_valid(categories_json)),
  created_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL
);

CREATE TABLE organisation_categories_current (
  singleton INTEGER PRIMARY KEY CHECK (singleton=1),
  version INTEGER NOT NULL REFERENCES organisation_category_versions(version)
);

CREATE TABLE organisation_assessments (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  ingestion_id TEXT NOT NULL REFERENCES ingestion_requests(id),
  job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
  category_version INTEGER NOT NULL REFERENCES organisation_category_versions(version),
  source_fingerprint TEXT NOT NULL,
  input_sha256 TEXT NOT NULL,
  disposition TEXT NOT NULL CHECK (disposition IN ('category_selected','uncertain')),
  category_id TEXT,
  requested_model TEXT NOT NULL,
  returned_model TEXT NOT NULL,
  input_json TEXT NOT NULL CHECK (json_valid(input_json)),
  result_json TEXT NOT NULL CHECK (json_valid(result_json)),
  source_refs_json TEXT NOT NULL CHECK (json_valid(source_refs_json)),
  created_at TEXT NOT NULL,
  CHECK ((disposition='uncertain' AND category_id IS NULL) OR
    (disposition='category_selected' AND category_id IS NOT NULL))
);

CREATE TABLE organisation_current (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  assessment_id TEXT NOT NULL REFERENCES organisation_assessments(id)
);

CREATE INDEX organisation_assessments_opportunity_idx ON organisation_assessments(opportunity_id,created_at,id);
CREATE TRIGGER organisation_assessments_no_update BEFORE UPDATE ON organisation_assessments
BEGIN SELECT RAISE(ABORT,'organisation assessments are immutable'); END;
CREATE TRIGGER organisation_assessments_no_delete BEFORE DELETE ON organisation_assessments
BEGIN SELECT RAISE(ABORT,'organisation assessments are immutable'); END;

CREATE TABLE record_changes (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  audit_id TEXT NOT NULL UNIQUE REFERENCES audit_changes(id),
  entity_kind TEXT NOT NULL CHECK (entity_kind IN ('company', 'opportunity')),
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  revision_before INTEGER,
  revision_after INTEGER,
  occurred_at TEXT NOT NULL,
  snapshot_state TEXT NOT NULL CHECK (snapshot_state = 'captured'),
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json))
);

CREATE TABLE qualification_input_versions (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  material_version INTEGER NOT NULL CHECK (material_version > 0),
  evidence_version INTEGER NOT NULL CHECK (evidence_version >= 0),
  context_version INTEGER NOT NULL CHECK (context_version > 0)
);

CREATE TABLE qualification_refresh_queue (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  requested_at TEXT NOT NULL,
  reason TEXT NOT NULL
);

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

CREATE TABLE offer_option_sets (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  source_id TEXT NOT NULL REFERENCES evidence_sources(id),
  context_version INTEGER NOT NULL CHECK (context_version > 0),
  span_start INTEGER NOT NULL CHECK (span_start >= 0),
  span_end INTEGER NOT NULL CHECK (span_end > span_start),
  source_excerpt TEXT NOT NULL,
  excerpt_sha256 TEXT NOT NULL,
  supersedes_id TEXT REFERENCES offer_option_sets(id),
  created_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL
);

CREATE TABLE offer_options (
  id TEXT PRIMARY KEY,
  set_id TEXT NOT NULL REFERENCES offer_option_sets(id),
  label TEXT NOT NULL CHECK (length(trim(label)) > 0)
);

CREATE TABLE qualification_current (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  evaluation_id TEXT NOT NULL REFERENCES qualification_evaluations(id)
);

CREATE INDEX opportunities_company_idx ON opportunities(company_id);

CREATE INDEX opportunities_updated_idx ON opportunities(updated_at, id);

CREATE INDEX opportunities_source_url_idx ON opportunities(source_url);

CREATE INDEX evidence_opportunity_criterion_idx ON evidence(opportunity_id, criterion, created_at);

CREATE INDEX evidence_supersedes_idx ON evidence(supersedes_id);

CREATE INDEX qualification_opportunity_idx ON qualification_evaluations(opportunity_id, created_at);

CREATE INDEX actions_status_due_date_idx ON actions(status, due_date);

CREATE INDEX actions_status_due_at_idx ON actions(status, due_at);

CREATE INDEX audit_entity_idx ON audit_changes(entity_kind, entity_id, occurred_at);

CREATE INDEX auth_sessions_expiry_idx ON auth_sessions(expires_at);

CREATE INDEX agent_credentials_expiry_idx ON agent_credentials(expires_at);

CREATE INDEX jobs_ready_idx ON jobs(state, available_at, created_at, id);

CREATE INDEX jobs_expired_lease_idx ON jobs(state, lease_until);

CREATE INDEX job_attempts_job_idx ON job_attempts(job_id, attempt_no);

CREATE INDEX record_changes_entity_idx ON record_changes(entity_kind, entity_id, sequence);

CREATE INDEX evidence_sources_opportunity_idx ON evidence_sources(opportunity_id,recorded_at,id);

CREATE INDEX offer_option_sets_opportunity_idx ON offer_option_sets(opportunity_id,context_version);

CREATE INDEX offer_options_set_idx ON offer_options(set_id);

CREATE UNIQUE INDEX offer_option_set_successor_idx ON offer_option_sets(supersedes_id)
  WHERE supersedes_id IS NOT NULL;

CREATE UNIQUE INDEX evidence_successor_unique ON evidence(supersedes_id)
WHERE supersedes_id IS NOT NULL;

CREATE INDEX evidence_source_idx ON evidence(source_id);

CREATE INDEX evidence_role_current_idx ON evidence
  (opportunity_id,role_criterion_id,role_definition_hash,role_preferences_version)
  WHERE role_criterion_id IS NOT NULL;

CREATE INDEX qualification_tuple_idx ON qualification_evaluations
  (opportunity_id,material_version,evidence_version,preferences_version,created_at);

CREATE TRIGGER record_changes_no_update BEFORE UPDATE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record changes are immutable'); END;

CREATE TRIGGER record_changes_no_delete BEFORE DELETE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record changes are immutable'); END;

CREATE TRIGGER record_changes_from_audit AFTER INSERT ON audit_changes
WHEN NEW.entity_kind IN ('company', 'opportunity')
BEGIN
  INSERT INTO record_changes
    (audit_id,entity_kind,entity_id,operation,actor_kind,actor_id,
     revision_before,revision_after,occurred_at,snapshot_state,snapshot_json)
  VALUES (
    NEW.id, NEW.entity_kind, NEW.entity_id, NEW.operation, NEW.actor_kind, NEW.actor_id,
    NEW.revision_before, NEW.revision_after, NEW.occurred_at, 'captured',
    CASE NEW.entity_kind
      WHEN 'company' THEN (
        SELECT json_object(
          'id',id,'name',name,'website',website,'notes',notes,
          'archivedAt',archived_at,'revision',revision,
          'createdAt',created_at,'updatedAt',updated_at)
        FROM companies WHERE id=NEW.entity_id)
      WHEN 'opportunity' THEN (
        SELECT json_object(
          'id',o.id,'companyId',o.company_id,'title',o.title,'kind',o.kind,
          'sourceUrl',o.source_url,'originalText',o.original_text,'notes',o.notes,
          'stage',o.stage,'workPattern',o.work_pattern,'locationText',o.location_text,
          'postedOn',o.posted_on,'deadlineOn',o.deadline_on,'archivedAt',o.archived_at,
          'revision',o.revision,'createdAt',o.created_at,'updatedAt',o.updated_at,
          'compensation',json_object(
            'currency',c.currency,'minAmountCents',c.min_amount_cents,
            'maxAmountCents',c.max_amount_cents,'period',c.period,
            'referenceHoursHundredths',c.reference_hours_hundredths,'basis',c.basis,
            'annualConversion',c.annual_conversion,
            'annualConversionSpanStart',c.annual_conversion_span_start,
            'annualConversionSpanEnd',c.annual_conversion_span_end,
            'annualConversionExcerpt',c.annual_conversion_excerpt,
            'annualConversionSHA256',c.annual_conversion_sha256,
            'benefitsText',c.benefits_text))
        FROM opportunities o LEFT JOIN compensation c ON c.opportunity_id=o.id
        WHERE o.id=NEW.entity_id)
    END
  );
END;

CREATE TRIGGER evidence_sources_no_update BEFORE UPDATE ON evidence_sources
BEGIN SELECT RAISE(ABORT,'evidence sources are immutable'); END;

CREATE TRIGGER evidence_sources_no_delete BEFORE DELETE ON evidence_sources
BEGIN SELECT RAISE(ABORT,'evidence sources are immutable'); END;

CREATE TRIGGER offer_option_sets_no_update BEFORE UPDATE ON offer_option_sets
BEGIN SELECT RAISE(ABORT,'offer option sets are immutable'); END;

CREATE TRIGGER offer_option_sets_no_delete BEFORE DELETE ON offer_option_sets
BEGIN SELECT RAISE(ABORT,'offer option sets are immutable'); END;

CREATE TRIGGER offer_options_no_update BEFORE UPDATE ON offer_options
BEGIN SELECT RAISE(ABORT,'offer options are immutable'); END;

CREATE TRIGGER offer_options_no_delete BEFORE DELETE ON offer_options
BEGIN SELECT RAISE(ABORT,'offer options are immutable'); END;

CREATE TRIGGER qualification_offer_set_insert AFTER INSERT ON offer_option_sets
BEGIN
  UPDATE qualification_input_versions SET evidence_version=evidence_version+1
    WHERE opportunity_id=NEW.opportunity_id;
  INSERT INTO qualification_refresh_queue(opportunity_id,requested_at,reason)
  VALUES (NEW.opportunity_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'offer.options')
  ON CONFLICT(opportunity_id) DO UPDATE SET requested_at=excluded.requested_at,reason=excluded.reason;
END;

CREATE TRIGGER evidence_role_guard BEFORE INSERT ON evidence
WHEN NEW.criterion='role_criterion'
BEGIN
  SELECT CASE WHEN NEW.role_criterion_id IS NULL OR NEW.role_definition_hash IS NULL OR
    NEW.role_definition_json IS NULL OR
    NEW.role_preferences_version IS NULL OR NEW.role_presence IS NULL OR
    NEW.source_id IS NULL OR length(NEW.role_definition_hash)<>64
    THEN RAISE(ABORT,'role claim requires bound definition and source') END;
  SELECT CASE WHEN NEW.supersedes_id IS NOT NULL AND NOT EXISTS(
    SELECT 1 FROM evidence old WHERE old.id=NEW.supersedes_id
      AND old.role_criterion_id=NEW.role_criterion_id
      AND old.role_definition_hash=NEW.role_definition_hash)
    THEN RAISE(ABORT,'role supersession definition mismatch') END;
END;

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

CREATE TRIGGER qualification_evaluations_no_update BEFORE UPDATE ON qualification_evaluations
BEGIN SELECT RAISE(ABORT,'qualification evaluations are immutable'); END;

CREATE TRIGGER qualification_evaluations_no_delete BEFORE DELETE ON qualification_evaluations
BEGIN SELECT RAISE(ABORT,'qualification evaluations are immutable'); END;

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
  OLD.reference_hours_hundredths IS NOT NEW.reference_hours_hundredths OR
  OLD.annual_conversion IS NOT NEW.annual_conversion OR
  OLD.annual_conversion_span_start IS NOT NEW.annual_conversion_span_start OR
  OLD.annual_conversion_span_end IS NOT NEW.annual_conversion_span_end OR
  OLD.annual_conversion_sha256 IS NOT NEW.annual_conversion_sha256 OR
  OLD.basis IS NOT NEW.basis
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
