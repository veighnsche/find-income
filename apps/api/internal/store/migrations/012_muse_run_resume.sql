CREATE TABLE muse_run_resume (
  run_ref TEXT NOT NULL PRIMARY KEY,
  round_id TEXT NOT NULL DEFAULT '',
  criteria_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(criteria_json)),
  bounds_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(bounds_json)),
  created_at TEXT NOT NULL
);
