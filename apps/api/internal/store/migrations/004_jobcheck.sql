CREATE TABLE job_checks (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision >= 1),
  workflow_revision INTEGER NOT NULL CHECK (workflow_revision >= 1),
  status TEXT NOT NULL CHECK (status IN ('checking','checked','blocked')),
  blocked_code TEXT NOT NULL DEFAULT '',
  blocked_detail TEXT NOT NULL DEFAULT '',
  vacancy_capture_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(vacancy_capture_ids_json)),
  vacancy_evidence_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(vacancy_evidence_ids_json)),
  vacancy_completeness TEXT NOT NULL DEFAULT '',
  vacancy_source_url TEXT NOT NULL DEFAULT '',
  vacancy_retrieved_at TEXT NOT NULL DEFAULT '',
  documents_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(documents_json)),
  route_json TEXT NOT NULL DEFAULT '',
  gaps_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(gaps_json)),
  question_set_sha256 TEXT NOT NULL DEFAULT '',
  question_set_version INTEGER NOT NULL DEFAULT 0,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE (opportunity_id, request_key)
);
CREATE INDEX job_checks_opportunity_idx ON job_checks(opportunity_id, created_at, id);

CREATE TABLE job_check_questions (
  id TEXT PRIMARY KEY,
  check_id TEXT NOT NULL REFERENCES job_checks(id),
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
  required TEXT NOT NULL CHECK (required IN ('required','optional','unknown')),
  kind TEXT NOT NULL DEFAULT '',
  capture_id TEXT NOT NULL REFERENCES source_captures(id),
  span_start INTEGER NOT NULL CHECK (span_start >= 0),
  span_end INTEGER NOT NULL CHECK (span_end - span_start BETWEEN 1 AND 2000),
  source_excerpt TEXT NOT NULL CHECK (length(source_excerpt) BETWEEN 1 AND 2000),
  text_sha256 TEXT NOT NULL CHECK (length(text_sha256) = 64),
  UNIQUE (check_id, ordinal)
);
CREATE INDEX job_check_questions_check_idx ON job_check_questions(check_id, ordinal);

CREATE TABLE job_check_activity (
  event_id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  check_id TEXT REFERENCES job_checks(id),
  kind TEXT NOT NULL CHECK (length(trim(kind)) BETWEEN 1 AND 64),
  request_fingerprint TEXT,
  observation_id TEXT,
  capture_id TEXT REFERENCES source_captures(id),
  outcome TEXT CHECK (outcome IS NULL OR outcome IN ('ok','reused','claimed_elsewhere',
    'stale','revision_conflict','identity_ambiguous','capture_incomplete',
    'budget_exhausted','stopped','rate_limited','outcome_uncertain','invalid',
    'conflict','forbidden','not_found')),
  payload_json TEXT CHECK (payload_json IS NULL OR json_valid(payload_json)),
  actor_kind TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL
);
CREATE INDEX job_check_activity_opportunity_idx ON job_check_activity(opportunity_id, recorded_at, event_id);
