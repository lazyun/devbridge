package sqlite

const conversationModelMigrationSQL = `
ALTER TABLE agent_conversations ADD COLUMN initial_model TEXT;
ALTER TABLE agent_conversations ADD COLUMN initial_model_source_run_id TEXT;
ALTER TABLE agent_conversations ADD COLUMN initial_model_source_thread_id TEXT;
ALTER TABLE agent_conversations ADD COLUMN initial_model_evidence TEXT;
`
