package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/inspectionresult"

	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// InspectionRecoveryOwner exposes the existing canonical Inspection owner.
// Detachment closes only its process-local connection after the shared Recovery
// admission has proved the exact source Turn terminal and its Thread idle.
// Durable admission, child authority and resolution stay in RecoveryStatusStore.
type InspectionRecoveryOwner interface {
	InspectionState(context.Context, string) (probeexecution.CanonicalState, error)
	DetachRecoverySource(context.Context, string) error
}

func (s *RecoveryStatusService) validateInspectionOwnerFacts(ctx context.Context, source run.Run, report recoverystatus.Report, facts *recoverystatus.FactualObservation) error {
	facts.SourcePurpose = string(run.PurposeInspection)
	facts.OwnerContractValidated = false
	if source.RuntimeWorkset == nil {
		return recoverystatus.ErrIneligible
	}
	state, err := s.inspectionSource(ctx, source, facts.Source, *source.RuntimeWorkset)
	if err != nil {
		return err
	}
	if probeexecution.InspectionObservationStatus(state.Before, facts.Before) != "UNCHANGED" {
		facts.ScopeValid = false
		return nil
	}
	result, err := inspectionresult.Parse(report.InspectionResult, *source.RuntimeWorkset)
	if err != nil {
		return nil
	}
	if (report.OverallStatus == "COMPLETED" && result.Status != "SUCCEEDED") || (report.OverallStatus == "PARTIAL" && result.Status != "BLOCKED") {
		return nil
	}
	facts.OwnerResult = append(json.RawMessage(nil), report.InspectionResult...)
	material, err := json.Marshal(struct {
		Receipt                     json.RawMessage
		SourceWorksetHash           string
		SourceBefore, Before, After []recoverystatus.WorktreeObservation
		Result                      json.RawMessage
	}{state.Spec.Receipt, state.Workset.ContentHash, state.Before, facts.Before, facts.After, facts.OwnerResult})
	if err != nil {
		return err
	}
	facts.OwnerContractEvidenceHash = recoverystatus.Hash(material)
	facts.OwnerContractValidated = true
	return nil
}

func (s *RecoveryStatusService) inspectionSource(ctx context.Context, source run.Run, spec recoverystatus.Source, w workset.RuntimeWorkset) (probeexecution.CanonicalState, error) {
	if s.InspectionOwner == nil {
		return probeexecution.CanonicalState{}, errors.New("canonical Inspection recovery owner unavailable")
	}
	state, err := s.InspectionOwner.InspectionState(ctx, source.OperationID)
	if err != nil {
		return state, err
	}
	if spec.OwnerKind != "PROJECT_INSPECTION" || spec.OwnerID != source.OperationID || spec.WorkspaceID != w.WorkspaceID || state.Spec.WorkspaceID != w.WorkspaceID {
		return state, recoverystatus.ErrIneligible
	}
	canonicalOwner := source.ContextProbeID == source.OperationID
	// The current owner snapshot exposes this marker only for the immutable
	// same-operation continuation receipt, never for an arbitrary NULL owner.
	continuationOwner := source.ContextProbeID == "" && state.ContinuationRecoveryStatusID != "" && state.ContinuationSourceTurnID != ""
	if source.Purpose != run.PurposeInspection || source.OperationKind != run.OperationInspection || source.FeatureAreaID != "" || source.ExecutionProposalID != "" || source.ReviewID != "" || (!canonicalOwner && !continuationOwner) || state.Spec.Probe.Canonical == nil || state.Finalization != nil || state.Spec.Probe.ID != source.OperationID || state.Spec.Probe.TaskID != source.TaskID || state.Spec.Probe.ProjectID != source.ProjectID || state.Run.ID != source.ID || state.Run.RuntimeEpoch != spec.RuntimeEpoch || state.Run.ThreadID != spec.ThreadID || state.Run.TurnID != spec.TurnID || state.Conversation.ID != spec.AgentConversationID || state.Conversation.BackendThreadID != spec.ThreadID || w.ProtocolVersion != workset.RuntimeProtocolVersionProjectInspection || w.OwnerKind != workset.RuntimeOwnerProjectInspection || w.InspectionOperationID != source.OperationID || w.InspectionOwnerTaskID != source.TaskID || w.RunID != source.ID || w.WorkspaceID != source.WorkspaceID || w.ContentHash != state.Workset.ContentHash || w.ContentHash != spec.RuntimeWorksetHash {
		return state, recoverystatus.ErrIneligible
	}
	return state, nil
}

func (s *RecoveryStatusService) detachSource(ctx context.Context, r recoverystatus.Record) error {
	if r.Spec.OperationKind == string(run.OperationInspection) {
		if s.InspectionOwner == nil {
			return recoverystatus.ErrIneligible
		}
		return s.InspectionOwner.DetachRecoverySource(ctx, r.Spec.RunID)
	}
	if s.Supervisor != nil {
		return s.Supervisor.detachRecoverySource(ctx, r.Spec.RunID)
	}
	return nil
}
