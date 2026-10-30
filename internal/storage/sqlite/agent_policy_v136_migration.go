package sqlite

// New optional columns preserve every historical row and its frozen hashes.
// An empty value is historical native policy, never cooperative authorization.
const agentPolicyV136SQL = `
ALTER TABLE runtime_scope_plans ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_scope_plans ADD COLUMN execution_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_scope_plans ADD COLUMN agent_config_fingerprint TEXT NOT NULL DEFAULT '';
`
