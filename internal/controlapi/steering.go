package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
)

func (f *Facade) SteerRunForOperator(ctx context.Context, in agentruntime.SteeringInput) (agentruntime.SteeringReceipt, error) {
	if in.SubmittedBy != "" {
		return agentruntime.SteeringReceipt{}, invalid("Steering actor is derived from authenticated caller scope")
	}
	ctx, err := localoperator.BindProject(ctx, in.ProjectID)
	if err != nil {
		return agentruntime.SteeringReceipt{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, in.ProjectID)
	if err != nil {
		return agentruntime.SteeringReceipt{}, err
	}
	v, _, err := reader.scopedRun(ctx, "", in.RunID)
	if err != nil {
		return agentruntime.SteeringReceipt{}, err
	}
	actor, _ := localoperator.FromContext(ctx)
	in.SubmittedBy = "operator:" + actor.PrincipalID()
	return f.steerRun(ctx, in, v)
}

func (f *Facade) steerRun(ctx context.Context, in agentruntime.SteeringInput, v run.Run) (agentruntime.SteeringReceipt, error) {
	if v.ProjectID != in.ProjectID || v.FeatureAreaID != in.FeatureAreaID || v.TaskID != in.TaskID || v.ThreadID != in.ThreadID || v.TurnID != in.TurnID || string(v.OperationKind) != in.OperationKind || v.OperationID != in.OperationID {
		return agentruntime.SteeringReceipt{}, MapError(agentruntime.ErrStale)
	}
	if f.deps.SteeringRuntime == nil {
		return agentruntime.SteeringReceipt{}, MapError(agentruntime.ErrUnsupported)
	}
	r, err := f.deps.SteeringRuntime.Steer(ctx, in)
	if err != nil {
		return r, MapError(err)
	}
	return r, nil
}
