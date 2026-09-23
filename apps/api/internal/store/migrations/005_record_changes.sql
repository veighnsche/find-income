-- Explicit AUTOINCREMENT keeps published cursors stable through VACUUM and
-- prevents reuse after deletion. New T08 events are never deleted or updated.
CREATE TABLE record_changes (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  audit_id TEXT NOT NULL UNIQUE REFERENCES audit_changes(id),
  entity_kind TEXT NOT NULL CHECK (entity_kind IN ('company', 'opportunity')),
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  revision_before INTEGER,
  revision_after INTEGER,
  occurred_at TEXT NOT NULL,
  snapshot_state TEXT NOT NULL CHECK (snapshot_state IN ('captured', 'unavailable_historical')),
  snapshot_json TEXT CHECK (snapshot_json IS NULL OR json_valid(snapshot_json)),
  CHECK (snapshot_state = 'unavailable_historical' OR snapshot_json IS NOT NULL)
);

CREATE INDEX record_changes_entity_idx ON record_changes(entity_kind, entity_id, sequence);

-- Old audit rows have no immutable source/compensation snapshot. Preserve
-- their metadata in existing rowid order, without claiming wall-clock order
-- or inventing historical content.
INSERT INTO record_changes
  (audit_id,entity_kind,entity_id,operation,actor_kind,actor_id,
   revision_before,revision_after,occurred_at,snapshot_state,snapshot_json)
SELECT id,entity_kind,entity_id,operation,actor_kind,actor_id,
  revision_before,revision_after,occurred_at,'unavailable_historical',NULL
FROM audit_changes WHERE entity_kind IN ('company','opportunity') ORDER BY rowid;

CREATE TRIGGER record_changes_from_audit AFTER INSERT ON audit_changes
WHEN NEW.entity_kind IN ('company', 'opportunity')
BEGIN
  INSERT INTO record_changes
    (audit_id,entity_kind,entity_id,operation,actor_kind,actor_id,
     revision_before,revision_after,occurred_at,snapshot_state,snapshot_json)
  VALUES (
    NEW.id, NEW.entity_kind, NEW.entity_id, NEW.operation, NEW.actor_kind, NEW.actor_id,
    NEW.revision_before, NEW.revision_after, NEW.occurred_at, 'captured',
    CASE NEW.entity_kind
      WHEN 'company' THEN (
        SELECT json_object(
          'id',id,'name',name,'website',website,'notes',notes,
          'archivedAt',archived_at,'revision',revision,
          'createdAt',created_at,'updatedAt',updated_at)
        FROM companies WHERE id=NEW.entity_id)
      WHEN 'opportunity' THEN (
        SELECT json_object(
          'id',o.id,'companyId',o.company_id,'title',o.title,'kind',o.kind,
          'sourceUrl',o.source_url,'originalText',o.original_text,'stage',o.stage,
          'workPattern',o.work_pattern,'locationText',o.location_text,
          'postedOn',o.posted_on,'deadlineOn',o.deadline_on,'archivedAt',o.archived_at,
          'revision',o.revision,'createdAt',o.created_at,'updatedAt',o.updated_at,
          'compensation',json_object(
            'currency',c.currency,'minAmountCents',c.min_amount_cents,
            'maxAmountCents',c.max_amount_cents,'period',c.period,
            'referenceHours',c.reference_hours,'basis',c.basis,
            'benefitsText',c.benefits_text))
        FROM opportunities o LEFT JOIN compensation c ON c.opportunity_id=o.id
        WHERE o.id=NEW.entity_id)
    END
  );
END;

CREATE TRIGGER record_changes_no_update BEFORE UPDATE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record changes are immutable'); END;

CREATE TRIGGER record_changes_no_delete BEFORE DELETE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record changes are immutable'); END;
