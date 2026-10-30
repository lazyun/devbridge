package controlapi

import (
	"context"
	"errors"
	"sort"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
)

const backendInventoryLimit = 100

// BackendSelectionOptions is the complete, scoped backend inventory exposed to
// confirmation. Disabled and malformed entries are never silently selected.
type BackendSelectionOptions struct {
	Candidates []codexconfig.BackendSelection       `json:"candidates"`
	Complete   bool                                 `json:"complete"`
	ReadError  string                               `json:"readError,omitempty"`
	Continuity *agentconversation.BackendContinuity `json:"continuity,omitempty"`
}

func (f *Facade) resolveBackendSelection(ctx context.Context, session, project, featureAreaID string, selected *codexconfig.BackendSelectionInput) (codexconfig.BackendSelection, error) {
	options, err := f.backendSelectionOptions(ctx, session, project, featureAreaID)
	if err != nil {
		return codexconfig.BackendSelection{}, err
	}
	if selected == nil || selected.ConfigID == "" {
		return codexconfig.BackendSelection{}, errors.New("explicit backend selection required")
	}
	if options.Continuity != nil && options.Continuity.State != agentconversation.BackendContinuityThreadless && options.Continuity.State != agentconversation.BackendContinuityBoundAvailable {
		return codexconfig.BackendSelection{}, errors.New(options.Continuity.Reason)
	}
	for _, candidate := range options.Candidates {
		if candidate.ConfigID == selected.ConfigID && candidate.ConfigFingerprint == selected.ConfigFingerprint {
			if options.Continuity != nil && options.Continuity.State == agentconversation.BackendContinuityBoundAvailable && options.Continuity.BoundBackend != nil {
				return *options.Continuity.BoundBackend, nil
			}
			return candidate, nil
		}
	}
	return codexconfig.BackendSelection{}, errors.New("selected backend is unavailable or changed")
}

// validateFrozenBackendSelection checks one already-authorized Execution
// binding without expanding it into a new inventory-based choice. Historical
// name/source stay frozen; current enabled state and functional fingerprint do
// not.
func (f *Facade) validateFrozenBackendSelection(ctx context.Context, session, project, featureAreaID string, selected *codexconfig.BackendSelection) error {
	if selected == nil || selected.ConfigID == "" || selected.ConfigFingerprint == "" {
		return errors.New("frozen backend selection unavailable")
	}
	var config codexconfig.Config
	var err error
	if session != "" {
		reader, ok := f.deps.StartupContext.(interface {
			GetCodexConfig(context.Context, string, string) (codexconfig.Config, error)
		})
		if !ok {
			return errors.New("backend inventory unavailable")
		}
		config, err = reader.GetCodexConfig(ctx, session, selected.ConfigID)
	} else {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.ProjectID() != project || f.deps.CodexConfigs == nil {
			return errors.New("backend scope unavailable")
		}
		config, err = f.deps.CodexConfigs.Get(ctx, selected.ConfigID)
	}
	if err != nil {
		return err
	}
	if !config.Enabled || config.ID != selected.ConfigID || config.Fingerprint() != selected.ConfigFingerprint {
		return errors.New("frozen backend is unavailable or changed")
	}
	options, err := f.backendSelectionOptions(ctx, session, project, featureAreaID)
	if err != nil {
		return err
	}
	if options.Continuity != nil && options.Continuity.State != agentconversation.BackendContinuityThreadless {
		bound := options.Continuity.BoundBackend
		if options.Continuity.State != agentconversation.BackendContinuityBoundAvailable || bound == nil || bound.ConfigID != selected.ConfigID || bound.ConfigFingerprint != selected.ConfigFingerprint {
			return errors.New(options.Continuity.Reason)
		}
	}
	return nil
}

func (f *Facade) backendSelectionOptions(ctx context.Context, session, project, featureAreaID string) (BackendSelectionOptions, error) {
	var configs []codexconfig.Config
	var err error
	if session != "" {
		if f.deps.StartupContext == nil {
			return BackendSelectionOptions{}, errors.New("backend inventory unavailable")
		}
		configs, err = f.deps.StartupContext.ListCodexConfigs(ctx, session, aicontext.ListOptions{Limit: backendInventoryLimit})
	} else if f.deps.CodexConfigs != nil {
		configs, err = f.deps.CodexConfigs.List(ctx)
	} else {
		return BackendSelectionOptions{}, errors.New("backend inventory unavailable")
	}
	if err != nil {
		return BackendSelectionOptions{}, err
	}
	actor, ok := localoperator.FromContext(ctx)
	if session == "" && (!ok || actor.ProjectID() != project) {
		return BackendSelectionOptions{}, errors.New("backend scope unavailable")
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].ID < configs[j].ID })
	out := BackendSelectionOptions{Complete: len(configs) < backendInventoryLimit}
	for _, config := range configs {
		if config.Enabled && config.ID != "" {
			source := "SESSION_INVENTORY"
			if session == "" {
				source = "OPERATOR_INVENTORY"
			}
			out.Candidates = append(out.Candidates, config.Selection(source))
		}
	}
	continuity := &agentconversation.BackendContinuity{State: agentconversation.BackendContinuityThreadless}
	out.Continuity = continuity
	if featureAreaID == "" {
		return out, nil
	}
	reader, ok := f.deps.AgentConversations.(interface {
		Current(context.Context, string) (agentconversation.Conversation, error)
	})
	if !ok {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityReadUnavailable, "Feature Area backend continuity reader is unavailable"
		out.Candidates = nil
		return out, nil
	}
	conversation, readErr := reader.Current(ctx, featureAreaID)
	if errors.Is(readErr, agentconversation.ErrNotFound) {
		return out, nil
	}
	if readErr != nil {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityReadUnavailable, readErr.Error()
		out.Candidates = nil
		return out, nil
	}
	continuity.ConversationID, continuity.ThreadID = conversation.ID, conversation.BackendThreadID
	if conversation.ProjectID != project || conversation.FeatureAreaID != featureAreaID {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityAssociationConflict, "Feature Area backend continuity association conflicts with the requested scope"
		out.Candidates = nil
		return out, nil
	}
	if conversation.BackendThreadID == "" {
		return out, nil
	}
	if conversation.BackendSelection == nil || conversation.BackendSelectionSourceRunID == "" || conversation.BackendSelectionSourceThreadID == "" {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityHistoricalUnknown, "The existing Thread has no durable backend creation evidence"
		out.Candidates = nil
		return out, nil
	}
	if conversation.BackendSelectionSourceThreadID != conversation.BackendThreadID {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityAssociationConflict, "The persisted backend evidence does not belong to the canonical Thread"
		out.Candidates = nil
		return out, nil
	}
	sourceRun, sourceErr := f.deps.Runs.Get(ctx, conversation.BackendSelectionSourceRunID)
	if sourceErr != nil {
		out.Candidates = nil
		if errors.Is(sourceErr, run.ErrNotFound) {
			continuity.State, continuity.Reason = agentconversation.BackendContinuityAssociationConflict, "The persisted backend source Run no longer exists"
		} else {
			continuity.State, continuity.Reason = agentconversation.BackendContinuityReadUnavailable, sourceErr.Error()
		}
		return out, nil
	}
	if sourceRun.ProjectID != project || sourceRun.FeatureAreaID != featureAreaID || sourceRun.ThreadID != conversation.BackendThreadID || sourceRun.OperationID == "" || sourceRun.OperationKind != run.OperationReview && sourceRun.OperationKind != run.OperationExecution {
		continuity.State, continuity.Reason = agentconversation.BackendContinuityAssociationConflict, "The persisted backend source Run does not own the canonical Thread"
		out.Candidates = nil
		return out, nil
	}
	bound := *conversation.BackendSelection
	continuity.SourceRunID, continuity.SourceOperationKind, continuity.SourceOperationID = conversation.BackendSelectionSourceRunID, string(sourceRun.OperationKind), sourceRun.OperationID
	continuity.Evidence, continuity.BoundBackend = conversation.BackendSelectionEvidence, &bound
	for _, candidate := range out.Candidates {
		if candidate.ConfigID == bound.ConfigID && candidate.ConfigFingerprint == bound.ConfigFingerprint {
			continuity.State = agentconversation.BackendContinuityBoundAvailable
			out.Candidates, out.Complete = []codexconfig.BackendSelection{bound}, true
			return out, nil
		}
	}
	continuity.State, continuity.Reason = agentconversation.BackendContinuityBoundUnavailable, "The backend that created the canonical Thread is disabled, missing, or has changed"
	out.Candidates = nil
	return out, nil
}
