package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
)

// completeFormalConfirmationTx is part of the business writer, after its exact
// immutable receipt (and, for a start, source) has been inserted. No public
// completion journal can survive a failed business/receipt transaction.
func completeFormalConfirmationTx(ctx context.Context, tx *sql.Tx, session, intentID, requestID, kind, taskID, revisionID, executionID string) error {
	intent, err := confirmationRead(ctx, tx, session, intentID)
	if err != nil {
		return err
	}
	if intent.Commit == nil || intent.Commit.RequestID != requestID || len(intent.Steps) != 1 || intent.Steps[0].Kind != kind {
		return fe.ErrConflict
	}
	step := intent.Steps[0]
	if step.RequestID != "confirmation_"+fe.Hash([]string{intentID, kind})[:32] || step.RunID != "" || step.WorkflowID != "" || (step.State != "PENDING" && step.State != "UNKNOWN") {
		return fe.ErrConflict
	}
	step.ProjectID, step.TaskID, step.RevisionID, step.ExecutionID = intent.ProjectID, taskID, revisionID, executionID
	if intent.Content.Execution == nil {
		return fe.ErrConflict
	}
	step.FeatureAreaID = intent.Content.Execution.FeatureAreaID
	step.State, step.Effect = "COMPLETED", "COMMAND_COMPLETED"
	if kind == "START_IMPLEMENTATION" {
		step.Effect = "PREPARATION_READY"
	}
	step.Failure, step.Code, step.Message = nil, "", ""
	intent.State, intent.Steps = "COMPLETED", []confirmation.Step{step}
	return confirmationSave(ctx, tx, intent)
}

// Fresh formal writers compare the Core-frozen prepare context inside their
// business transaction. Historical receipts never call this live guard.
func validateFormalConfirmationContextTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	b := intent.Content.Execution
	if b == nil || b.PreparedTopologyRevision <= 0 || b.PreparedDevelopmentContextHash == "" {
		return confirmation.NewFailure("CONFIRMATION_SELECTION_CONFLICT", "CORE", "BUSINESS_STEP", "The unfinished formal confirmation has no original Project context fence; prepare a new confirmation.", confirmation.PrepareNewVersion, nil)
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, intent.ProjectID)
	if err != nil {
		return err
	}
	if topology != b.PreparedTopologyRevision {
		return confirmation.NewFailure("PROJECT_TOPOLOGY_CHANGED", "CORE", "BUSINESS_STEP", "The Project repository topology changed after this formal confirmation was prepared.", confirmation.PrepareNewVersion, nil)
	}
	if development != b.PreparedDevelopmentContextHash {
		return confirmation.NewFailure("DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED", "CORE", "BUSINESS_STEP", "The Project development branch configuration changed after this formal confirmation was prepared.", confirmation.PrepareNewVersion, nil)
	}
	return nil
}
