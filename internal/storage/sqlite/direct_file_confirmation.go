package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

// Exact consumption is checked in the same writer transaction as the grant;
// an arbitrary prepared or completed confirmation is never start authority.
func validateDirectFileConfirmationTx(ctx context.Context, tx *sql.Tx, in directfile.StartInput, revision task.DefinitionRevision) error {
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.ConfirmationIntentID)
	if err != nil {
		return err
	}
	b := intent.Content.Execution
	if intent.State != "CONFIRMING" || intent.Purpose != confirmation.Execution || intent.Action != in.Action || intent.Commit == nil || b == nil || b.Action != in.Action || b.TaskID != in.TaskID || b.TaskDefinitionRevisionID != in.TaskDefinitionRevisionID || b.DecisionID != in.DecisionID || b.WorkflowID != in.WorkflowID || b.RuntimeEpoch != in.RuntimeEpoch || b.GrantGeneration != in.GrantGeneration {
		return directFileError(directfile.NotEnabled, "exact consumed Direct File confirmation required")
	}
	stepOK := false
	for _, step := range intent.Steps {
		if step.Kind == in.Action && step.RequestID == in.RequestID && step.State == "UNKNOWN" {
			stepOK = true
		}
	}
	if !stepOK {
		return directFileError(directfile.NotEnabled, "exact active confirmation step required")
	}
	for _, v := range intent.Content.Variants {
		if v.ID == intent.Commit.VariantID && v.ImplementationMode == task.ImplementationDirectFile && v.WorkSpec != nil && v.VerificationSpec != nil && reflect.DeepEqual(*v.WorkSpec, revision.Definition.WorkSpec) && reflect.DeepEqual(*v.VerificationSpec, revision.Definition.VerificationSpec) && reflect.DeepEqual(v.RepositoryTargets, revision.Definition.RepositoryTargets) {
			return nil
		}
	}
	return directFileError(directfile.NotEnabled, "confirmation does not match frozen Direct File definition")
}
func validateDirectFilePreparationConfirmationTx(ctx context.Context, tx *sql.Tx, grant taskFileAuthorization, revision task.DefinitionRevision) error {
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM direct_file_start_receipts WHERE workflow_id=? AND runtime_epoch=? AND generation=? AND action IN ('START_DIRECT_FILE','RECOVER_DIRECT_FILE') ORDER BY created_at DESC LIMIT 1`, grant.WorkflowID, grant.RuntimeEpoch, grant.GrantGeneration).Scan(&payload); err != nil {
		return err
	}
	var in directfile.StartInput
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		return err
	}
	return validateDirectFileConfirmationTx(ctx, tx, in, revision)
}
