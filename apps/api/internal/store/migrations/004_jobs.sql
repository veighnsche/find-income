CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (length(trim(kind)) BETWEEN 1 AND 80),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_sha256 TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  request_sha256 TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
  available_at TEXT NOT NULL,
  lease_token TEXT,
  lease_owner TEXT,
  lease_until TEXT,
  result_ref TEXT,
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  last_error_code TEXT,
  last_error_message TEXT,
  cancelled_by_kind TEXT,
  cancelled_by_id TEXT,
  cancelled_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (actor_kind, actor_id, idempotency_key),
  CHECK ((state = 'running' AND lease_token IS NOT NULL AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
      OR (state <> 'running' AND lease_token IS NULL AND lease_owner IS NULL AND lease_until IS NULL)),
  CHECK ((state = 'succeeded' AND completed_at IS NOT NULL)
      OR (state <> 'succeeded' AND result_ref IS NULL AND result_json IS NULL)),
  CHECK ((state = 'cancelled' AND cancelled_at IS NOT NULL AND cancelled_by_kind IS NOT NULL AND cancelled_by_id IS NOT NULL)
      OR (state <> 'cancelled' AND cancelled_at IS NULL AND cancelled_by_kind IS NULL AND cancelled_by_id IS NULL))
);

CREATE TABLE job_attempts (
  job_id TEXT NOT NULL REFERENCES jobs(id),
  attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
  lease_token TEXT NOT NULL UNIQUE,
  worker_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  lease_until TEXT NOT NULL,
  finished_at TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('running', 'succeeded', 'retry', 'failed', 'expired', 'cancelled')),
  error_code TEXT,
  error_message TEXT,
  retry_not_before TEXT,
  result_ref TEXT,
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  PRIMARY KEY (job_id, attempt_no),
  CHECK ((outcome = 'running' AND finished_at IS NULL)
      OR (outcome <> 'running' AND finished_at IS NOT NULL))
);

CREATE INDEX jobs_ready_idx ON jobs(state, available_at, created_at, id);
CREATE INDEX jobs_expired_lease_idx ON jobs(state, lease_until);
CREATE INDEX job_attempts_job_idx ON job_attempts(job_id, attempt_no);
