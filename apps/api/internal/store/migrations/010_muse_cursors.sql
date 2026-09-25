CREATE TABLE muse_cursors (
  run_ref TEXT NOT NULL PRIMARY KEY,
  tier TEXT NOT NULL CHECK (tier IN ('contributor','standard')),
  last_saved_receipt TEXT NOT NULL DEFAULT '',
  saved_count INTEGER NOT NULL DEFAULT 0 CHECK (saved_count >= 0),
  saved_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(saved_refs_json)),
  updated_at TEXT NOT NULL
);
