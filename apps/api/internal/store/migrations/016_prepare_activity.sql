CREATE TABLE prepare_activity (
  event_id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  check_id TEXT REFERENCES job_checks(id),
  kind TEXT NOT NULL CHECK (length(trim(kind)) BETWEEN 1 AND 64),
  outcome TEXT CHECK (outcome IS NULL OR length(trim(outcome)) BETWEEN 1 AND 64),
  payload_json TEXT CHECK (payload_json IS NULL OR json_valid(payload_json)),
  actor_kind TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL
);
CREATE INDEX prepare_activity_opportunity_idx ON prepare_activity(opportunity_id, recorded_at, event_id);
