package sqlite

// Existing rows remain source-unknown. They are never relabeled as a local
// operator and no historical AI Session is inferred from unrelated objects.
const decisionActorSourceMigrationSQL = `
ALTER TABLE decision_resolution_receipts ADD COLUMN actor_kind TEXT;
ALTER TABLE decision_resolution_receipts ADD COLUMN actor_id TEXT;
CREATE INDEX decision_resolution_actor_request ON decision_resolution_receipts(project_id,actor_kind,actor_id,request_id);
`
