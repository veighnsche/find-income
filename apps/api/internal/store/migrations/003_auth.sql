CREATE TABLE administrator (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  password_hash TEXT NOT NULL,
  credential_version INTEGER NOT NULL CHECK (credential_version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,
  administrator_id INTEGER NOT NULL DEFAULT 1 REFERENCES administrator(singleton),
  csrf_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_seen_at TEXT
);

CREATE INDEX auth_sessions_expiry_idx ON auth_sessions(expires_at);

CREATE TABLE agent_credentials (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
  token_hash TEXT NOT NULL UNIQUE,
  scopes_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_used_at TEXT,
  created_by_admin INTEGER NOT NULL DEFAULT 1 REFERENCES administrator(singleton)
);

CREATE INDEX agent_credentials_expiry_idx ON agent_credentials(expires_at);
