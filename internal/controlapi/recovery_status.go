package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

type RecoveryStatusService interface {
	Prepare(context.Context, recoverystatus.Spec) (recoverystatus.Record, error)
	Start(context.Context, string) (recoverystatus.Record, error)
	Read(context.Context, string) (recoverystatus.Record, error)
	LatestForSource(context.Context, string) (recoverystatus.Record, error)
	Resolve(context.Context, recoverystatus.ResolveInput) (recoverystatus.Record, error)
}
type RecoveryStatusResult struct {
	Record         recoverystatus.Record         `json:"record"`
	Projection     recoverystatus.Projection     `json:"projection"`
	StartCommand   *RecoveryStatusCommand        `json:"startCommand,omitempty"`
	ResolveCommand *RecoveryStatusResolveCommand `json:"resolveCommand,omitempty"`
}
type RecoveryStatusCommand struct {
	recoverystatus.Source
	ID                    string `json:"id"`
	RequestID             string `json:"requestId"`
	PayloadHash           string `json:"payloadHash"`
	ExpectedRecoveryEpoch string `json:"expectedRecoveryEpoch"`
}
type RecoveryStatusResolveCommand struct {
	RecoveryStatusCommand
	AssessmentHash string                          `json:"assessmentHash"`
	ReportHash     string                          `json:"reportHash"`
	Purpose        *recoverystatus.PurposeProposal `json:"purpose,omitempty"`
}

func recoveryStatusResult(r recoverystatus.Record, sources ...run.Run) RecoveryStatusResult {
	p := recoverystatus.Projection{Required: r.State != "RESOLVED", Eligibility: r.State == "PREPARED", SourceOperationKind: r.Spec.OperationKind, SourceOperationID: r.Spec.OperationID, SourceRunID: r.Spec.RunID, SourceTurnID: r.Spec.TurnID, RecoveryRunID: r.RecoveryRunID, RecoveryTurnID: r.RecoveryTurnID, State: r.State, SourceResponseDisposition: r.SourceResponseDisposition, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, LastObservedAt: &r.UpdatedAt}
	if r.AnalysisRequired && len(r.Report) == 0 && r.State == "RUNNING" {
		p.State = "ANALYSIS_REQUIRED"
	}
	p.OwnerKind, p.OwnerID, p.ProjectID, p.FeatureAreaID = r.Spec.OwnerKind, r.Spec.OwnerID, r.Spec.ProjectID, r.Spec.FeatureAreaID
	p.OwnerTaskID, p.WorkspaceID, p.RuntimeWorksetHash = r.Spec.TaskID, r.Spec.WorkspaceID, r.Spec.RuntimeWorksetHash
	var ownerFacts recoverystatus.FactualObservation
	if json.Unmarshal(r.Facts, &ownerFacts) == nil {
		p.MemberCount = len(ownerFacts.Workset.Members)
	}
	if len(sources) > 0 && sources[0].RuntimeWorkset != nil {
		p.MemberCount = len(sources[0].RuntimeWorkset.Members)
		p.WorkspaceID = sources[0].RuntimeWorkset.WorkspaceID
		p.SourceIsRecoveryChild = sources[0].Purpose == run.PurposeRecoveryStatus
	}
	if r.State == "RUN_CREATED" && r.RecoveryTurnID == "" {
		// A persisted child without a dispatch intent still needs an explicit
		// caller start. Reading it never starts or replaces the existing Run.
		p.State, p.Eligibility = "PREPARED", true
	}
	var report struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(r.Report, &report)
	p.ReportSummary = report.Summary
	var assessment struct {
		Consistency            string   `json:"consistency"`
		EngineeringDisposition string   `json:"engineeringDisposition"`
		ReasonCodes            []string `json:"reasonCodes"`
	}
	_ = json.Unmarshal(r.Assessment, &assessment)
	p.Consistency, p.EngineeringDisposition, p.ReasonCodes = assessment.Consistency, assessment.EngineeringDisposition, assessment.ReasonCodes
	if r.State == "FAILED" && r.FailureReason == "RECOVERY_PREPARATION_FAILED" {
		p.ReasonCodes = append(p.ReasonCodes, r.FailureReason)
	}
	if r.FailureReason == "RECOVERY_CANCELED" {
		var receipt struct {
			State string `json:"state"`
		}
		if json.Unmarshal(r.Resolution, &receipt) == nil && receipt.State == "CANCELED" {
			p.Required = false
			p.ReasonCodes = append(p.ReasonCodes, "RECOVERY_CANCELED")
		}
	}
	out := RecoveryStatusResult{Record: r, Projection: recoverystatus.ProjectProjection(p)}
	command := RecoveryStatusCommand{Source: r.Spec.Source, ID: r.ID, RequestID: r.Spec.RequestID, PayloadHash: r.Spec.PayloadHash, ExpectedRecoveryEpoch: r.RuntimeEpoch}
	if p.State == "PREPARED" {
		out.StartCommand = &command
	}
	canResolve := false
	for _, action := range out.Projection.AvailableActions {
		canResolve = canResolve || action == "RESOLVE_RECOVERY_STATUS"
	}
	if canResolve {
		in := recoverystatus.ResolveInput{ID: r.ID, RequestID: r.ID + ":resolve", RuntimeEpoch: r.RuntimeEpoch, AssessmentHash: r.AssessmentHash, ReportHash: r.ReportHash}
		command.RequestID = in.RequestID
		command.PayloadHash = in.ComputePayloadHash()
		out.ResolveCommand = &RecoveryStatusResolveCommand{RecoveryStatusCommand: command, AssessmentHash: r.AssessmentHash, ReportHash: r.ReportHash}
	}
	return out
}
func (f *Facade) recoveryStatusResult(ctx context.Context, r recoverystatus.Record) RecoveryStatusResult {
	var out RecoveryStatusResult
	if r.Spec.OwnerKind == "PROJECT_INSPECTION" {
		if source, err := f.deps.Runs.Get(ctx, r.Spec.RunID); err == nil {
			out = recoveryStatusResult(r, source)
		}
	}
	if out.Record.ID == "" {
		out = recoveryStatusResult(r)
	}
	if r.State == "RESOLVED" && out.Projection.EngineeringDisposition == "REVISION_REQUIRED" && r.Spec.OwnerKind != "PROJECT_INSPECTION" {
		if reader, ok := f.deps.RecoveryStatus.(interface {
			ReadPurposeConfirmation(context.Context, string) (recoverystatus.PurposeConfirmationProjection, error)
		}); ok {
			confirmation, err := reader.ReadPurposeConfirmation(ctx, r.ID)
			if err == nil {
				out.Projection.PurposeConfirmation = &confirmation
			} else {
				out.Projection.PurposeConfirmation = &recoverystatus.PurposeConfirmationProjection{State: "REPAIR_REQUIRED", RepairRequired: true}
				out.Projection.ReasonCodes = append(out.Projection.ReasonCodes, "PURPOSE_READ_UNAVAILABLE")
			}
		}
		out.Projection = recoverystatus.ProjectProjection(out.Projection)
	}
	return out
}
func (f *Facade) recoverySource(ctx context.Context, sessionID string, source recoverystatus.Source) (run.Run, error) {
	v, _, err := f.scopedRun(ctx, sessionID, source.RunID)
	if err != nil {
		return v, err
	}
	if v.OperationKind == run.OperationInspection && (source.OwnerKind != "PROJECT_INSPECTION" || source.OwnerID != v.OperationID || source.WorkspaceID != v.WorkspaceID || v.RuntimeWorkset == nil || source.RuntimeWorksetHash != v.RuntimeWorkset.ContentHash) {
		return v, MapError(recoverystatus.ErrConflict)
	}
	if v.ProjectID != source.ProjectID || v.FeatureAreaID != source.FeatureAreaID || v.TaskID != source.TaskID || string(v.OperationKind) != source.OperationKind || v.OperationID != source.OperationID || v.TurnID != source.TurnID || v.ThreadID != source.ThreadID {
		return v, MapError(recoverystatus.ErrConflict)
	}
	if f.deps.RecoveryStatus == nil {
		return v, invalid("Recovery status service unavailable")
	}
	return v, nil
}
func (f *Facade) PrepareRecoveryStatusForSession(ctx context.Context, sessionID string, spec recoverystatus.Spec) (RecoveryStatusResult, error) {
	if spec.ActorID != "" {
		return RecoveryStatusResult{}, invalid("Recovery actor is server-derived")
	}
	if _, err := f.recoverySource(ctx, sessionID, spec.Source); err != nil {
		return RecoveryStatusResult{}, err
	}
	spec.ActorID = "session:" + sessionID
	return f.prepareRecoveryStatus(ctx, spec)
}
func (f *Facade) prepareRecoveryStatus(ctx context.Context, spec recoverystatus.Spec) (RecoveryStatusResult, error) {
	// Preparation computes a checksum over the exact caller-authored bytes. It
	// never creates a Run or sends a Turn, and never generates instruction text.
	if spec.PayloadHash == "" {
		spec.PayloadHash = spec.ComputePayloadHash()
	}
	if spec.PayloadHash != spec.ComputePayloadHash() {
		return RecoveryStatusResult{}, MapError(recoverystatus.ErrConflict)
	}
	r, err := f.deps.RecoveryStatus.Prepare(ctx, spec)
	return f.recoveryStatusResult(ctx, r), recoveryStatusError(err)
}
func (f *Facade) ReadRecoveryStatusForSession(ctx context.Context, sessionID, id string) (RecoveryStatusResult, error) {
	if f.deps.RecoveryStatus == nil {
		return RecoveryStatusResult{}, invalid("Recovery status service unavailable")
	}
	r, err := f.deps.RecoveryStatus.Read(ctx, id)
	if err != nil {
		return RecoveryStatusResult{}, MapError(err)
	}
	if _, _, err = f.scopedRun(ctx, sessionID, r.Spec.RunID); err != nil {
		return RecoveryStatusResult{}, err
	}
	return f.recoveryStatusResult(ctx, r), nil
}
func (f *Facade) recoveryCommand(ctx context.Context, sessionID string, in RecoveryStatusCommand) (recoverystatus.Record, error) {
	if _, err := f.recoverySource(ctx, sessionID, in.Source); err != nil {
		return recoverystatus.Record{}, err
	}
	r, err := f.deps.RecoveryStatus.Read(ctx, in.ID)
	if err != nil {
		return r, MapError(err)
	}
	if r.Spec.Source != in.Source || r.RuntimeEpoch != in.ExpectedRecoveryEpoch || in.RequestID == "" || in.PayloadHash == "" {
		return r, MapError(recoverystatus.ErrConflict)
	}
	return r, nil
}
func (f *Facade) StartRecoveryStatusForSession(ctx context.Context, sessionID string, in RecoveryStatusCommand) (RecoveryStatusResult, error) {
	r, err := f.recoveryCommand(ctx, sessionID, in)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	if r.Spec.RequestID != in.RequestID || r.Spec.PayloadHash != in.PayloadHash || r.Spec.ActorID != "session:"+sessionID {
		return RecoveryStatusResult{}, MapError(recoverystatus.ErrConflict)
	}
	r, err = f.deps.RecoveryStatus.Start(ctx, in.ID)
	return f.recoveryStatusResult(ctx, r), recoveryStatusError(err)
}
func (f *Facade) ResolveRecoveryStatusForSession(ctx context.Context, sessionID string, in RecoveryStatusResolveCommand) (RecoveryStatusResult, error) {
	if _, err := f.recoveryCommand(ctx, sessionID, in.RecoveryStatusCommand); err != nil {
		return RecoveryStatusResult{}, err
	}
	return f.resolveRecoveryStatus(ctx, "session:"+sessionID, in)
}
func (f *Facade) resolveRecoveryStatus(ctx context.Context, actor string, in RecoveryStatusResolveCommand) (RecoveryStatusResult, error) {
	command := recoverystatus.ResolveInput{ID: in.ID, RequestID: in.RequestID, ActorID: actor, RuntimeEpoch: in.ExpectedRecoveryEpoch, AssessmentHash: in.AssessmentHash, ReportHash: in.ReportHash, Purpose: in.Purpose, PayloadHash: in.PayloadHash}
	r, err := f.deps.RecoveryStatus.Resolve(ctx, command)
	return f.recoveryStatusResult(ctx, r), recoveryStatusError(err)
}
func (f *Facade) RecoveryStatusForSourceForSession(ctx context.Context, sessionID, runID string) (recoverystatus.Projection, error) {
	v, _, err := f.scopedRun(ctx, sessionID, runID)
	if err != nil {
		return recoverystatus.Projection{}, err
	}
	if f.deps.RecoveryStatus != nil {
		r, e := f.deps.RecoveryStatus.LatestForSource(ctx, runID)
		if e == nil {
			return f.recoveryStatusResult(ctx, r).Projection, nil
		}
		if !errors.Is(e, recoverystatus.ErrNotFound) {
			return recoverystatus.Projection{}, MapError(e)
		}
	}
	return recoveryStatusRequired(v), nil
}

func recoveryStatusRequired(v run.Run) recoverystatus.Projection {
	p := recoverystatus.Projection{State: "NOT_REQUIRED", SourceOperationKind: string(v.OperationKind), SourceOperationID: v.OperationID, SourceRunID: v.ID, SourceTurnID: v.TurnID}
	if v.AgentRuntime != nil {
		for _, request := range v.AgentRuntime.Requests {
			if agentruntime.ResponseDisposition(request) == "UNKNOWN" {
				p.SourceResponseDisposition = "UNKNOWN"
				break
			}
		}
	}
	if v.AgentRuntime != nil && (v.AgentRuntime.TurnStatus == "completed" || v.AgentRuntime.TurnStatus == "failed" || v.AgentRuntime.TurnStatus == "interrupted") && v.Result == nil && v.Status != run.StatusSucceeded {
		p.State = "REQUIRED"
		p.ReasonCodes = []string{"RECOVERY_STATUS_REQUIRED"}
	}
	return recoverystatus.ProjectProjection(p)
}
func (f *Facade) recoveryOperator(ctx context.Context, projectID string) (context.Context, *Facade, string, error) {
	ctx, err := localoperator.BindProject(ctx, projectID)
	if err != nil {
		return ctx, nil, "", MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return ctx, nil, "", err
	}
	actor, _ := localoperator.FromContext(ctx)
	return ctx, reader, "operator:" + actor.PrincipalID(), nil
}
func (f *Facade) PrepareRecoveryStatusForOperator(ctx context.Context, spec recoverystatus.Spec) (RecoveryStatusResult, error) {
	if spec.ActorID != "" {
		return RecoveryStatusResult{}, invalid("Recovery actor is server-derived")
	}
	ctx, reader, actor, err := f.recoveryOperator(ctx, spec.ProjectID)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	if _, err = reader.recoverySource(ctx, "", spec.Source); err != nil {
		return RecoveryStatusResult{}, err
	}
	spec.ActorID = actor
	return reader.prepareRecoveryStatus(ctx, spec)
}
func (f *Facade) ReadRecoveryStatusForOperator(ctx context.Context, projectID, id string) (RecoveryStatusResult, error) {
	ctx, reader, _, err := f.recoveryOperator(ctx, projectID)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	return reader.ReadRecoveryStatusForSession(ctx, "", id)
}
func (f *Facade) StartRecoveryStatusForOperator(ctx context.Context, in RecoveryStatusCommand) (RecoveryStatusResult, error) {
	ctx, reader, actor, err := f.recoveryOperator(ctx, in.ProjectID)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	r, err := reader.recoveryCommand(ctx, "", in)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	if r.Spec.ActorID != actor || r.Spec.RequestID != in.RequestID || r.Spec.PayloadHash != in.PayloadHash {
		return RecoveryStatusResult{}, MapError(recoverystatus.ErrConflict)
	}
	r, err = reader.deps.RecoveryStatus.Start(ctx, in.ID)
	return reader.recoveryStatusResult(ctx, r), recoveryStatusError(err)
}
func (f *Facade) ResolveRecoveryStatusForOperator(ctx context.Context, in RecoveryStatusResolveCommand) (RecoveryStatusResult, error) {
	ctx, reader, actor, err := f.recoveryOperator(ctx, in.ProjectID)
	if err != nil {
		return RecoveryStatusResult{}, err
	}
	if _, err = reader.recoveryCommand(ctx, "", in.RecoveryStatusCommand); err != nil {
		return RecoveryStatusResult{}, err
	}
	return reader.resolveRecoveryStatus(ctx, actor, in)
}
func (f *Facade) RecoveryStatusForSourceForOperator(ctx context.Context, projectID, runID string) (recoverystatus.Projection, error) {
	ctx, reader, _, err := f.recoveryOperator(ctx, projectID)
	if err != nil {
		return recoverystatus.Projection{}, err
	}
	return reader.RecoveryStatusForSourceForSession(ctx, "", runID)
}

func recoveryStatusError(err error) error {
	if err == nil {
		return nil
	}
	return MapError(err)
}
