CREATE TABLE opportunity_material_versions (
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  version INTEGER NOT NULL CHECK (version > 0),
  pack_id TEXT NOT NULL REFERENCES application_packs(id),
  check_id TEXT NOT NULL REFERENCES job_checks(id),
  question_set_sha256 TEXT NOT NULL CHECK (length(question_set_sha256) = 64),
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision >= 1),
  profile_revision INTEGER NOT NULL CHECK (profile_revision >= 1),
  workflow_revision INTEGER NOT NULL CHECK (workflow_revision >= 0),
  origin TEXT NOT NULL CHECK (origin IN ('prepared','direct_edit','rewrite')),
  rewrite_of INTEGER CHECK (rewrite_of IS NULL OR rewrite_of > 0),
  answers_json TEXT NOT NULL CHECK (json_valid(answers_json)),
  readiness_json TEXT NOT NULL CHECK (json_valid(readiness_json)),
  source_shas_json TEXT NOT NULL CHECK (json_valid(source_shas_json)),
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL CHECK (length(request_sha256) = 64),
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (opportunity_id, version),
  UNIQUE (opportunity_id, request_key)
);
CREATE INDEX material_versions_pack_idx ON opportunity_material_versions(pack_id);
