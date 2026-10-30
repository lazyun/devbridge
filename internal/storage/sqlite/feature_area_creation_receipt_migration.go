package sqlite

import (
	"context"
	"database/sql"
)

func migrateFeatureAreaCreationReceiptV47(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE feature_area_creation_receipts (
 ai_session_id TEXT NOT NULL, project_id TEXT NOT NULL, command TEXT NOT NULL, request_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64 AND payload_sha256 GLOB '[0-9a-f]*'),
 contract_version TEXT NOT NULL, prepared_json TEXT NOT NULL, feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 task_id TEXT, review_id TEXT, run_id TEXT, review_admitted INTEGER NOT NULL CHECK(review_admitted IN (0,1)),
 failed_step TEXT, error_code TEXT, error_message TEXT,
 PRIMARY KEY(ai_session_id,project_id,command,request_id),
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES feature_areas(id,project_id)
)`)
	return err
}
