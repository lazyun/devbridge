package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
)

type inspectionRecoveryContinuationRunner interface {
	ContinueInspection(context.Context, probeexecution.InspectionContinuationInput) (contextprobe.Probe, error)
}

func (f *Facade) ContinueInspectionRecoveryForSession(ctx context.Context, sessionID string, in probeexecution.InspectionContinuationInput) (contextprobe.Probe, error) {
	if in.ActorID != "" {
		return contextprobe.Probe{}, invalid("Inspection continuation actor is server-derived")
	}
	if f.deps.RecoveryStatus == nil {
		return contextprobe.Probe{}, invalid("Recovery status service unavailable")
	}
	record, err := f.deps.RecoveryStatus.Read(ctx, in.RecoveryStatusID)
	if err != nil {
		return contextprobe.Probe{}, MapError(err)
	}
	if _, err = f.recoverySource(ctx, sessionID, record.Spec.Source); err != nil {
		return contextprobe.Probe{}, err
	}
	if record.Spec.OwnerKind != "PROJECT_INSPECTION" || record.State != "RESOLVED" {
		return contextprobe.Probe{}, MapError(recoverystatus.ErrConflict)
	}
	in.ActorID = "session:" + sessionID
	return f.continueInspectionRecovery(ctx, in)
}
func (f *Facade) continueInspectionRecovery(ctx context.Context, in probeexecution.InspectionContinuationInput) (contextprobe.Probe, error) {
	runner, ok := f.deps.ContextProbeRunner.(inspectionRecoveryContinuationRunner)
	if !ok {
		return contextprobe.Probe{}, invalid("Inspection continuation unavailable")
	}
	result, err := runner.ContinueInspection(ctx, in)
	if err != nil {
		return result, MapError(err)
	}
	return result, nil
}
func (f *Facade) ContinueInspectionRecoveryForOperator(ctx context.Context, projectID string, in probeexecution.InspectionContinuationInput) (contextprobe.Probe, error) {
	if in.ActorID != "" {
		return contextprobe.Probe{}, invalid("Inspection continuation actor is server-derived")
	}
	ctx, reader, actor, err := f.recoveryOperator(ctx, projectID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if reader.deps.RecoveryStatus == nil {
		return contextprobe.Probe{}, invalid("Recovery status service unavailable")
	}
	record, err := reader.deps.RecoveryStatus.Read(ctx, in.RecoveryStatusID)
	if err != nil {
		return contextprobe.Probe{}, MapError(err)
	}
	if _, err = reader.recoverySource(ctx, "", record.Spec.Source); err != nil {
		return contextprobe.Probe{}, err
	}
	if record.Spec.OwnerKind != "PROJECT_INSPECTION" || record.State != "RESOLVED" {
		return contextprobe.Probe{}, MapError(recoverystatus.ErrConflict)
	}
	in.ActorID = actor
	return reader.continueInspectionRecovery(ctx, in)
}
