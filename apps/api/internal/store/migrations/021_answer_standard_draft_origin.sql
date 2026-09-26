-- C4/M4 producer: the Prepare turn may save grounded drafts for the
-- required blanks the owner flagged, recorded as standard_draft.
-- SQLite cannot widen a CHECK in place, so rebuild the table with the
-- same shape plus the new origin (and the 018 draft_requested column).
ALTER TABLE answer_values RENAME TO answer_values_legacy;
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
  draft_requested INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL CHECK (origin IN ('jev_suggestion','owner_written','owner_edited','carried_blank','standard_draft')),
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
INSERT INTO answer_values (opportunity_id,check_id,question_id,question_text_sha256,required,version,state,text,text_sha256,draft_requested,origin,match_run_id,match_answer_id,match_answer_version,match_answer_text_sha256,edited_at,edited_by_kind,edited_by_id,updated_at)
  SELECT opportunity_id,check_id,question_id,question_text_sha256,required,version,state,text,text_sha256,draft_requested,origin,match_run_id,match_answer_id,match_answer_version,match_answer_text_sha256,edited_at,edited_by_kind,edited_by_id,updated_at
  FROM answer_values_legacy;
DROP TABLE answer_values_legacy;
CREATE INDEX answer_values_opportunity_idx ON answer_values(opportunity_id, check_id);
