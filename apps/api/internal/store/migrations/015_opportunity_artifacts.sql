CREATE TABLE opportunity_artifacts (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  artifact_type TEXT NOT NULL,
  version INTEGER NOT NULL,
  request_key TEXT NOT NULL,
  content TEXT NOT NULL,
  basis_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(basis_json)),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (opportunity_id, artifact_type, version),
  UNIQUE (opportunity_id, artifact_type, request_key)
);
CREATE INDEX idx_opportunity_artifacts_current ON opportunity_artifacts(opportunity_id, artifact_type, version DESC);
