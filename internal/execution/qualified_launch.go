package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type qualifiedRuntimeLaunchVerifier interface {
	VerifiedExecutionLaunch(context.Context, taskgoal.Execution) (codexlaunch.VerifiedExecutable, error)
}

type qualifiedRunCodexFactory interface {
	StartForQualifiedRun(context.Context, string, codexconfig.Config, string, string, string, codex.AuthorizationCeiling, codexlaunch.VerifiedExecutable, executionCheckpoint, *rejoinExecution) (CodexSession, error)
}

func (f DefaultCodexFactory) StartForQualifiedRun(ctx context.Context, runID string, selected codexconfig.Config, model, reasoning, tier string, ceiling codex.AuthorizationCeiling, launch codexlaunch.VerifiedExecutable, checkpoint executionCheckpoint, rejoin *rejoinExecution) (CodexSession, error) {
	if err := validateCheckpointTaskBinding(checkpoint); err != nil {
		return nil, err
	}
	if launch == nil {
		return nil, errors.New("qualified native launch identity unavailable")
	}
	// Keep the stored backend path/fingerprint intact. The launch capability is
	// transient, and cannot be reconstructed from that mutable path on restart.
	f.Config.VerifiedExecutable = launch
	f = f.withTaskCheckpoint(checkpoint)
	if f.ContinuityEnabled() || rejoin != nil {
		return f.StartForBoundRun(ctx, runID, selected, model, reasoning, tier, ceiling, checkpoint, rejoin)
	}
	return f.StartForRuntimeSelection(ctx, selected, model, reasoning, tier, ceiling)
}

// Recovery reads the immutable Execution, not an inferred authority from the
// checkpoint. This gate runs before opening the old transport or recording any
// observations, and ExecuteRun repeats the live qualification check at rejoin.
func (c *Coordinator) qualifiedRecoveryExecution(ctx context.Context, current run.Run, cp executionCheckpoint, binding appserver.Binding) (*taskgoal.Execution, error) {
	if current.ExecutionProposalID == "" {
		return nil, nil
	}
	if c.Executions == nil {
		return nil, errors.New("immutable recovery Execution unavailable")
	}
	value, err := c.Executions.Get(ctx, current.ExecutionProposalID)
	if err != nil {
		return nil, err
	}
	phaseRunID := value.RunID
	if current.Purpose == run.PurposeVerification {
		phaseRunID = value.VerificationRunID
	}
	if (current.Purpose != run.PurposeImplementation && current.Purpose != run.PurposeVerification) ||
		value.ID != current.ExecutionProposalID || value.Status != "CONSUMED" || value.Outcome != "RUNNING" ||
		phaseRunID != current.ID || value.TaskID != current.TaskID || value.ProjectID != current.ProjectID ||
		current.OperationKind != run.OperationExecution || current.OperationID != value.ID ||
		cp.Task.ID != value.TaskID || cp.Task.ProjectID != value.ProjectID || cp.Task.FeatureAreaID != current.FeatureAreaID ||
		cp.Task.CurrentDefinitionRevisionID != value.TaskDefinitionRevisionID || value.TaskDefinitionRevisionID == "" ||
		!reflect.DeepEqual(cp.BackendSelection, value.BackendSelection) {
		return nil, fmt.Errorf("immutable recovery Execution differs from Run/checkpoint: %w", runtimeplatform.ErrIdentityMismatch)
	}
	if !runtimeplatform.QualifiedExecutorIdentity(value.RuntimeAdapterIdentity, value.RuntimeProtocolIdentity) {
		return nil, nil
	}
	verifier, ok := c.QualifiedRuntime.(qualifiedRuntimeLaunchVerifier)
	if !ok {
		return nil, errors.New("qualified native launch verifier unavailable")
	}
	launch, err := verifier.VerifiedExecutionLaunch(ctx, value)
	if err != nil {
		return nil, err
	}
	if launch == nil {
		return nil, errors.New("qualified native launch identity unavailable")
	}
	path, err := launch.Executable(ctx)
	if err != nil {
		return nil, err
	}
	if path == "" || path != binding.ExecutablePath {
		return nil, fmt.Errorf("recovered App Server differs from qualified native executable: %w", runtimeplatform.ErrIdentityMismatch)
	}
	return &value, nil
}
