ALTER TABLE job_checks ADD COLUMN requirements_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(requirements_json));
