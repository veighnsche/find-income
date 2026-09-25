-- Versioned reason catalog (lane C, C2): one authored rubric plus
-- positive, negative and missing-information reason choices per owner-brief
-- version. Rows are immutable: a brief change (a new preferences version)
-- gets a new row that preserves its own requirements snapshot, so earlier
-- catalogs keep classifying against the brief they were authored for.
-- No current-pointer table: the brief version is the key, exactly like
-- preferences_versions. Change-my-search input arrives through the existing
-- steer-shaped Codex-owned records; the authoring call only records that
-- steer message as provenance, never a transcribed form.
CREATE TABLE reason_catalogs (
  profile_version INTEGER PRIMARY KEY REFERENCES preferences_versions(version),
  rubric_version TEXT NOT NULL CHECK (rubric_version LIKE 'criteria-v%'),
  catalog_version TEXT NOT NULL UNIQUE CHECK (catalog_version LIKE 'catalog-v%'),
  rubric_text TEXT NOT NULL CHECK (length(trim(rubric_text)) > 0),
  role_criteria_json TEXT NOT NULL CHECK (json_valid(role_criteria_json)),
  role_criteria_sha256 TEXT NOT NULL CHECK (length(role_criteria_sha256) = 64),
  positive_json TEXT NOT NULL CHECK (json_valid(positive_json)),
  negative_json TEXT NOT NULL CHECK (json_valid(negative_json)),
  missing_information_json TEXT NOT NULL CHECK (json_valid(missing_information_json)),
  steer_run_id TEXT NOT NULL DEFAULT '',
  steer_message_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL
);
