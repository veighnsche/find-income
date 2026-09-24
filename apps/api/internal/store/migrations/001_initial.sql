-- Initial schema for the single-owner recruitment prototype.
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

CREATE TABLE relationship_counterparties (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('recruiter','referrer','contact')),
  organization_text TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_ref TEXT,
  source_excerpt TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE relationship_events (
  id TEXT PRIMARY KEY,
  counterparty_id TEXT REFERENCES relationship_counterparties(id),
  opportunity_id TEXT REFERENCES opportunities(id),
  kind TEXT NOT NULL CHECK(kind IN ('introduction','conversation','referral','other')),
  summary TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_ref TEXT,
  source_excerpt TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE opportunity_routes (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  event_id TEXT REFERENCES relationship_events(id),
  counterparty_id TEXT REFERENCES relationship_counterparties(id),
  kind TEXT NOT NULL CHECK(kind IN ('direct','referral','recruiter')),
  destination_text TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_ref TEXT,
  source_excerpt TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE delivery_route_assessments (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  route_id TEXT NOT NULL REFERENCES opportunity_routes(id),
  source_sha256 TEXT NOT NULL,
  route_sha256 TEXT NOT NULL,
  input_sha256 TEXT NOT NULL,
  choice TEXT NOT NULL CHECK (choice IN ('application_mailbox','other_contact','unresolved')),
  round_id TEXT NOT NULL REFERENCES rounds(id),
  jev_attempt_id TEXT NOT NULL UNIQUE REFERENCES jev_attempts(id),
  result_json TEXT NOT NULL CHECK (json_valid(result_json)),
  created_at TEXT NOT NULL,
  UNIQUE(route_id,source_sha256,route_sha256)
);

CREATE TABLE delivery_reviews (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  pack_ids_json TEXT NOT NULL CHECK (json_valid(pack_ids_json)),
  material_sha256 TEXT NOT NULL,
  approved_sha256 TEXT,
  approved_at TEXT,
  created_at TEXT NOT NULL,
  CHECK ((approved_sha256 IS NULL) = (approved_at IS NULL)),
  UNIQUE(owner_id,request_key)
);
CREATE TABLE delivery_items (
  id TEXT PRIMARY KEY,
  review_id TEXT NOT NULL REFERENCES delivery_reviews(id),
  pack_id TEXT NOT NULL REFERENCES application_packs(id),
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL,
  source_sha256 TEXT NOT NULL,
  profile_revision INTEGER NOT NULL,
  pack_content_sha256 TEXT NOT NULL,
  route_id TEXT NOT NULL REFERENCES opportunity_routes(id),
  route_revision INTEGER NOT NULL,
  route_sha256 TEXT NOT NULL,
  title TEXT NOT NULL,
  company_name TEXT NOT NULL,
  route_excerpt TEXT NOT NULL,
  recipient TEXT NOT NULL,
  sender TEXT NOT NULL,
  subject TEXT NOT NULL,
  body TEXT NOT NULL,
  attachment_sha256 TEXT NOT NULL,
  mime_sha256 TEXT NOT NULL,
  mime_bytes BLOB NOT NULL,
  message_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('prepared','sending','accepted_by_smtp','failed','uncertain')),
  round_id TEXT REFERENCES rounds(id),
  attempt_id TEXT REFERENCES round_attempts(id),
  smtp_stage TEXT NOT NULL DEFAULT '',
  smtp_code INTEGER NOT NULL DEFAULT 0,
  outcome_detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(review_id,pack_id),
  UNIQUE(message_id)
);
CREATE INDEX delivery_items_by_review ON delivery_items(review_id);
CREATE UNIQUE INDEX delivery_one_possible_submission ON delivery_items(pack_id)
  WHERE state IN ('sending','accepted_by_smtp','uncertain');
CREATE TABLE interviews (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL CHECK(opportunity_revision>0),
  profile_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  context_text TEXT NOT NULL CHECK(length(context_text)>0 AND length(context_text)<=30000),
  context_sha256 TEXT NOT NULL,
  round_id TEXT REFERENCES rounds(id),
  brief_json TEXT CHECK(brief_json IS NULL OR json_valid(brief_json)),
  brief_sha256 TEXT,
  focus_json TEXT CHECK(focus_json IS NULL OR json_valid(focus_json)),
  focus_jev_attempt_id TEXT REFERENCES jev_attempts(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_id,request_key)
);

CREATE TABLE interview_debriefs (
  id TEXT PRIMARY KEY,
  interview_id TEXT NOT NULL REFERENCES interviews(id),
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  owner_notes TEXT NOT NULL CHECK(length(owner_notes)>0 AND length(owner_notes)<=30000),
  round_id TEXT REFERENCES rounds(id),
  debrief_json TEXT CHECK(debrief_json IS NULL OR json_valid(debrief_json)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_id,request_key)
);

-- A commissioned round is the sole authority for recruitment work. The
-- partial unique index includes paused rounds so restart never frees a slot.
CREATE TABLE rounds (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  intent TEXT NOT NULL CHECK (length(trim(intent)) BETWEEN 1 AND 2000),
  outcome TEXT NOT NULL CHECK (length(trim(outcome)) BETWEEN 1 AND 100 AND outcome <> 'discover'),
  initial_profile_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  profile_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  scope_json TEXT NOT NULL CHECK (json_valid(scope_json)),
  state TEXT NOT NULL CHECK (state IN ('queued','running','awaiting_input','stopping','paused','completed','failed')),
  revision INTEGER NOT NULL CHECK (revision > 0),
  generation INTEGER NOT NULL CHECK (generation > 0),
  deadline_at TEXT NOT NULL,
  request_limit INTEGER NOT NULL CHECK (request_limit >= 0),
  item_limit INTEGER NOT NULL CHECK (item_limit >= 0),
  tool_limit INTEGER NOT NULL CHECK (tool_limit >= 0),
  turn_limit INTEGER NOT NULL CHECK (turn_limit >= 0),
  requests_used INTEGER NOT NULL DEFAULT 0 CHECK (requests_used >= 0 AND requests_used <= request_limit),
  items_used INTEGER NOT NULL DEFAULT 0 CHECK (items_used >= 0 AND items_used <= item_limit),
  tools_used INTEGER NOT NULL DEFAULT 0 CHECK (tools_used >= 0 AND tools_used <= tool_limit),
  turns_used INTEGER NOT NULL DEFAULT 0 CHECK (turns_used >= 0 AND turns_used <= turn_limit),
  step TEXT NOT NULL DEFAULT '',
  cursor_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(cursor_json)),
  unresolved_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(unresolved_json)),
  report_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(report_json)),
  stop_reason TEXT NOT NULL DEFAULT '',
  deliverable_status TEXT NOT NULL DEFAULT '',
  reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation_required IN (0,1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(actor_kind,actor_id,request_key)
);
CREATE TABLE offer_comparison_intakes (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  sources_json TEXT NOT NULL CHECK (json_valid(sources_json)),
  created_at TEXT NOT NULL,
  UNIQUE(owner_id,request_key)
);
CREATE TABLE offer_comparisons (
  id TEXT PRIMARY KEY,
  intake_id TEXT NOT NULL UNIQUE REFERENCES offer_comparison_intakes(id),
  round_id TEXT NOT NULL UNIQUE REFERENCES rounds(id),
  input_sha256 TEXT NOT NULL,
  comparison_json TEXT NOT NULL CHECK (json_valid(comparison_json)),
  created_at TEXT NOT NULL
);
CREATE TABLE offer_tradeoff_assessments (
  id TEXT PRIMARY KEY,
  comparison_id TEXT NOT NULL UNIQUE REFERENCES offer_comparisons(id),
  round_id TEXT NOT NULL REFERENCES rounds(id),
  jev_attempt_id TEXT NOT NULL UNIQUE REFERENCES jev_attempts(id),
  result_json TEXT NOT NULL CHECK (json_valid(result_json)),
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX one_active_round ON rounds((1))
  WHERE state IN ('queued','running','awaiting_input','stopping','paused');

CREATE TABLE owner_instructions (
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('campaign','profile','opportunity','evidence','relationship','application_pack')),
  target_id TEXT NOT NULL,
  expected_revision INTEGER NOT NULL CHECK (expected_revision > 0),
  round_id TEXT REFERENCES rounds(id),
  text TEXT NOT NULL CHECK (length(trim(text)) BETWEEN 1 AND 20000),
  created_at TEXT NOT NULL,
  revoked_at TEXT,
  UNIQUE(actor_id,request_key)
);
CREATE INDEX owner_instructions_by_round ON owner_instructions(round_id,created_at);
CREATE INDEX owner_instructions_by_target ON owner_instructions(target_kind,target_id,created_at);

CREATE TABLE owner_instruction_applications (
  audit_id TEXT PRIMARY KEY REFERENCES audit_changes(id),
  instruction_id TEXT NOT NULL REFERENCES owner_instructions(id)
);

CREATE TABLE owner_opportunity_decisions (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0),
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision > 0),
  decision TEXT NOT NULL CHECK (decision IN ('selected','dismissed','acknowledged')),
  audit_id TEXT NOT NULL REFERENCES audit_changes(id),
  created_at TEXT NOT NULL,
  UNIQUE(actor_id,request_key),
  UNIQUE(opportunity_id,revision)
);
CREATE INDEX owner_opportunity_decisions_latest ON owner_opportunity_decisions(opportunity_id,revision DESC);

CREATE TABLE round_attempts (
  id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  operation TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK (generation > 0),
  state TEXT NOT NULL CHECK (state IN ('reserved','dispatched','succeeded','failed','uncertain','observed_success','observed_failure','cancelled')),
  requests_reserved INTEGER NOT NULL CHECK (requests_reserved >= 0),
  items_reserved INTEGER NOT NULL CHECK (items_reserved >= 0),
  tools_reserved INTEGER NOT NULL CHECK (tools_reserved >= 0),
  turns_reserved INTEGER NOT NULL CHECK (turns_reserved >= 0),
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  late_result_json TEXT CHECK (late_result_json IS NULL OR json_valid(late_result_json)),
  error_code TEXT NOT NULL DEFAULT '',
  cancel_requested_at TEXT,
  cancel_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  dispatched_at TEXT,
  finished_at TEXT,
  UNIQUE(round_id,request_key)
);
CREATE INDEX round_attempts_by_state ON round_attempts(round_id,state);

CREATE TABLE round_remote_dispatches (
  attempt_id TEXT PRIMARY KEY REFERENCES round_attempts(id),
  round_id TEXT NOT NULL REFERENCES rounds(id),
  generation INTEGER NOT NULL,
  thread_id TEXT,
  turn_id TEXT,
  observed_status TEXT CHECK (observed_status IN ('completed','failed','interrupted','unknown')),
  observed_evidence_json TEXT CHECK (observed_evidence_json IS NULL OR json_valid(observed_evidence_json)),
  updated_at TEXT NOT NULL
);

CREATE TABLE round_tool_capabilities (
  token_sha256 TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  attempt_id TEXT NOT NULL REFERENCES round_attempts(id),
  actor_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  issued_at TEXT NOT NULL,
  revoked_at TEXT
);
CREATE UNIQUE INDEX round_tool_capabilities_by_attempt ON round_tool_capabilities(attempt_id);

CREATE TABLE round_results (
  id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  attempt_id TEXT NOT NULL UNIQUE REFERENCES round_attempts(id),
  result_json TEXT NOT NULL CHECK (json_valid(result_json)),
  created_at TEXT NOT NULL
);

CREATE TABLE round_record_changes (
  round_id TEXT NOT NULL REFERENCES rounds(id),
  attempt_id TEXT NOT NULL REFERENCES round_attempts(id),
  audit_id TEXT NOT NULL REFERENCES audit_changes(id),
  attached_at TEXT NOT NULL,
  PRIMARY KEY (round_id,audit_id)
);

CREATE TABLE round_reconciliation_checks (
  id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  attempt_id TEXT NOT NULL REFERENCES round_attempts(id),
  control_generation INTEGER NOT NULL CHECK (control_generation > 0),
  state TEXT NOT NULL CHECK (state IN ('pending','resolved','unknown','fenced')),
  evidence_json TEXT CHECK (evidence_json IS NULL OR json_valid(evidence_json)),
  created_at TEXT NOT NULL,
  finished_at TEXT
);

CREATE TABLE jev_attempts (
  id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  round_attempt_id TEXT NOT NULL REFERENCES round_attempts(id),
  step_index INTEGER NOT NULL,
  purpose TEXT NOT NULL,
  input_sha256 TEXT NOT NULL,
  source_refs_json TEXT NOT NULL CHECK (json_valid(source_refs_json)),
  candidate_set_json TEXT NOT NULL CHECK (json_valid(candidate_set_json)),
  profile_version INTEGER NOT NULL,
  rubric_version TEXT NOT NULL,
  requested_model TEXT,
  returned_model TEXT,
  logical_request_json BLOB,
  transport_request_bytes BLOB,
  raw_response_bytes BLOB,
  response_truncated INTEGER NOT NULL DEFAULT 0 CHECK (response_truncated IN (0,1)),
  response_read_error INTEGER NOT NULL DEFAULT 0 CHECK (response_read_error IN (0,1)),
  status TEXT NOT NULL CHECK (status IN ('dispatched','succeeded','invalid_response','failed','budget_exceeded','uncertain')),
  error_kind TEXT,
  http_status INTEGER,
  input_tokens INTEGER,
  output_tokens INTEGER,
  created_at TEXT NOT NULL,
  finished_at TEXT,
  UNIQUE(round_attempt_id,step_index)
);

CREATE TABLE round_jev_assessments (
  id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  kind TEXT NOT NULL CHECK(kind IN ('screening','organisation')),
  source_id TEXT NOT NULL REFERENCES evidence_sources(id),
  source_revision TEXT NOT NULL,
  opportunity_revision INTEGER NOT NULL,
  profile_version INTEGER NOT NULL,
  category_version INTEGER,
  round_generation INTEGER NOT NULL,
  input_sha256 TEXT NOT NULL,
  input_json TEXT NOT NULL CHECK(json_valid(input_json)),
  result_json TEXT NOT NULL CHECK(json_valid(result_json)),
  jev_attempt_ids_json TEXT NOT NULL CHECK(json_valid(jev_attempt_ids_json)),
  omitted_bytes INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE round_jev_current (
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  kind TEXT NOT NULL,
  assessment_id TEXT NOT NULL REFERENCES round_jev_assessments(id),
  PRIMARY KEY(opportunity_id,kind)
);

CREATE TABLE application_packs (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL,
  profile_revision INTEGER NOT NULL,
  version INTEGER NOT NULL,
  content_sha256 TEXT NOT NULL,
  manifest_json TEXT NOT NULL,
  typst_source BLOB NOT NULL,
  pdf BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(opportunity_id,version)
);

-- Owner submissions enter this durable
-- intake. Jobs provide leases/attempt history; this row preserves source and
-- the exact verified record mapping across retries and process restarts.
CREATE TABLE ingestion_requests (
  id TEXT PRIMARY KEY,
  origin TEXT NOT NULL CHECK (origin IN ('owner','agent')),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  submission_sha256 TEXT NOT NULL,
  source_url TEXT,
  original_text TEXT NOT NULL DEFAULT '',
  source_opening_id TEXT REFERENCES source_openings(id),
  status TEXT NOT NULL CHECK (status IN ('pending','processing','completed','needs_text','failed')),
  job_id TEXT NOT NULL REFERENCES jobs(id),
  attempts_started INTEGER NOT NULL DEFAULT 1 CHECK (attempts_started > 0),
  dispatch_started INTEGER NOT NULL DEFAULT 0 CHECK (dispatch_started IN (0,1)),
  codex_thread_id TEXT,
  codex_turn_id TEXT,
  dispatch_terminal_status TEXT CHECK (dispatch_terminal_status IN ('completed','failed','interrupted')),
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
  CHECK (status <> 'completed' OR (opportunity_id IS NOT NULL AND record_change_id IS NOT NULL AND source_id IS NOT NULL)),
  CHECK (status <> 'needs_text' OR source_url IS NOT NULL)
);

CREATE INDEX ingestion_requests_recent_idx ON ingestion_requests(created_at,id);
CREATE INDEX ingestion_requests_job_idx ON ingestion_requests(job_id);
CREATE INDEX ingestion_requests_opportunity_idx ON ingestion_requests(opportunity_id);
CREATE INDEX ingestion_requests_source_opening_idx ON ingestion_requests(source_opening_id,created_at);

-- A contextual vacancy commission keeps its original owner request identity
-- even when source deduplication points several commissions at one ingestion.
CREATE TABLE owner_input_sources (
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  ingestion_id TEXT NOT NULL REFERENCES ingestion_requests(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (actor_id,request_key)
);

CREATE TABLE ingestion_dispatch_history (
  ingestion_id TEXT NOT NULL REFERENCES ingestion_requests(id),
  job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
  codex_thread_id TEXT NOT NULL,
  codex_turn_id TEXT NOT NULL,
  terminal_status TEXT NOT NULL CHECK (terminal_status IN ('completed','failed','interrupted')),
  confirmed_at TEXT NOT NULL,
  PRIMARY KEY (ingestion_id,job_id)
);

-- One reliable source key owns a stable opportunity mapping. The digest is
-- only the latest observed revision; every retrieval is retained below.
CREATE TABLE source_openings (
  id TEXT PRIMARY KEY,
  identity_key TEXT NOT NULL UNIQUE,
  canonical_url TEXT,
  current_sha256 TEXT NOT NULL,
  current_ingestion_id TEXT NOT NULL REFERENCES ingestion_requests(id),
  opportunity_id TEXT REFERENCES opportunities(id),
  latest_observed_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE source_sightings (
  id TEXT PRIMARY KEY,
  source_opening_id TEXT NOT NULL REFERENCES source_openings(id),
  ingestion_id TEXT NOT NULL REFERENCES ingestion_requests(id),
  source_url TEXT,
  original_text TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  decision TEXT NOT NULL CHECK (decision IN ('new','changed','unchanged','older'))
);
CREATE INDEX source_sightings_opening_idx ON source_sightings(source_opening_id,observed_at,id);
CREATE TRIGGER source_sightings_no_update BEFORE UPDATE ON source_sightings
BEGIN SELECT RAISE(ABORT,'source sightings are immutable'); END;
CREATE TRIGGER source_sightings_no_delete BEFORE DELETE ON source_sightings
BEGIN SELECT RAISE(ABORT,'source sightings are immutable'); END;

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

-- Bounded read-only correspondence mirror (I19). Rows are written only by an
-- owner-commissioned sync from an authorised account. Nothing here sends, and
-- notifications are inert records: no trigger starts a round.
CREATE TABLE correspondence_accounts (
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL,
  provider TEXT NOT NULL CHECK(length(provider)>0 AND length(provider)<=80),
  external_account_id TEXT NOT NULL CHECK(length(external_account_id)>0 AND length(external_account_id)<=320),
  display_name TEXT NOT NULL CHECK(length(display_name)<=200),
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','auth_lost','disabled')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_id,provider,external_account_id)
);

CREATE TABLE correspondence_threads (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES correspondence_accounts(id),
  actor_id TEXT NOT NULL,
  provider_thread_id TEXT NOT NULL CHECK(length(provider_thread_id)>0 AND length(provider_thread_id)<=320),
  subject TEXT NOT NULL DEFAULT '' CHECK(length(subject)<=500),
  opportunity_id TEXT REFERENCES opportunities(id),
  last_message_at TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0 CHECK(message_count>=0),
  provenance_json TEXT NOT NULL CHECK(json_valid(provenance_json)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(account_id,provider_thread_id)
);
CREATE INDEX correspondence_threads_actor ON correspondence_threads(actor_id,updated_at);

CREATE TABLE correspondence_messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL REFERENCES correspondence_threads(id),
  actor_id TEXT NOT NULL,
  provider_message_id TEXT NOT NULL CHECK(length(provider_message_id)>0 AND length(provider_message_id)<=320),
  sender TEXT NOT NULL CHECK(length(sender)<=320),
  recipients_json TEXT NOT NULL CHECK(json_valid(recipients_json)),
  sent_at TEXT NOT NULL,
  body_sha256 TEXT NOT NULL,
  body TEXT NOT NULL CHECK(length(body)<=100000),
  provenance_json TEXT NOT NULL CHECK(json_valid(provenance_json)),
  created_at TEXT NOT NULL,
  UNIQUE(thread_id,provider_message_id)
);
CREATE INDEX correspondence_messages_thread ON correspondence_messages(thread_id,sent_at);

CREATE TABLE correspondence_notifications (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES correspondence_accounts(id),
  actor_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('incoming','due')),
  thread_id TEXT REFERENCES correspondence_threads(id),
  due_at TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
  created_at TEXT NOT NULL
);

-- Reply processing commissions and Codex-owned follow-up drafts (I23).
-- Drafts are immutable once saved; sending a draft reuses the exact reviewed
-- delivery approval path and is never implied here.
CREATE TABLE reply_processings (
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL,
  thread_id TEXT NOT NULL REFERENCES correspondence_threads(id),
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  profile_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  round_id TEXT REFERENCES rounds(id),
  intent TEXT NOT NULL DEFAULT '' CHECK(length(intent)<=80),
  intent_jev_attempt_id TEXT REFERENCES jev_attempts(id),
  opportunity_id TEXT REFERENCES opportunities(id),
  processed_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_id,request_key)
);

CREATE TABLE reply_drafts (
  id TEXT PRIMARY KEY,
  processing_id TEXT NOT NULL REFERENCES reply_processings(id),
  actor_id TEXT NOT NULL,
  thread_id TEXT NOT NULL REFERENCES correspondence_threads(id),
  round_id TEXT NOT NULL REFERENCES rounds(id),
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision=1),
  draft_json TEXT NOT NULL CHECK(json_valid(draft_json)),
  draft_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(processing_id)
);

-- Research storage foundation (T07 lane C; frozen per T06 contract §§1-6,
-- T03 §1 as corrected by D-input C1-C8). Actor-owned rows carry
-- (actor_kind, actor_id) per T03 conventions. source_captures rows are
-- content-addressed stateless-reusable artifacts shared across actors, so
-- they carry no actor columns; run_events/run_checkpoints scope via rounds.
CREATE TABLE source_captures (
  id TEXT PRIMARY KEY,
  content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
  artifact_ref TEXT NOT NULL CHECK (length(artifact_ref) > 0),
  byte_length INTEGER NOT NULL CHECK (byte_length >= 0),
  media_type TEXT NOT NULL DEFAULT '',
  http_status INTEGER,
  original_url TEXT,
  final_url TEXT,
  redirect_chain_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(redirect_chain_json)),
  retrieved_at TEXT NOT NULL,
  provenance_kind TEXT NOT NULL CHECK (provenance_kind IN
    ('fetched_response','rendered_dom','search_result','owner_statement')),
  completeness TEXT NOT NULL CHECK (completeness IN
    ('complete','truncated','paginated','partial')),
  extent_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(extent_json)),
  executor_identity_json TEXT NOT NULL CHECK (json_valid(executor_identity_json)),
  role_id TEXT,
  is_snippet INTEGER NOT NULL DEFAULT 0 CHECK (is_snippet IN (0,1)),
  created_at TEXT NOT NULL
);
CREATE INDEX source_captures_content_idx ON source_captures(content_sha256);
CREATE INDEX source_captures_url_history_idx ON source_captures(original_url, retrieved_at);
CREATE TRIGGER source_captures_no_update BEFORE UPDATE ON source_captures
BEGIN SELECT RAISE(ABORT,'source captures are immutable'); END;
CREATE TRIGGER source_captures_no_delete BEFORE DELETE ON source_captures
BEGIN SELECT RAISE(ABORT,'source captures are immutable'); END;

CREATE TABLE research_requests (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
  request_json TEXT NOT NULL CHECK (json_valid(request_json)),
  cache_scope TEXT NOT NULL CHECK (cache_scope IN
    ('stateless_reusable','stateful_context_bound')),
  state TEXT NOT NULL CHECK (state IN
    ('free','claimed','fresh','stale','exhausted','uncertain')),
  lease_owner TEXT,
  lease_generation INTEGER CHECK (lease_generation IS NULL OR lease_generation > 0),
  lease_until TEXT,
  latest_observation_id TEXT REFERENCES research_observations(id),
  latest_capture_id TEXT REFERENCES source_captures(id),
  fresh_until TEXT,
  negative_until TEXT,
  refresh_reason TEXT CHECK (refresh_reason IS NULL OR refresh_reason IN
    ('stale','changed_source','coverage_gap','owner_correction')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_kind, actor_id, fingerprint),
  CHECK ((state = 'claimed' AND lease_owner IS NOT NULL AND
      lease_generation IS NOT NULL AND lease_until IS NOT NULL)
    OR (state <> 'claimed' AND lease_owner IS NULL AND
      lease_generation IS NULL AND lease_until IS NULL))
);
CREATE UNIQUE INDEX research_requests_live_claim_idx ON
  research_requests(actor_kind, actor_id, fingerprint) WHERE state = 'claimed';
CREATE INDEX research_requests_actor_updated_idx ON
  research_requests(actor_kind, actor_id, updated_at);
CREATE INDEX research_requests_expiry_idx ON research_requests(state, lease_until);
CREATE INDEX research_requests_latest_capture_idx ON research_requests(latest_capture_id);

CREATE TABLE research_observations (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL REFERENCES research_requests(id),
  attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  round_attempt_id TEXT REFERENCES round_attempts(id),
  operation TEXT NOT NULL CHECK (operation IN
    ('search','fetch','browser_action','api_call','exec')),
  actual_url_or_query TEXT NOT NULL,
  actual_params_json TEXT CHECK (actual_params_json IS NULL OR json_valid(actual_params_json)),
  started_at TEXT NOT NULL,
  finished_at TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN
    ('success','empty','blocked','failed','rate_limited','uncertain','late')),
  provenance_kind TEXT NOT NULL CHECK (provenance_kind IN
    ('fetched_response','rendered_dom','search_result','owner_statement','model_note')),
  receipt_ref TEXT,
  executor_identity_json TEXT CHECK
    (executor_identity_json IS NULL OR json_valid(executor_identity_json)),
  capture_id TEXT REFERENCES source_captures(id),
  error_code TEXT NOT NULL DEFAULT '',
  truncation_note TEXT NOT NULL DEFAULT '',
  is_late INTEGER NOT NULL DEFAULT 0 CHECK (is_late IN (0,1)),
  created_at TEXT NOT NULL,
  UNIQUE(request_id, attempt_no),
  CHECK (capture_id IS NOT NULL OR outcome IN
    ('empty','blocked','failed','rate_limited','uncertain'))
);
CREATE INDEX research_observations_capture_idx ON research_observations(capture_id);
CREATE INDEX research_observations_round_idx ON research_observations(round_id, created_at);
CREATE TRIGGER research_observations_no_update BEFORE UPDATE ON research_observations
BEGIN SELECT RAISE(ABORT,'research observations are immutable'); END;
CREATE TRIGGER research_observations_no_delete BEFORE DELETE ON research_observations
BEGIN SELECT RAISE(ABORT,'research observations are immutable'); END;

CREATE TABLE research_notes (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  brief_profile_version INTEGER NOT NULL CHECK (brief_profile_version > 0),
  brief_rubric_version TEXT NOT NULL,
  intent TEXT NOT NULL CHECK (length(trim(intent)) > 0),
  usefulness_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(usefulness_json)),
  coverage_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(coverage_json)),
  overlap_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(overlap_refs_json)),
  conclusion TEXT NOT NULL DEFAULT '',
  evidence_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_refs_json)),
  outstanding_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(outstanding_json)),
  supersedes_id TEXT REFERENCES research_notes(id),
  created_at TEXT NOT NULL
);
CREATE INDEX research_notes_round_idx ON research_notes(round_id, created_at);
CREATE UNIQUE INDEX research_note_successor_idx ON research_notes(supersedes_id)
  WHERE supersedes_id IS NOT NULL;
CREATE VIRTUAL TABLE research_notes_fts USING
  fts5(intent, coverage, conclusion, content='research_notes', content_rowid='rowid',
  tokenize='unicode61');
CREATE TRIGGER research_notes_fts_insert AFTER INSERT ON research_notes
BEGIN
  INSERT INTO research_notes_fts(rowid, intent, coverage, conclusion)
  VALUES (NEW.rowid, NEW.intent, NEW.coverage_json, NEW.conclusion);
END;
CREATE TRIGGER research_notes_fts_delete AFTER DELETE ON research_notes
BEGIN
  INSERT INTO research_notes_fts(research_notes_fts, rowid, intent, coverage, conclusion)
  VALUES ('delete', OLD.rowid, OLD.intent, OLD.coverage_json, OLD.conclusion);
END;
CREATE TRIGGER research_notes_fts_update AFTER UPDATE ON research_notes
BEGIN
  INSERT INTO research_notes_fts(research_notes_fts, rowid, intent, coverage, conclusion)
  VALUES ('delete', OLD.rowid, OLD.intent, OLD.coverage_json, OLD.conclusion);
  INSERT INTO research_notes_fts(rowid, intent, coverage, conclusion)
  VALUES (NEW.rowid, NEW.intent, NEW.coverage_json, NEW.conclusion);
END;

-- D §1.1 with corrections C1-C4: candidate entries carry kind+revision, the
-- set hash is canonical, decision=same names exactly one chosen target plus
-- its revision, distinguishing refs feed explanation/audit, and identity is
-- brief-independent by design (no brief_version column).
CREATE TABLE identity_decisions (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('employer','vacancy')),
  observation_refs_json TEXT NOT NULL CHECK (json_valid(observation_refs_json)),
  candidate_ids_json TEXT NOT NULL CHECK (json_valid(candidate_ids_json)),
  candidate_set_hash TEXT NOT NULL CHECK (length(candidate_set_hash) = 64),
  decision TEXT NOT NULL CHECK (decision IN ('same','new','unresolved')),
  decision_basis TEXT NOT NULL DEFAULT '',
  subject_company_id TEXT REFERENCES companies(id),
  subject_opportunity_id TEXT REFERENCES opportunities(id),
  subject_revision INTEGER CHECK (subject_revision IS NULL OR subject_revision > 0),
  distinguishing_refs_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(distinguishing_refs_json)),
  brief_independent INTEGER NOT NULL DEFAULT 1 CHECK (brief_independent IN (0,1)),
  jev_assessment_id TEXT REFERENCES jev_assessments_dynamic(id),
  supersedes_id TEXT REFERENCES identity_decisions(id),
  created_at TEXT NOT NULL,
  CHECK ((decision = 'same' AND
      ((subject_company_id IS NOT NULL AND subject_opportunity_id IS NULL) OR
       (subject_company_id IS NULL AND subject_opportunity_id IS NOT NULL)) AND
      subject_revision IS NOT NULL)
    OR (decision <> 'same' AND subject_company_id IS NULL AND
      subject_opportunity_id IS NULL AND subject_revision IS NULL))
);
CREATE INDEX identity_decisions_subject_idx ON
  identity_decisions(subject_kind, decision, created_at);
CREATE INDEX identity_decisions_assessment_idx ON identity_decisions(jev_assessment_id);
CREATE INDEX identity_decisions_candidate_set_idx ON identity_decisions(candidate_set_hash);
CREATE INDEX identity_decisions_round_idx ON identity_decisions(round_id);

-- D §1.2 with corrections C5-C8: strong-key uniqueness is a partial index
-- over current rows only, namespaces are open (shape-validated, never
-- membership-checked), reuse writes a superseding row (never overwrites),
-- and the record link is a split company/opportunity FK pair.
CREATE TABLE entity_identity_keys (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  entity_kind TEXT NOT NULL CHECK (entity_kind IN ('employer','vacancy')),
  namespace TEXT NOT NULL CHECK (length(namespace) BETWEEN 1 AND 128 AND
    namespace NOT GLOB '*[^_a-z0-9:.-]*'),
  key_value TEXT NOT NULL CHECK (length(key_value) BETWEEN 1 AND 1024),
  strength TEXT NOT NULL CHECK (strength IN ('strong','alias')),
  company_id TEXT REFERENCES companies(id),
  opportunity_id TEXT REFERENCES opportunities(id),
  evidence_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_refs_json)),
  status TEXT NOT NULL DEFAULT 'current' CHECK (status IN ('current','superseded')),
  supersedes_id TEXT REFERENCES entity_identity_keys(id),
  created_at TEXT NOT NULL,
  CHECK ((company_id IS NOT NULL AND opportunity_id IS NULL) OR
    (company_id IS NULL AND opportunity_id IS NOT NULL))
);
CREATE UNIQUE INDEX entity_identity_strong_unique ON
  entity_identity_keys(namespace, key_value)
  WHERE strength = 'strong' AND status = 'current';
CREATE INDEX entity_identity_company_idx ON entity_identity_keys(company_id);
CREATE INDEX entity_identity_opportunity_idx ON entity_identity_keys(opportunity_id);
CREATE INDEX entity_identity_lookup_idx ON entity_identity_keys(namespace, key_value);

-- D §2.2: queryable binding over one jev_attempts row (reused verbatim).
CREATE TABLE jev_assessments_dynamic (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  jev_attempt_id TEXT NOT NULL UNIQUE REFERENCES jev_attempts(id),
  purpose TEXT NOT NULL CHECK (length(trim(purpose)) > 0),
  questions_json TEXT NOT NULL CHECK (json_valid(questions_json)),
  evidence_refs_json TEXT NOT NULL CHECK (json_valid(evidence_refs_json)),
  profile_version INTEGER NOT NULL CHECK (profile_version > 0),
  rubric_version TEXT NOT NULL,
  candidates_json TEXT NOT NULL CHECK (json_valid(candidates_json)),
  candidate_set_hash TEXT NOT NULL CHECK (length(candidate_set_hash) = 64),
  requested_model TEXT,
  reuse_key TEXT NOT NULL CHECK (length(reuse_key) = 64),
  status TEXT NOT NULL CHECK (status IN
    ('succeeded','partial_abstain','invalid_response','failed')),
  answers_json TEXT NOT NULL CHECK (json_valid(answers_json)),
  supersedes_id TEXT REFERENCES jev_assessments_dynamic(id),
  created_at TEXT NOT NULL,
  UNIQUE(actor_kind, actor_id, reuse_key)
);
CREATE INDEX jev_dynamic_round_idx ON jev_assessments_dynamic(round_id);
CREATE INDEX jev_dynamic_brief_idx ON
  jev_assessments_dynamic(actor_kind, actor_id, profile_version);
CREATE INDEX jev_dynamic_candidate_set_idx ON jev_assessments_dynamic(candidate_set_hash);
CREATE TRIGGER jev_assessments_dynamic_no_update BEFORE UPDATE ON jev_assessments_dynamic
BEGIN SELECT RAISE(ABORT,'dynamic assessments are immutable'); END;
CREATE TRIGGER jev_assessments_dynamic_no_delete BEFORE DELETE ON jev_assessments_dynamic
BEGIN SELECT RAISE(ABORT,'dynamic assessments are immutable'); END;

-- D §2.3: indexed assessment<->capture join (brief-change invalidation and
-- evidence-integrity walks must not scan JSON).
CREATE TABLE jev_assessment_captures (
  assessment_id TEXT NOT NULL REFERENCES jev_assessments_dynamic(id),
  capture_id TEXT NOT NULL, -- content sha256; resolved via content lookup, verified against bytes at write time
  span_start INTEGER NOT NULL CHECK (span_start >= 0),
  span_end INTEGER NOT NULL CHECK (span_end > span_start),
  PRIMARY KEY (assessment_id, capture_id, span_start, span_end)
);
CREATE INDEX jev_assessment_captures_by_capture ON
  jev_assessment_captures(capture_id, assessment_id);
CREATE TRIGGER jev_assessment_captures_no_update BEFORE UPDATE ON jev_assessment_captures
BEGIN SELECT RAISE(ABORT,'assessment capture links are immutable'); END;
CREATE TRIGGER jev_assessment_captures_no_delete BEFORE DELETE ON jev_assessment_captures
BEGIN SELECT RAISE(ABORT,'assessment capture links are immutable'); END;

-- D §3.1: immutable sighting home for research-saved records. The ingestion
-- path keeps its own source_sightings table; the two never share rows.
CREATE TABLE record_sightings (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  company_id TEXT REFERENCES companies(id),
  opportunity_id TEXT REFERENCES opportunities(id),
  capture_id TEXT NOT NULL, -- content sha256; resolved via content lookup, verified against bytes at write time
  observed_url TEXT,
  final_url TEXT,
  content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
  sighting_kind TEXT NOT NULL CHECK (sighting_kind IN
    ('first','unchanged','changed','reused_identifier')),
  observed_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  CHECK ((company_id IS NOT NULL AND opportunity_id IS NULL) OR
    (company_id IS NULL AND opportunity_id IS NOT NULL))
);
CREATE INDEX record_sightings_opportunity_idx ON
  record_sightings(opportunity_id, observed_at);
CREATE INDEX record_sightings_company_idx ON record_sightings(company_id, observed_at);
CREATE INDEX record_sightings_capture_idx ON record_sightings(capture_id);
CREATE INDEX record_sightings_content_idx ON record_sightings(content_sha256);
CREATE TRIGGER record_sightings_no_update BEFORE UPDATE ON record_sightings
BEGIN SELECT RAISE(ABORT,'record sightings are immutable'); END;
CREATE TRIGGER record_sightings_no_delete BEFORE DELETE ON record_sightings
BEGIN SELECT RAISE(ABORT,'record sightings are immutable'); END;

-- T06 §5 event envelope. kind stays an open string: research kinds are
-- frozen by the contract but B adds run/turn/steering kinds at T08/T13.
-- outcome values are the frozen §8 shared codes.
CREATE TABLE run_events (
  event_id TEXT PRIMARY KEY,
  round_id TEXT NOT NULL REFERENCES rounds(id),
  attempt_id TEXT REFERENCES round_attempts(id),
  kind TEXT NOT NULL CHECK (length(trim(kind)) BETWEEN 1 AND 64),
  request_fingerprint TEXT,
  observation_id TEXT REFERENCES research_observations(id),
  capture_id TEXT REFERENCES source_captures(id),
  outcome TEXT CHECK (outcome IS NULL OR outcome IN ('ok','reused','claimed_elsewhere',
    'stale','revision_conflict','identity_ambiguous','capture_incomplete',
    'budget_exhausted','stopped','rate_limited','outcome_uncertain','invalid',
    'conflict','forbidden','not_found')),
  payload_json TEXT CHECK (payload_json IS NULL OR json_valid(payload_json)),
  observed_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL
);
CREATE INDEX run_events_round_idx ON run_events(round_id, recorded_at, event_id);
CREATE INDEX run_events_kind_idx ON run_events(round_id, kind, recorded_at);
CREATE TRIGGER run_events_no_update BEFORE UPDATE ON run_events
BEGIN SELECT RAISE(ABORT,'run events are immutable'); END;
CREATE TRIGGER run_events_no_delete BEFORE DELETE ON run_events
BEGIN SELECT RAISE(ABORT,'run events are immutable'); END;

-- T06 §5 checkpoint: one mutable row per run, upserted by the supervisor.
-- remaining_allowance_json stays the opaque B struct.
CREATE TABLE run_checkpoints (
  round_id TEXT PRIMARY KEY REFERENCES rounds(id),
  profile_version INTEGER NOT NULL CHECK (profile_version > 0),
  rubric_version TEXT NOT NULL,
  active_claims_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(active_claims_json)),
  evidence_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_ids_json)),
  saved_record_ids_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(saved_record_ids_json)),
  unresolved_attempts_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(unresolved_attempts_json)),
  remaining_allowance_json TEXT CHECK
    (remaining_allowance_json IS NULL OR json_valid(remaining_allowance_json)),
  next_work_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(next_work_json)),
  generation INTEGER NOT NULL CHECK (generation > 0),
  updated_at TEXT NOT NULL
);

-- D §3.3: broad candidate retrieval indexes on existing tables. Full text
-- broadens, never decides.
CREATE INDEX companies_name_idx ON companies(name);
CREATE INDEX opportunities_title_idx ON opportunities(title);
CREATE VIRTUAL TABLE opportunities_fts USING
  fts5(title, location_text, content='opportunities', content_rowid='rowid',
  tokenize='unicode61');
CREATE TRIGGER opportunities_fts_insert AFTER INSERT ON opportunities
BEGIN
  INSERT INTO opportunities_fts(rowid, title, location_text)
  VALUES (NEW.rowid, NEW.title, NEW.location_text);
END;
CREATE TRIGGER opportunities_fts_delete AFTER DELETE ON opportunities
BEGIN
  INSERT INTO opportunities_fts(opportunities_fts, rowid, title, location_text)
  VALUES ('delete', OLD.rowid, OLD.title, OLD.location_text);
END;
CREATE TRIGGER opportunities_fts_update AFTER UPDATE ON opportunities
BEGIN
  INSERT INTO opportunities_fts(opportunities_fts, rowid, title, location_text)
  VALUES ('delete', OLD.rowid, OLD.title, OLD.location_text);
  INSERT INTO opportunities_fts(rowid, title, location_text)
  VALUES (NEW.rowid, NEW.title, NEW.location_text);
END;
