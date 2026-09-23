CREATE INDEX opportunities_company_idx ON opportunities(company_id);
CREATE INDEX opportunities_updated_idx ON opportunities(updated_at, id);
CREATE INDEX opportunities_source_url_idx ON opportunities(source_url);
CREATE INDEX evidence_opportunity_criterion_idx ON evidence(opportunity_id, criterion, created_at);
CREATE INDEX evidence_supersedes_idx ON evidence(supersedes_id);
CREATE INDEX qualification_opportunity_idx ON qualification_evaluations(opportunity_id, created_at);
CREATE INDEX actions_status_due_date_idx ON actions(status, due_date);
CREATE INDEX actions_status_due_at_idx ON actions(status, due_at);
CREATE INDEX audit_entity_idx ON audit_changes(entity_kind, entity_id, occurred_at);
