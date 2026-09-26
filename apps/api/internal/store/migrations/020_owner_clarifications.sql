-- K4: distinct owner clarifications for genuinely unknown personal facts.
-- Separate from employer questions; the owner's exact answer becomes
-- verified job-scoped context and resumes only the affected work.
CREATE TABLE owner_clarifications (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  check_id TEXT NOT NULL REFERENCES job_checks(id),
  origin TEXT NOT NULL DEFAULT 'owner_clarification',
  requirement_statement TEXT NOT NULL,
  requirement_capture_id TEXT NOT NULL,
  span_start INTEGER NOT NULL,
  span_end INTEGER NOT NULL,
  prompt TEXT NOT NULL,
  affected_work_json TEXT NOT NULL CHECK (json_valid(affected_work_json)),
  status TEXT NOT NULL DEFAULT 'open',
  answer_text TEXT NOT NULL DEFAULT '',
  answered_at TEXT NOT NULL DEFAULT '',
  answered_by_kind TEXT NOT NULL DEFAULT '',
  answered_by_id TEXT NOT NULL DEFAULT '',
  request_key TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  answer_request_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(opportunity_id, request_key)
);
CREATE INDEX owner_clarifications_opportunity_idx ON owner_clarifications(opportunity_id, id);
