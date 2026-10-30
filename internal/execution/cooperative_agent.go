package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

type AgentPolicyRecorder interface {
	RecordAgentPolicy(context.Context, string, agentpolicy.Envelope) error
	RecordAgentPolicyReceipt(context.Context, string, agentpolicy.LaunchReceipt) error
}

// startCooperativeAgent launches only an explicitly selected prospective policy.
// It does not wrap the task prompt, grant native permissions, or adopt recovery.
func (c *Coordinator) startCooperativeAgent(ctx context.Context, prepared PreparedRun, backend codexconfig.Config, selected codexruntime.NextRunRuntimeConfig, cwd, existingThread string) (agent.Session, error) {
	if selected.ExecutionPolicy != agentpolicy.PolicyCooperativeTrustedV1 || agentpolicy.ValidateFrozenSelection(selected.AgentID, selected.ExecutionPolicy, selected.AgentConfigFingerprint) != nil {
		return nil, errors.New("explicit cooperative agent selection required")
	}
	if prepared.rejoin != nil || prepared.qualifiedExecution != nil {
		return nil, errors.New("cooperative policy cannot adopt native recovery or qualification")
	}
	required := []agent.Capability{agent.CapabilityCooperativePolicy}
	if selected.ServiceTier != "" {
		required = append(required, agent.CapabilityServiceTier)
	}
	if existingThread != "" {
		required = append(required, agent.CapabilityThreadResume)
	}
	if c.Cancellations != nil {
		required = append(required, agent.CapabilityTurnStarted, agent.CapabilityInterrupt)
	}
	factory, err := c.Agents.Select(selected.AgentID, required...)
	if err != nil {
		return nil, err
	}
	descriptor := factory.AgentDescriptor()
	if descriptor.ID != selected.AgentID || selected.AgentConfigFingerprint == "" || descriptor.ConfigFingerprint != selected.AgentConfigFingerprint {
		return nil, errors.New("registered agent configuration differs from frozen approval")
	}
	recorder, ok := c.Results.(AgentPolicyRecorder)
	if !ok {
		return nil, errors.New("durable cooperative policy recorder unavailable")
	}
	readOnly := prepared.purpose == run.PurposeVerification || prepared.purpose == run.PurposeScopeReview
	envelope := agentpolicy.Envelope{AgentConfigFingerprint: selected.AgentConfigFingerprint, Version: agentpolicy.EnvelopeVersion, Policy: selected.ExecutionPolicy, AgentID: selected.AgentID, RunID: prepared.Run.ID, TaskID: prepared.Run.TaskID, ReviewID: prepared.Run.ReviewID, ExecutionID: prepared.Run.ExecutionProposalID, RevisionID: prepared.task.CurrentDefinitionRevisionID, Purpose: string(prepared.purpose), CWD: cwd, ReadOnly: readOnly, ReadableRoots: []string{cwd}}
	if !readOnly {
		envelope.WritableRoots = []string{cwd}
	}
	if permissions := prepared.task.WorkSpec.CommandPermissions; permissions != nil {
		envelope.NetworkAccess = permissions.NetworkAccess
	}
	scope := map[string]any{"executionScope": prepared.executionScope}
	if prepared.purpose == run.PurposeScopeReview {
		scope = map[string]any{"reviewScope": prepared.reviewScope}
	}
	runtime := prepared.multiRuntime
	if runtime == nil {
		runtime = prepared.scalarRuntime
	}
	if runtime != nil {
		if runtime.Binding.TaskPermissions != nil || runtime.Workset.TaskPermissions != nil {
			return nil, errors.New("cooperative policy cannot reinterpret native TaskPermissions")
		}
		if runtime.Workset.AgentConfigFingerprint != selected.AgentConfigFingerprint || runtime.Workset.AgentID != selected.AgentID || runtime.Workset.ExecutionPolicy != selected.ExecutionPolicy || runtime.Workset.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || runtime.Workset.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity {
			return nil, errors.New("cooperative workset identity mismatch")
		}
		if err := runtime.Workset.Validate(); err != nil {
			return nil, err
		}
		boundCWD, boundBinding, bindingErr := multiRuntimeClientBinding(runtime.Workset)
		if bindingErr != nil || boundCWD != runtime.CWD {
			return nil, errors.Join(bindingErr, errors.New("cooperative workset CWD mismatch"))
		}
		if readOnly {
			boundBinding.WritableRoots = nil
		}
		if !sameRuntimeBinding(boundBinding, runtime.Binding) {
			return nil, errors.New("cooperative workset binding mismatch")
		}
		envelope.CWD = runtime.CWD
		envelope.ReadableRoots = append([]string(nil), runtime.Binding.RuntimeWorkspaceRoots...)
		envelope.WritableRoots = append([]string(nil), runtime.Binding.WritableRoots...)
		if readOnly {
			envelope.WritableRoots = nil
		}
		envelope.SourceWorksetHash = runtime.Workset.ContentHash
		// Members carry exact engineering write scopes, not instruction text.
		type memberScope struct {
			MemberID string             `json:"memberId"`
			Root     string             `json:"root"`
			Write    workset.WriteScope `json:"write"`
		}
		members := make([]memberScope, 0, len(runtime.Workset.Members))
		for _, member := range runtime.Workset.Members {
			members = append(members, memberScope{MemberID: member.MemberID, Root: member.CanonicalRoot, Write: member.Write})
		}
		scope["members"] = members
	}
	// Host scratch is explicitly cooperative, not a native sandbox/profile.
	// Retain it after launch (including uncertain outcomes) as recorded facts;
	// deleting possibly agent-owned files is not proof that execution stopped.
	scratch, scratchErr := os.MkdirTemp("", "devbridge-agent-scratch-")
	if scratchErr != nil {
		return nil, scratchErr
	}
	scratch, scratchErr = filepath.EvalSymlinks(scratch)
	if scratchErr != nil {
		return nil, scratchErr
	}
	envelope.ScratchRoots = []string{scratch}
	envelope.EngineeringScope, err = json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	envelope, err = envelope.Seal()
	if err != nil {
		return nil, err
	}
	if err = recorder.RecordAgentPolicy(ctx, prepared.Run.ID, envelope); err != nil {
		return nil, err
	}
	// Pin receipt comparison to immutable strings, not the adapter-owned pointer.
	expectedRun, expectedAgent, expectedHash := envelope.RunID, envelope.AgentID, envelope.SHA256
	var mu sync.Mutex
	configured, returned := false, false
	var receiptErr error
	record := func(receipt agentpolicy.LaunchReceipt) error {
		mu.Lock()
		defer mu.Unlock()
		if returned {
			return errors.New("cooperative launch receipt is late")
		}
		if configured || receiptErr != nil {
			receiptErr = errors.Join(receiptErr, errors.New("cooperative launch receipt is duplicate"))
			return receiptErr
		}
		if err := receipt.Validate(); err != nil {
			receiptErr = err
			return err
		}
		if receipt.RunID != expectedRun || receipt.AgentID != expectedAgent || receipt.PolicySHA256 != expectedHash || receipt.AgentConfigFingerprint != selected.AgentConfigFingerprint {
			receiptErr = errors.New("cooperative launch receipt identity mismatch")
			return receiptErr
		}
		receiptErr = recorder.RecordAgentPolicyReceipt(ctx, expectedRun, receipt)
		configured = receiptErr == nil
		return receiptErr
	}
	request := agent.LaunchRequest{AgentConfigFingerprint: selected.AgentConfigFingerprint, RunID: prepared.Run.ID, Backend: agent.BackendConfig{ID: backend.ID, Name: backend.Name, Fingerprint: backend.Fingerprint(), Enabled: backend.Enabled, CreatedAt: backend.CreatedAt, UpdatedAt: backend.UpdatedAt}, Runtime: agent.RuntimeSelection{Model: selected.Model, ReasoningEffort: selected.ReasoningEffort, ServiceTier: selected.ServiceTier}, CWD: envelope.CWD, Policy: &envelope, OnPolicyConfigured: record}
	session, startErr := factory.StartAgent(ctx, request)
	mu.Lock()
	returned = true
	recordErr := receiptErr
	recorded := configured
	mu.Unlock()
	if startErr == nil && (!recorded || recordErr != nil) {
		startErr = errors.Join(recordErr, errors.New("cooperative policy launch receipt was not durably recorded"))
	}
	if startErr == nil && session == nil {
		startErr = errors.New("cooperative factory returned no session")
	}
	if startErr == nil {
		startErr = validateAgentSessionCapabilities(factory, session, required)
	}
	if startErr != nil {
		if session != nil {
			startErr = errors.Join(startErr, session.Close())
		}
		return nil, startErr
	}
	return session, nil
}
