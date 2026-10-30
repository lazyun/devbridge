package sqlite

const conversationBackendMigrationSQL = `
ALTER TABLE agent_conversations ADD COLUMN backend_selection_json TEXT;
ALTER TABLE agent_conversations ADD COLUMN backend_selection_source_run_id TEXT;
ALTER TABLE agent_conversations ADD COLUMN backend_selection_source_thread_id TEXT;
ALTER TABLE agent_conversations ADD COLUMN backend_selection_evidence TEXT;
ALTER TABLE reviews ADD COLUMN backend_selection_json TEXT;
CREATE TRIGGER review_backend_selection_immutable BEFORE UPDATE OF backend_selection_json ON reviews BEGIN SELECT RAISE(ABORT,'Review backend selection is immutable'); END;
`
