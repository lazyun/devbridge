package sqlite

const responseDispatchV80SQL = `
CREATE TABLE runtime_response_dispatch_attempts (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id),
 runtime_request_id TEXT NOT NULL REFERENCES runtime_user_decisions(runtime_request_id),
 user_decision_id TEXT NOT NULL REFERENCES runtime_user_decisions(id),
 connection_id TEXT NOT NULL,
 backend_request_id TEXT NOT NULL,
 command_request_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 attempt_json TEXT NOT NULL CHECK(json_valid(attempt_json)),
 started_at TEXT NOT NULL,
 FOREIGN KEY(connection_id,backend_request_id) REFERENCES codex_server_requests(connection_id,request_id)
);
CREATE INDEX runtime_response_dispatch_request ON runtime_response_dispatch_attempts(runtime_request_id,started_at);
CREATE TRIGGER runtime_response_dispatch_no_update BEFORE UPDATE ON runtime_response_dispatch_attempts BEGIN SELECT RAISE(ABORT,'response dispatch intent is immutable'); END;
CREATE TRIGGER runtime_response_dispatch_no_delete BEFORE DELETE ON runtime_response_dispatch_attempts BEGIN SELECT RAISE(ABORT,'response dispatch intent is immutable'); END;
CREATE TABLE runtime_response_evidence (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id),
 runtime_request_id TEXT NOT NULL REFERENCES runtime_user_decisions(runtime_request_id),
 connection_id TEXT NOT NULL,
 backend_request_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('EXACT_PENDING_REPLAY','PRE_EFFECT_REJECTED','SERVER_REQUEST_RESOLVED','TRANSPORT_OUTCOME_UNKNOWN')),
 evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json)),
 observed_at TEXT NOT NULL,
 FOREIGN KEY(connection_id,backend_request_id) REFERENCES codex_server_requests(connection_id,request_id)
);
CREATE TRIGGER runtime_response_evidence_no_update BEFORE UPDATE ON runtime_response_evidence BEGIN SELECT RAISE(ABORT,'response evidence is immutable'); END;
CREATE TRIGGER runtime_response_evidence_no_delete BEFORE DELETE ON runtime_response_evidence BEGIN SELECT RAISE(ABORT,'response evidence is immutable'); END;
`
