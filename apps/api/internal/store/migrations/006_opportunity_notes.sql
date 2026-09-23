-- Personal tracking notes are separate from original vacancy text. Keep 005
-- unchanged so existing database migration digests remain valid. Historical
-- snapshots written before this migration remain exactly as captured.
ALTER TABLE opportunities ADD COLUMN notes TEXT NOT NULL DEFAULT '';

DROP TRIGGER record_changes_from_audit;

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
          'sourceUrl',o.source_url,'originalText',o.original_text,'notes',o.notes,
          'stage',o.stage,'workPattern',o.work_pattern,'locationText',o.location_text,
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
