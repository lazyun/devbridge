package execution_test

import (
	"context"

	"testing"
	"time"

	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type runtimeExecutionLookup struct{ *sqlite.ExecutionProposalStore }

func (s runtimeExecutionLookup) Get(ctx context.Context, id string) (taskgoal.Execution, error) {
	return s.GetByID(ctx, id)
}

func seedOwnedRuntimeExecution(t *testing.T, db *sqlite.DB, taskID, sessionID, id string) executionproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	proposal, _, err := sqlite.NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, sessionID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func startOwnedRuntimeExecution(t *testing.T, ctx context.Context, c *exec.Coordinator, db *sqlite.DB, taskID, sessionID string) (run.Run, error) {
	t.Helper()
	proposal, r, err := sqlite.NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, sessionID, taskID)
	if err != nil {
		return r, err
	}
	proposals := sqlite.NewExecutionProposalStore(db)
	if proposal.Status == executionproposal.StatusAdmitted && r.ID == "" {
		proposal, r, err = proposals.PrepareAdmittedExecution(ctx, proposal.ID, time.Now().UTC())
		if err != nil {
			return r, err
		}
	}
	c.Executions = runtimeExecutionLookup{proposals}
	prepared, err := c.PrepareReservedRun(ctx, r.ID, exec.Instruction{CodexInstruction: proposal.WorkSpec.CodexInstruction, ExecutionScope: proposal.WorkSpec.ExecutionScope})
	if err != nil {
		return prepared.Run, err
	}
	r, err = c.ExecuteRun(ctx, prepared, nil)
	if err == nil {
		// This fixture exercises one runtime operation, then ends its pipeline.
		// The Task stays ACTIVE and cannot be accepted without verification.
		_, err = proposals.Complete(ctx, proposal.ID, executionproposal.OutcomeFailed, "FIXTURE_PIPELINE_STOPPED", "Synthetic pipeline ends after implementation", time.Now().UTC())
	}
	return r, err
}
