package sqlite

// No historical backfill: a preexisting Run is not proof of a delivery request.
const runtimeHandoffMigrationSQL = `
CREATE TABLE runtime_handoffs (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 runtime_epoch TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('REGISTERED','PREPARING','ACCEPTED','REJECTED','UNKNOWN')),
 code TEXT NOT NULL DEFAULT '',
 accepted_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX runtime_handoffs_delivery ON runtime_handoffs(runtime_epoch,state,run_id);
`
