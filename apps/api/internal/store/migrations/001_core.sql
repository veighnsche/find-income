CREATE TABLE preferences_versions (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  preferred_location TEXT NOT NULL,
  allow_remote INTEGER NOT NULL CHECK (allow_remote IN (0, 1)),
  allow_hybrid INTEGER NOT NULL CHECK (allow_hybrid IN (0, 1)),
  target_hours INTEGER NOT NULL CHECK (target_hours BETWEEN 1 AND 168),
  min_monthly_base_cents INTEGER NOT NULL CHECK (min_monthly_base_cents >= 0),
  salary_currency TEXT NOT NULL,
  require_backend_platform INTEGER NOT NULL CHECK (require_backend_platform IN (0, 1)),
  exclude_frontend_duties INTEGER NOT NULL CHECK (exclude_frontend_duties IN (0, 1)),
  exclude_php_focused INTEGER NOT NULL CHECK (exclude_php_focused IN (0, 1)),
  timezone TEXT NOT NULL,
  created_at TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL
);

CREATE TABLE preferences_current (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  version INTEGER NOT NULL REFERENCES preferences_versions(version)
);

CREATE TABLE companies (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  website TEXT,
  notes TEXT NOT NULL DEFAULT '',
  archived_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE opportunities (
  id TEXT PRIMARY KEY,
  company_id TEXT NOT NULL REFERENCES companies(id),
  title TEXT NOT NULL CHECK (length(trim(title)) > 0),
  kind TEXT NOT NULL CHECK (kind IN ('employment', 'project')),
  source_url TEXT,
  original_text TEXT NOT NULL DEFAULT '',
  stage TEXT NOT NULL,
  work_pattern TEXT NOT NULL DEFAULT 'unknown',
  location_text TEXT NOT NULL DEFAULT '',
  posted_on TEXT,
  deadline_on TEXT,
  archived_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE compensation (
  opportunity_id TEXT PRIMARY KEY REFERENCES opportunities(id),
  currency TEXT NOT NULL,
  min_amount_cents INTEGER CHECK (min_amount_cents >= 0),
  max_amount_cents INTEGER CHECK (max_amount_cents >= min_amount_cents),
  period TEXT NOT NULL CHECK (period IN ('month', 'year', 'hour', 'project', 'unknown')),
  reference_hours INTEGER CHECK (reference_hours BETWEEN 1 AND 168),
  basis TEXT NOT NULL CHECK (basis IN ('base', 'inclusive', 'unknown')),
  benefits_text TEXT NOT NULL DEFAULT '',
  actual_monthly_base_cents INTEGER CHECK (actual_monthly_base_cents >= 0),
  actual_hours INTEGER CHECK (actual_hours BETWEEN 1 AND 168),
  actual_confirmed INTEGER NOT NULL DEFAULT 0 CHECK (actual_confirmed IN (0, 1)),
  CHECK (max_amount_cents IS NULL OR min_amount_cents IS NOT NULL),
  CHECK ((actual_monthly_base_cents IS NULL AND actual_hours IS NULL AND actual_confirmed = 0)
      OR (actual_monthly_base_cents IS NOT NULL AND actual_hours IS NOT NULL))
);

CREATE TABLE evidence (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  criterion TEXT NOT NULL,
  observed_value TEXT NOT NULL,
  confirmation_state TEXT NOT NULL CHECK (confirmation_state IN ('confirmed', 'unknown', 'conflicting')),
  source_kind TEXT NOT NULL,
  source_url TEXT,
  source_excerpt TEXT,
  source_contact_text TEXT,
  observed_at TEXT NOT NULL,
  supersedes_id TEXT REFERENCES evidence(id),
  created_at TEXT NOT NULL
);

CREATE TABLE qualification_evaluations (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
  opportunity_revision INTEGER NOT NULL CHECK (opportunity_revision > 0),
  preferences_version INTEGER NOT NULL REFERENCES preferences_versions(version),
  overall_state TEXT NOT NULL CHECK (overall_state IN ('qualified', 'unsuitable', 'unresolved', 'needs_requalification')),
  criterion_results_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE actions (
  id TEXT PRIMARY KEY,
  opportunity_id TEXT REFERENCES opportunities(id),
  description TEXT NOT NULL CHECK (length(trim(description)) > 0),
  due_date TEXT,
  due_at TEXT,
  due_timezone TEXT,
  status TEXT NOT NULL CHECK (status IN ('open', 'completed', 'cancelled')),
  completed_at TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((due_date IS NOT NULL AND due_at IS NULL AND due_timezone IS NULL)
      OR (due_date IS NULL AND due_at IS NOT NULL AND due_timezone IS NOT NULL))
);

CREATE TABLE audit_changes (
  id TEXT PRIMARY KEY,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  entity_kind TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  revision_before INTEGER,
  revision_after INTEGER,
  occurred_at TEXT NOT NULL
);
