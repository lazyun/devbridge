package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/executioncancel"
)

// resolveUniversalCancellation translates identity only. The canonical owner
// protocol still revalidates claim, epoch, domain state and exact backend target.
func (f *Facade) resolveUniversalCancellation(ctx context.Context, in executioncancel.Input) (executioncancel.Input, error) {
	if f.deps.AgentExecutions == nil || in.ExecutionID == "" || in.OwnerType != "" && in.OwnerType != "EXECUTION" {
		return in, nil
	}
	e, err := f.deps.AgentExecutions.Get(ctx, in.ExecutionID)
	if err != nil {
		return in, MapError(err)
	}
	if e.ProjectID != in.ProjectID || string(e.ProvenanceKind) == "LEGACY_UNKNOWN" {
		return in, MapError(executioncancel.ErrRunExecutionMismatch)
	}
	if in.OwnerID != "" && in.OwnerID != e.SemanticOwnerID {
		return in, MapError(executioncancel.ErrRunExecutionMismatch)
	}
	if in.RunID != "" {
		r, err := f.deps.Runs.Get(ctx, in.RunID)
		if err != nil {
			return in, MapError(err)
		}
		if r.AgentExecutionID != e.ID || r.TaskID != e.TaskID || r.ProjectID != e.ProjectID || r.FeatureAreaID != e.FeatureAreaID || string(r.OperationKind) != e.SemanticOwnerKind || r.OperationID != e.SemanticOwnerID {
			return in, MapError(executioncancel.ErrRunExecutionMismatch)
		}
	}
	in.OwnerType = e.SemanticOwnerKind
	in.OwnerID = e.SemanticOwnerID
	if in.OwnerType != "EXECUTION" {
		in.ExecutionID = ""
	}
	return in, nil
}
