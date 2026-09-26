-- D2: one active identity per verified source URL. Create/patch already
-- map this index's conflicts onto the re-sight reconcile.
CREATE UNIQUE INDEX IF NOT EXISTS opportunities_source_url_active
  ON opportunities(source_url)
  WHERE source_url IS NOT NULL AND source_url <> '' AND archived_at IS NULL;
