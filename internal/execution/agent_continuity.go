package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

// validateConversationAgentIdentity is a read-only gate on the exact Run that
// first bound this canonical thread. Config fingerprint equality alone cannot
// authorize crossing agents or execution policies. Mutable defaults and launcher
// paths never supply missing provenance.
func (c *Coordinator) validateConversationAgentIdentity(ctx context.Context, conversation agentconversation.Conversation, current run.Run, selected codexruntime.NextRunRuntimeConfig) error {
	if conversation.BackendThreadID == "" {
		return nil
	}
	legacy := selected.AgentID == "" && selected.ExecutionPolicy == ""
	if err := agentpolicy.ValidateFrozenSelection(selected.AgentID, selected.ExecutionPolicy, selected.AgentConfigFingerprint); err != nil {
		return err
	}
	wantAgent, wantPolicy := normalizedConversationSelection(selected)
	conflict := func(reason string) error { return fmt.Errorf("AGENT_CONTINUITY_CONFLICT: %s", reason) }
	unavailable := func(reason string, cause error) error {
		if legacy {
			return nil
		} // compatibility only, never proof for a new selection
		return errors.Join(cause, fmt.Errorf("AGENT_CONTINUITY_UNAVAILABLE: %s", reason))
	}
	if legacy && conversation.Backend != "" && !strings.EqualFold(conversation.Backend, "codex") {
		return conflict("historical conversation names a different agent")
	}
	if conversation.BackendSelectionSourceRunID == "" || c.Runs == nil {
		return unavailable("original source Run is missing", nil)
	}
	source, err := runtimeAuthorityRun(ctx, c.Runs, conversation.BackendSelectionSourceRunID)
	if err != nil {
		return unavailable("original source Run cannot be read", err)
	}
	// A workset is not a substitute for its immutable owner, but positive
	// conflicting evidence must never be ignored by the legacy fallback.
	if source.RuntimeWorkset != nil && (source.RuntimeWorkset.AgentID != "" || source.RuntimeWorkset.ExecutionPolicy != "") {
		observed := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: source.RuntimeWorkset.AgentConfigFingerprint, AgentID: source.RuntimeWorkset.AgentID, ExecutionPolicy: source.RuntimeWorkset.ExecutionPolicy}
		a, p := normalizedConversationSelection(observed)
		if agentpolicy.ValidateFrozenSelection(observed.AgentID, observed.ExecutionPolicy, observed.AgentConfigFingerprint) != nil || a != wantAgent || p != wantPolicy || observed.AgentConfigFingerprint != selected.AgentConfigFingerprint {
			return conflict("source workset belongs to another agent or policy")
		}
	}
	var frozen codexruntime.NextRunRuntimeConfig
	var historicalCodex, exactOwner bool
	switch {
	case source.ExecutionProposalID != "" && source.ReviewID == "":
		if c.Executions == nil {
			return unavailable("source Execution reader is missing", nil)
		}
		execution, readErr := c.Executions.Get(ctx, source.ExecutionProposalID)
		if readErr != nil {
			return unavailable("source Execution cannot be read", readErr)
		}
		frozen = execution.RuntimeConfig
		historicalCodex = execution.RuntimeAdapterIdentity == "codex-app-server-v1" || execution.RuntimeAdapterIdentity == executionproposal.RuntimeAdapterCodexAppServerV1 || runtimeplatform.QualifiedExecutorIdentity(execution.RuntimeAdapterIdentity, execution.RuntimeProtocolIdentity)
		exactOwner = execution.ID == source.ExecutionProposalID && execution.ProjectID == source.ProjectID && execution.TaskID == source.TaskID && (execution.RunID == source.ID || execution.VerificationRunID == source.ID)
	case source.ReviewID != "" && source.ExecutionProposalID == "":
		if c.Reviews == nil {
			return unavailable("source Review reader is missing", nil)
		}
		value, readErr := c.Reviews.Get(ctx, source.ReviewID)
		if readErr != nil {
			return unavailable("source Review cannot be read", readErr)
		}
		frozen = value.RuntimeConfig
		if value.ExecutorKind == review.ExecutorPiAgent && frozen.AgentID == "" && frozen.ExecutionPolicy == "" {
			return conflict("legacy Pi thread has no compatible frozen policy")
		}
		historicalCodex = value.ExecutorKind.Effective() == review.ExecutorCodingAgent
		exactOwner = value.ID == source.ReviewID && value.RunID == source.ID && value.ProjectID == source.ProjectID && value.FeatureAreaID == source.FeatureAreaID && value.TaskID == source.TaskID
	default:
		return unavailable("source Run has no exact Execution or Review owner", nil)
	}
	if err := agentpolicy.ValidateFrozenSelection(frozen.AgentID, frozen.ExecutionPolicy, frozen.AgentConfigFingerprint); err != nil {
		return conflict("source owner has an invalid agent policy")
	}
	gotAgent, gotPolicy := normalizedConversationSelection(frozen)
	if gotAgent != wantAgent || gotPolicy != wantPolicy || frozen.AgentConfigFingerprint != selected.AgentConfigFingerprint {
		return conflict("source owner belongs to another agent or policy")
	}
	if frozen.AgentID == "" && frozen.ExecutionPolicy == "" && !historicalCodex {
		return unavailable("historical Codex identity is unproven", nil)
	}
	if !exactOwner || source.ID != conversation.BackendSelectionSourceRunID || source.ThreadID != conversation.BackendThreadID || conversation.BackendSelectionSourceThreadID != conversation.BackendThreadID || source.ProjectID != current.ProjectID || source.FeatureAreaID != current.FeatureAreaID {
		return unavailable("source ownership or thread identity is incomplete", nil)
	}
	return nil
}

func normalizedConversationSelection(value codexruntime.NextRunRuntimeConfig) (string, string) {
	agentID, policy := value.AgentID, value.ExecutionPolicy
	if agentID == "" {
		agentID = "codex"
	}
	if policy == "" {
		policy = agentpolicy.PolicyTaskSandboxV1
	}
	return agentID, policy
}
