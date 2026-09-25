CREATE TABLE answer_match_runs (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  check_id TEXT NOT NULL REFERENCES job_checks(id),
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  question_set_sha256 TEXT NOT NULL CHECK (length(question_set_sha256) = 64),
  answer_catalog_digest TEXT NOT NULL CHECK (length(answer_catalog_digest) = 64),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (opportunity_id, check_id, request_key)
);
CREATE INDEX answer_match_runs_opportunity_idx ON answer_match_runs(opportunity_id, created_at, id);

CREATE TABLE answer_matches (
  run_id TEXT NOT NULL REFERENCES answer_match_runs(id),
  question_id TEXT NOT NULL REFERENCES job_check_questions(id),
  question_text_sha256 TEXT NOT NULL CHECK (length(question_text_sha256) = 64),
  candidate_set_hash TEXT NOT NULL CHECK (length(candidate_set_hash) = 64),
  answer_id TEXT REFERENCES saved_answers(id),
  answer_version INTEGER CHECK (answer_version IS NULL OR answer_version > 0),
  answer_text_sha256 TEXT CHECK (answer_text_sha256 IS NULL OR length(answer_text_sha256) = 64),
  none_fits INTEGER NOT NULL CHECK (none_fits IN (0,1)),
  deterministic INTEGER NOT NULL DEFAULT 0 CHECK (deterministic IN (0,1)),
  confidence REAL NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
  jev_attempt_id TEXT REFERENCES jev_attempts(id),
  matched_at TEXT NOT NULL,
  PRIMARY KEY (run_id, question_id),
  CHECK ((none_fits = 1 AND answer_id IS NULL AND answer_version IS NULL AND answer_text_sha256 IS NULL) OR
         (none_fits = 0 AND answer_id IS NOT NULL AND answer_version IS NOT NULL AND answer_text_sha256 IS NOT NULL)),
  CHECK ((deterministic = 1 AND jev_attempt_id IS NULL) OR (deterministic = 0 AND jev_attempt_id IS NOT NULL))
);
CREATE INDEX answer_matches_question_idx ON answer_matches(question_id);
