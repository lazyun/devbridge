package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

const noRunCurrentSnapshotSchemaID = runtimeselection.SnapshotSchemaID

// The current authority is the exact immutable revision binding and its bound
// qualification. Historical Review fields, live model inventory and process
// constants cannot fill an absent source or silently upgrade legacy receipts.
func (s *WorkflowProgressionStore) resolveCurrentNoRunAuthorityTx(ctx context.Context, tx *sql.Tx, in wp.PrepareSuccessorExecution) (runtimepreparation.Plan, error) {
	authority, err := readCurrentRuntimeSelectionTx(ctx, tx, runtimeselection.Owner{
		ProjectID: in.ProjectID, TaskID: in.TaskID,
		TaskDefinitionRevisionID: in.ExpectedDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION",
	})
	if err != nil {
		return runtimepreparation.Plan{}, err
	}
	v := authority.Values
	return runtimepreparation.Plan{
		BackendIdentity: v.BackendIdentity, BackendConfigFingerprint: v.BackendConfigFingerprint,
		BackendSource: v.BackendSource, BackendKind: v.BackendKind,
		Model: v.Model, ReasoningEffort: v.ReasoningEffort, CodexFastMode: v.CodexFastMode, ServiceTier: v.ServiceTier, AgentID: v.AgentID, AgentConfigFingerprint: v.AgentConfigFingerprint, ExecutionPolicy: v.ExecutionPolicy,
		RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity,
		AuthoritySnapshotHash: authority.AuthoritySnapshotHash,
	}, nil
}
