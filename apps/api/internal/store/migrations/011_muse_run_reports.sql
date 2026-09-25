CREATE TABLE muse_run_reports (
  run_ref TEXT NOT NULL PRIMARY KEY,
  round_id TEXT NOT NULL DEFAULT '',
  tier TEXT NOT NULL CHECK (tier IN ('contributor','standard')),
  outcome TEXT NOT NULL CHECK (outcome IN ('completed','stopped','failed','crashed','expired')),
  detail TEXT NOT NULL DEFAULT '',
  saved_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(saved_refs_json)),
  classify_errors_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(classify_errors_json)),
  updated_at TEXT NOT NULL
);
