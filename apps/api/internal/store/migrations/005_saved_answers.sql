CREATE TABLE saved_answers (
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  context_note TEXT NOT NULL DEFAULT '',
  current_version INTEGER NOT NULL CHECK (current_version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(actor_id, request_key)
);
CREATE TABLE saved_answer_scope_tags (
  answer_id TEXT NOT NULL REFERENCES saved_answers(id),
  tag TEXT NOT NULL,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  PRIMARY KEY (answer_id, tag)
);
CREATE TABLE saved_answer_versions (
  answer_id TEXT NOT NULL REFERENCES saved_answers(id),
  version INTEGER NOT NULL CHECK (version > 0),
  text TEXT NOT NULL,
  text_sha256 TEXT NOT NULL,
  approved_at TEXT NOT NULL,
  approved_by_kind TEXT NOT NULL,
  approved_by_id TEXT NOT NULL,
  approval_request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  change_note TEXT NOT NULL DEFAULT '',
  source_refs_json TEXT,
  supersedes INTEGER,
  PRIMARY KEY (answer_id, version),
  UNIQUE(answer_id, approval_request_key)
);
CREATE INDEX saved_answers_created ON saved_answers(created_at, id);
CREATE INDEX saved_answer_versions_answer ON saved_answer_versions(answer_id, version);
