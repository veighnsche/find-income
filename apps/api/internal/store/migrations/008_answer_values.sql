CREATE TABLE answer_values (
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  check_id TEXT NOT NULL REFERENCES job_checks(id),
  question_id TEXT PRIMARY KEY REFERENCES job_check_questions(id),
  question_text_sha256 TEXT NOT NULL CHECK (length(question_text_sha256) = 64),
  required TEXT NOT NULL CHECK (required IN ('required','optional','unknown')),
  version INTEGER NOT NULL CHECK (version >= 1),
  state TEXT NOT NULL CHECK (state IN ('answered','blank')),
  text TEXT NOT NULL DEFAULT '',
  text_sha256 TEXT CHECK (text_sha256 IS NULL OR length(text_sha256) = 64),
  origin TEXT NOT NULL CHECK (origin IN ('jev_suggestion','owner_written','owner_edited','carried_blank')),
  match_run_id TEXT REFERENCES answer_match_runs(id),
  match_answer_id TEXT REFERENCES saved_answers(id),
  match_answer_version INTEGER CHECK (match_answer_version IS NULL OR match_answer_version > 0),
  match_answer_text_sha256 TEXT CHECK (match_answer_text_sha256 IS NULL OR length(match_answer_text_sha256) = 64),
  edited_at TEXT NOT NULL,
  edited_by_kind TEXT NOT NULL,
  edited_by_id TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((match_answer_id IS NULL AND match_answer_version IS NULL AND match_answer_text_sha256 IS NULL) OR
         (match_answer_id IS NOT NULL AND match_answer_version IS NOT NULL AND match_answer_text_sha256 IS NOT NULL)),
  CHECK ((state = 'blank' AND text = '' AND text_sha256 IS NULL) OR
         (state = 'answered' AND text <> '' AND text_sha256 IS NOT NULL))
);
CREATE INDEX answer_values_opportunity_idx ON answer_values(opportunity_id, check_id);
