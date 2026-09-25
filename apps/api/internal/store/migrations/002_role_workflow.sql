CREATE TABLE IF NOT EXISTS role_workflow (
  opportunity_id TEXT PRIMARY KEY,
  stage TEXT NOT NULL,
  revision INTEGER NOT NULL,
  blocked_reason TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
